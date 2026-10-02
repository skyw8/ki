package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/codemode"
	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/toggles"
	toolapi "ki/internal/tool"
	"ki/internal/types"
)

const (
	mcpSearchName = "mcp__fixture__search_documents"
	mcpReadName   = "mcp__fixture__get_document"
)

type mcpServerFixture struct {
	url         string
	searchCalls atomic.Int32
	readCalls   atomic.Int32
}

func newMCPServerFixture(t *testing.T) *mcpServerFixture {
	t.Helper()
	f := &mcpServerFixture{}
	server := mcp.NewServer(&mcp.Implementation{Name: "ki-integration", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "search_documents", Description: "Find fixture documents. unique_search_token.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"mcp_query_unique":{"type":"string"}},"required":["mcp_query_unique"],"additionalProperties":false}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.searchCalls.Add(1)
		var args map[string]string
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "MCP_SEARCH_RAW:" + args["mcp_query_unique"]}},
		}, nil
	})
	server.AddTool(&mcp.Tool{
		Name: "get_document", Description: "Read fixture document contents. unique_read_token.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"document_id_unique":{"type":"string"}},"required":["document_id_unique"],"additionalProperties":false}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.readCalls.Add(1)
		var args map[string]string
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "MCP_DOCUMENT_RAW:" + args["document_id_unique"]}},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	f.url = httpServer.URL
	return f
}

func mcpServerConfig(t *testing.T, url string) config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Compaction.Enabled = false
	cfg.MCPServers = map[string]config.MCPServer{"fixture": {URL: url}}
	return cfg
}

func newMCPIntegrationServer(t *testing.T, cfg config.Config, streamer loop.Streamer) *Server {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config: cfg, Token: "tok", Streamer: streamer,
		CodeModeConfig: &codemode.Config{
			Executable: executable,
			Args:       []string{"-test.run=^TestCodeModeServerWorkerProcess$", "--", codemode.WorkerArg},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return srv
}

func mcpRequestSpec(req loop.Request, name string) (toolapi.Spec, bool) {
	for _, spec := range req.Tools {
		if spec.Name == name {
			return spec, true
		}
	}
	return toolapi.Spec{}, false
}

func mcpAssertLoadedSchema(t *testing.T, req loop.Request, loaded bool) {
	t.Helper()
	_, direct := mcpRequestSpec(req, mcpSearchName)
	exec, ok := mcpRequestSpec(req, "exec")
	if !ok || strings.Contains(exec.Description, "mcp_query_unique") != loaded {
		t.Fatalf("loaded=%v exec description=%s", loaded, exec.Description)
	}
	if strings.Contains(exec.Description, "document_id_unique") {
		t.Fatal("searching one MCP tool exposed the other tool's schema")
	}
	if direct != loaded {
		t.Fatalf("loaded=%v direct=%v tools=%v", loaded, direct, requestToolNames(req.Tools))
	}
	if _, present := mcpRequestSpec(req, mcpReadName); present {
		t.Fatal("searching one MCP tool advertised the other tool")
	}
	if loaded {
		spec, _ := mcpRequestSpec(req, mcpSearchName)
		if !strings.Contains(codeModeJSON(t, spec.Parameters), "mcp_query_unique") {
			t.Fatalf("loaded tool missing input schema: %+v", spec)
		}
	}
}

func TestMCPServerSchemaExposureMatrix(t *testing.T) {
	f := newMCPServerFixture(t)
	for _, api := range []string{"completions", "responses", "anthropic"} {
		t.Run(api, func(t *testing.T) {
			var got loop.Request
			srv := newMCPIntegrationServer(t, mcpServerConfig(t, f.url), codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
				got = req
				return codeModeStop(), nil
			}))
			id := codeModeTestSession(t, srv, t.TempDir(), api)
			codeModeRunPrompt(t, srv, id, "MCP exposure")
			search, ok := mcpRequestSpec(got, "search_tool")
			if !ok || search.Type != "function" || search.Parameters == nil {
				t.Fatalf("portable search schema missing: %+v", got.Tools)
			}
			mcpAssertLoadedSchema(t, got, false)
			exec, _ := mcpRequestSpec(got, "exec")
			if strings.Contains(exec.Description, "mcp_query_unique") || strings.Contains(exec.Description, "document_id_unique") {
				t.Fatal("deferred MCP schemas leaked into initial exec description")
			}
			if (exec.Type == "custom") != (api == "responses") {
				t.Fatalf("wrong exec protocol shape: api=%s spec=%+v", api, exec)
			}
		})
	}
	if f.searchCalls.Load() != 0 || f.readCalls.Load() != 0 {
		t.Fatal("catalog exposure executed MCP tools")
	}
}

