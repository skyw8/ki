package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/config"
	"ki/internal/tool"
)

func sdkServer(options *sdk.ServerOptions) *sdk.Server {
	return sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, options)
}

func addTool(s *sdk.Server, name, schema string, fn sdk.ToolHandler) {
	s.AddTool(&sdk.Tool{Name: name, Description: "Description " + name, InputSchema: json.RawMessage(schema)}, fn)
}

func httpManager(t *testing.T, s *sdk.Server, cfg config.MCPServer) (*Manager, *httptest.Server) {
	t.Helper()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, nil)
	hs := httptest.NewServer(handler)
	cfg.URL = hs.URL
	m := NewManager(map[string]config.MCPServer{"fixture": cfg})
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close MCP: %v", err)
		}
		hs.Close()
	})
	return m, hs
}

func toolsFor(t *testing.T, m *Manager) []tool.Tool {
	t.Helper()
	tools, err := m.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func find(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	i := slices.IndexFunc(tools, func(t tool.Tool) bool { return t.Name() == name })
	if i < 0 {
		t.Fatalf("missing %s in %v", name, tools)
	}
	return tools[i]
}

func TestHTTPPaginationSchemaAndFrozenCatalog(t *testing.T) {
	var calls atomic.Int32
	s := sdkServer(&sdk.ServerOptions{PageSize: 1, Instructions: "Use the fixture tools."})
	fn := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "okay"}}}, nil
	}
	schema := `{"type":"object","$defs":{"value":{"type":"integer","minimum":2}},"properties":{"count":{"$ref":"#/$defs/value"},"mode":{"enum":["yes"]}},"required":["count","mode"],"additionalProperties":false}`
	for _, name := range []string{"first", "second", "third"} {
		addTool(s, name, schema, fn)
	}
	m, _ := httpManager(t, s, config.MCPServer{})
	if got := m.Catalog(); len(got) != 0 || m.entries[0].session != nil {
		t.Fatal("Catalog connected lazily configured server")
	}
	tools := toolsFor(t, m)
	if len(tools) != 3 {
		t.Fatalf("paginated tools=%d", len(tools))
	}
	first := find(t, tools, "mcp__fixture__first")
	source, description := first.(interface{ SourceInfo() (string, string) }).SourceInfo()
	if source != "fixture" || description != "Use the fixture tools." {
		t.Fatalf("source=%q description=%q", source, description)
	}
	params := first.Parameters()
	params["type"] = "string"
	if first.Parameters()["type"] != "object" {
		t.Fatal("caller changed frozen parameters")
	}
	bad := []map[string]any{
		{"count": 1, "mode": "yes"}, {"count": 2, "mode": "no"}, {"count": 2, "mode": "yes", "extra": true},
	}
	for _, args := range bad {
		if err := first.(tool.Validator).Validate(args); err == nil {
			t.Fatalf("invalid schema arguments accepted: %v", args)
		}
		if res := first.Execute(t.Context(), args); !res.IsError {
			t.Fatal("direct invalid execution was allowed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid arguments reached remote server")
	}
	if res := first.Execute(t.Context(), map[string]any{"count": 2, "mode": "yes"}); res.IsError || res.Content[0].Text != "okay" {
		t.Fatalf("execute: %+v", res)
	}
	s.AddTool(&sdk.Tool{Name: "first", Description: "changed", InputSchema: json.RawMessage(`{"type":"object"}`)}, fn)
	// Wait for observable catalog notification, not a fixed delay. Calling the
	// iterator remains cheap after each cached acquisition.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		current, err := m.Tools(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if find(t, current, "mcp__fixture__first").Description() == "changed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("notification did not refresh catalog")
		case <-tick.C:
		}
	}
	if first.Description() != "Description first" || first.Parameters()["required"] == nil {
		t.Fatal("refresh mutated old occupy snapshot")
	}
}

func TestCallCancellationAndShutdownJoin(t *testing.T) {
	started := make(chan struct{}, 2)
	canceled := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	s := sdkServer(nil)
	addTool(s, "block", `{"type":"object"}`, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			canceled <- struct{}{}
			return nil, ctx.Err()
		case <-release:
			return &sdk.CallToolResult{}, nil
		}
	})
	addTool(s, "ok", `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "alive"}}}, nil
	})
	m, _ := httpManager(t, s, config.MCPServer{})
	tools := toolsFor(t, m)
	block := find(t, tools, "mcp__fixture__block")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan tool.Result, 1)
	go func() { done <- block.Execute(ctx, nil) }()
	<-started
	cancel()
	if result := <-done; !result.IsError {
		t.Fatal("canceled tool succeeded")
	}
	<-canceled
	if res := find(t, tools, "mcp__fixture__ok").Execute(t.Context(), nil); res.IsError || res.Content[0].Text != "alive" {
		t.Fatalf("canceled call closed shared session: %+v", res)
	}
	go func() { done <- block.Execute(t.Context(), nil) }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	if result := <-done; !result.IsError {
		t.Fatal("closed manager did not cancel owned call")
	}
	// Local callbacks must retire before closure can finish. SDK cancellation
	// delivery is best effort; release the remote fixture explicitly rather
	// than claiming the host can force remote code to stop.
	releaseOnce.Do(func() { close(release) })
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Tools(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Tools error=%v", err)
	}
}

func TestResultPreservesContentAndStructuredData(t *testing.T) {
	s := sdkServer(nil)
	addTool(s, "content", `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			IsError: true,
			Content: []sdk.Content{
				&sdk.TextContent{Text: "error text", Meta: sdk.Meta{"extra": "kept"}},
				&sdk.ImageContent{Data: []byte{1, 2}, MIMEType: "image/png"},
				&sdk.AudioContent{Data: []byte{3, 4}, MIMEType: "audio/wav"},
				&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///unread", Text: "embedded"}},
				&sdk.ResourceLink{URI: "https://example.invalid/unread", Name: "link"},
			},
			StructuredContent: map[string]any{"answer": 42},
		}, nil
	})
	m, _ := httpManager(t, s, config.MCPServer{})
	res := toolsFor(t, m)[0].Execute(t.Context(), nil)
	if !res.IsError || len(res.Content) != 5 || res.Content[1].Type != "image" || res.Content[2].Type != "audio" {
		t.Fatalf("result=%+v", res)
	}
	details := res.Details.(map[string]any)
	if details["structuredContent"].(map[string]any)["answer"] != float64(42) ||
		details["content"] != nil || len(details["contentMetadata"].([]any)) != 5 || details["contentMetadata"].([]any)[0].(map[string]any)["_meta"] == nil {
		t.Fatalf("details=%+v", details)
	}
}