func TestMCPServerDisabledSearchFallsBackToDirect(t *testing.T) {
	f := newMCPServerFixture(t)
	cfg := mcpServerConfig(t, f.url)
	if err := toggles.Save(cfg.Home, toggles.File{Tools: session.Toggle{Disabled: []string{"search_tool"}}}); err != nil {
		t.Fatal(err)
	}
	var got loop.Request
	srv := newMCPIntegrationServer(t, cfg, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		got = req
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, t.TempDir(), "completions")
	codeModeRunPrompt(t, srv, id, "search disabled")
	if _, present := mcpRequestSpec(got, "search_tool"); present {
		t.Fatal("disabled search was advertised")
	}
	exec, _ := mcpRequestSpec(got, "exec")
	for _, field := range []string{"mcp_query_unique", "document_id_unique"} {
		if !strings.Contains(exec.Description, field) {
			t.Fatalf("disabled search stranded nested MCP tool: missing %s", field)
		}
	}
	for _, name := range []string{mcpSearchName, mcpReadName} {
		if _, present := mcpRequestSpec(got, name); !present {
			t.Fatalf("disabled search stranded direct MCP tool: %s", name)
		}
	}
}

func TestMCPServerSearchPromotesOnlyLiveSchemasAcrossOccupies(t *testing.T) {
	f := newMCPServerFixture(t)
	cfg := mcpServerConfig(t, f.url)
	step := 0
	srv := newMCPIntegrationServer(t, cfg, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		step++
		mcpAssertLoadedSchema(t, req, step > 1)
		if step == 1 {
			return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{
				Type: "toolCall", ID: "discover-mcp", Name: "search_tool",
				Arguments: map[string]any{"query": "unique_search_token", "limit": 1},
			}}}, nil
		}
		if step == 2 {
			_, results := codeModeTurn(req)
			if len(results) != 1 || results[0].ToolName != "search_tool" || results[0].IsError {
				t.Fatalf("discovery result: %+v", results)
			}
			if !strings.Contains(results[0].Text(), mcpSearchName) || !strings.Contains(results[0].Text(), "mcp_query_unique") {
				t.Fatalf("discovery did not disclose selected schema: %s", results[0].Text())
			}
			var details struct {
				Names []string `json:"discovered_tools"`
			}
			if err := json.Unmarshal([]byte(codeModeJSON(t, results[0].Details)), &details); err != nil || !slices.Equal(details.Names, []string{mcpSearchName}) {
				t.Fatalf("discovery metadata=%+v error=%v", results[0].Details, err)
			}
		}
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, t.TempDir(), "completions")
	codeModeRunPrompt(t, srv, id, "discover")
	codeModeRunPrompt(t, srv, id, "resume discovery")
	if step != 3 {
		t.Fatalf("request count=%d", step)
	}
	// Reopen persisted history with a newly filtered live MCP catalog.
	// Old discovery names must not resurrect a now-disabled capability.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.MCPServers = map[string]config.MCPServer{"fixture": {URL: f.url, DisabledTools: []string{"search_documents"}}}
	reopened := newMCPIntegrationServer(t, cfg, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		mcpAssertLoadedSchema(t, req, false)
		exec, _ := mcpRequestSpec(req, "exec")
		if strings.Contains(exec.Description, "mcp_query_unique") {
			t.Fatal("disabled MCP schema resurrected from discovery history")
		}
		return codeModeStop(), nil
	}))
	codeModeRunPrompt(t, reopened, id, "after MCP removal")
}

const mcpAfterToolWorkerArg = "__mcp-after-tool-fixture"