func TestRawFiltersAndFailures(t *testing.T) {
	s := sdkServer(nil)
	for _, name := range []string{"a", "b", "raw-name"} {
		addTool(s, name, `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
	}
	m, hs := httpManager(t, s, config.MCPServer{EnabledTools: []string{"a", "raw-name"}, DisabledTools: []string{"a"}})
	got := toolsFor(t, m)
	if len(got) != 1 || got[0].(*adapter).raw != "raw-name" {
		t.Fatalf("filtered=%v", got)
	}
	disabled := false
	other := NewManager(map[string]config.MCPServer{
		"good":     {URL: hs.URL},
		"optional": {Command: "missing-ki-mcp-fixture-executable"},
		"disabled": {Command: "missing-ki-mcp-fixture-executable", Enabled: &disabled, Required: true},
	})
	defer other.Close()
	partial, err := other.Tools(t.Context())
	if len(partial) != 3 || err == nil || errors.Is(err, ErrRequired) {
		t.Fatalf("optional tools=%d err=%v", len(partial), err)
	}
	required := NewManager(map[string]config.MCPServer{"missing": {Command: "missing-ki-mcp-fixture-executable", Required: true}})
	defer required.Close()
	if _, err := required.Tools(t.Context()); !errors.Is(err, ErrRequired) {
		t.Fatalf("required err=%v", err)
	}
}

func TestNamesIncludeAliasCollisionSafety(t *testing.T) {
	raws := []struct{ server, raw string }{
		{"a", "b_c"}, {"a_b", "c"}, {"a", "raw-name"}, {"a", "raw_name"},
		{"a", strings.Repeat("long", 60)}, {"a", "☃"},
	}
	var adapters []*adapter
	for _, raw := range raws {
		adapters = append(adapters, &adapter{server: raw.server, raw: raw.raw, name: modelName(raw.server, raw.raw)})
	}
	tools := normalizeCatalogNames(adapters)
	if _, err := tool.NewRegistry(tools); err != nil {
		t.Fatal(err)
	}
	for _, t := range tools {
		if len(t.Name()) > 128 {
			panic("name budget exceeded")
		}
	}
}

func TestHeadersAndNoRedirectCredentials(t *testing.T) {
	t.Setenv("KI_MCP_TOKEN", "secret")
	s := sdkServer(nil)
	addTool(s, "ok", `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	var invalid atomic.Bool
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, nil)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Key") != "value" {
			invalid.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	defer hs.Close()
	m := NewManager(map[string]config.MCPServer{"http": {URL: hs.URL, BearerTokenEnv: "KI_MCP_TOKEN", HTTPHeaders: map[string]string{"X-Key": "value"}}})
	toolsFor(t, m)
	_ = m.Close()
	if invalid.Load() {
		t.Fatal("configured headers missing")
	}
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirected := NewManager(map[string]config.MCPServer{"http": {URL: redirect.URL, BearerTokenEnv: "KI_MCP_TOKEN"}})
	defer redirected.Close()
	if _, err := redirected.Tools(t.Context()); err == nil {
		t.Fatal("redirect connected")
	}
	if leaked.Load() {
		t.Fatal("MCP followed unconfigured redirect")
	}
}

func TestSDKStdioHelper(t *testing.T) {
	if os.Getenv("KI_MCP_STDIO_FIXTURE") != "1" {
		return
	}
	s := sdkServer(nil)
	addTool(s, "environment", `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("%s/%s/%s", os.Getenv("MCP_EXPLICIT"), os.Getenv("MCP_ALLOW"), os.Getenv("MCP_SECRET"))}}}, nil
	})
	if err := s.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestSDKStdioRoundTripAndConcurrentClose(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_ALLOW", "included")
	t.Setenv("MCP_SECRET", "excluded")
	m := NewManager(map[string]config.MCPServer{"stdio": {
		Command: executable, Args: []string{"-test.run=^TestSDKStdioHelper$"},
		// The race runtime's default post-exit sleep is not MCP shutdown work
		// and would trigger the real transport's process termination deadline.
		Env: map[string]string{"KI_MCP_STDIO_FIXTURE": "1", "MCP_EXPLICIT": "set", "GORACE": "atexit_sleep_ms=0"}, EnvVars: []string{"MCP_ALLOW"},
	}})
	tools := toolsFor(t, m)
	if res := tools[0].Execute(t.Context(), nil); res.IsError || res.Content[0].Text != "set/included/" {
		t.Fatalf("stdio result=%+v", res)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := m.Close(); err != nil {
				t.Errorf("close=%v", err)
			}
		})
	}
	wg.Wait()
	if m.entries[0].sessionDone == nil {
		t.Fatal("no worker monitor")
	}
	select {
	case <-m.entries[0].sessionDone:
	default:
		t.Fatal("Close did not join SDK session monitor")
	}
}

func TestIndependentServersConnectInParallel(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	configs := make(map[string]config.MCPServer)
	for _, name := range []string{"one", "two"} {
		s := sdkServer(nil)
		addTool(s, "ok", `{"type":"object"}`, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
		handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, nil)
		var first sync.Once
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				first.Do(func() { entered <- struct{}{} })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(hs.Close)
		configs[name] = config.MCPServer{URL: hs.URL}
	}
	m := NewManager(configs)
	t.Cleanup(func() { _ = m.Close() })
	done := make(chan error, 1)
	go func() {
		tools, err := m.Tools(t.Context())
		if err == nil && len(tools) != 2 {
			err = fmt.Errorf("tools=%d", len(tools))
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("second server could not connect while first was gated")
		}
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