// The lifecycle fixture uses the current test executable, avoiding an extra Go
// build and portable-shell dependency. Exit before testing prints its banner.
func TestMCPServerAfterToolProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != mcpAfterToolWorkerArg {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(1)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		switch request.Method {
		case "initialize":
			result = map[string]any{"subscriptions": []any{map[string]any{"event": "tool_result", "mode": "sync"}}}
		case "lifecycle.invoke":
			var params struct {
				Payload struct {
					Call struct {
						Name string `json:"name"`
					} `json:"call"`
					Result map[string]any `json:"result"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				os.Exit(1)
			}
			result = params.Payload.Result
			if strings.HasPrefix(params.Payload.Call.Name, "mcp__fixture__") {
				params.Payload.Result["content"] = []types.Content{{Type: "text", Text: "HOOKED_MCP_RESULT"}}
			}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(1)
		}
	}
	if scanner.Err() != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func mcpInstallAfterToolFixture(t *testing.T, home string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "extensions", "mcp-result-policy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name": "mcp-result-policy", "capabilities": []string{"lifecycle"},
		"runtime": map[string]any{
			"kind": "rpc", "command": executable,
			"args": []string{"-test.run=^TestMCPServerAfterToolProcess$", "--", mcpAfterToolWorkerArg},
		},
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(codeModeJSON(t, manifest)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPServerCodeCanInvokeDeferredBeforeSearchWithPolicyAndAudit(t *testing.T) {
	f := newMCPServerFixture(t)
	cfg := mcpServerConfig(t, f.url)
	cfg.MCPServers = map[string]config.MCPServer{"fixture": {URL: f.url, DisabledTools: []string{"get_document"}}}
	mcpInstallAfterToolFixture(t, cfg.Home)
	var outer types.Message
	source := fmt.Sprintf(`
if (typeof tools.search_tool !== "undefined") throw new Error("search must stay top-level");
if (!ALL_TOOLS.some(t => t.name === %q)) throw new Error("deferred tool missing");
if (ALL_TOOLS.some(t => t.name === %q) || typeof tools[%q] !== "undefined") throw new Error("disabled tool leaked");
const result = await tools[%q]({mcp_query_unique:"before-search"});
if (result.isError) throw new Error(JSON.stringify(result));
text(result.content);
`, mcpSearchName, mcpReadName, mcpReadName, mcpSearchName)
	srv := newMCPIntegrationServer(t, cfg, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) == 0 {
			mcpAssertLoadedSchema(t, req, false)
			return codeModeExecCall(req, "outer-mcp", source), nil
		}
		if len(results) != 1 || results[0].ToolName != "exec" {
			t.Fatalf("nested MCP leaked provider result: %+v", results)
		}
		outer = results[0]
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, t.TempDir(), "responses")
	st := codeModeRunPrompt(t, srv, id, "call deferred MCP without discovery")
	if outer.IsError || !strings.Contains(outer.Text(), "HOOKED_MCP_RESULT") || strings.Contains(outer.Text(), "MCP_SEARCH_RAW") {
		t.Fatalf("post-hook result did not reach JS: %+v", outer)
	}
	if f.searchCalls.Load() != 1 || f.readCalls.Load() != 0 {
		t.Fatalf("MCP calls search=%d read=%d", f.searchCalls.Load(), f.readCalls.Load())
	}
	var starts, ends int
	for _, entry := range codeModeEntries(t, srv, id) {
		if entry.Message != nil && entry.Message.Role == "toolResult" && entry.Message.ToolName != "exec" {
			t.Fatalf("nested MCP transcript result: %+v", entry.Message)
		}
		if entry.Type != string(loop.ToolExecutionStart) && entry.Type != string(loop.ToolExecutionEnd) {
			continue
		}
		raw := codeModeJSON(t, entry.Details)
		var audit struct {
			ToolName     string `json:"toolName"`
			ToolCallID   string `json:"toolCallId"`
			ParentCallID string `json:"parentCallId"`
			CellID       string `json:"cellId"`
		}
		if err := json.Unmarshal([]byte(raw), &audit); err != nil {
			t.Fatal(err)
		}
		if audit.ToolName != mcpSearchName {
			continue
		}
		if audit.ParentCallID != "outer-mcp" || audit.CellID == "" || !strings.HasPrefix(audit.ToolCallID, "outer-mcp:nested-") {
			t.Fatalf("nested MCP lost host attribution: %s", raw)
		}
		if entry.Type == string(loop.ToolExecutionStart) {
			starts++
		} else {
			ends++
			if !strings.Contains(raw, "HOOKED_MCP_RESULT") || strings.Contains(raw, "MCP_SEARCH_RAW") {
				t.Fatalf("audit recorded pre-policy result: %s", raw)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("durable MCP audits start=%d end=%d", starts, ends)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	var sseStarts, sseEnds int
	for _, ev := range st.evs {
		if ev.ToolName != mcpSearchName || ev.ParentCallID != "outer-mcp" {
			continue
		}
		if ev.EntryID == "" || ev.CellID == "" || ev.RunID != st.runID {
			t.Fatalf("MCP SSE attribution: %+v", ev)
		}
		if ev.Type == loop.ToolExecutionStart {
			sseStarts++
		} else if ev.Type == loop.ToolExecutionEnd {
			sseEnds++
		}
	}
	if sseStarts != 1 || sseEnds != 1 {
		t.Fatalf("SSE MCP audits start=%d end=%d", sseStarts, sseEnds)
	}
}
