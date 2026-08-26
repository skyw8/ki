package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/session"
)

func TestLoadGlobalOnlyAndToggle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".mcp.json"), []byte(`{"mcpServers":{"github":{"command":"npx"},"old":{"command":"true"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".ki"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".ki", ".mcp.json"), []byte(`{"mcpServers":{"github":{"command":"echo","args":["hi"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Load(home, cwd)
	if f.MCPServers["github"].Command != "npx" || f.Sources["github"] != "home" {
		t.Fatalf("global config = %+v", f)
	}
	if _, ok := f.MCPServers["project-only"]; ok {
		t.Fatal("project MCP config should be ignored")
	}
	if got := FilterNames(f, session.Toggle{Only: []string{"github"}}); len(got) != 1 || got[0] != "github" {
		t.Fatalf("filtered names = %v", got)
	}
}

func TestApplyExtensionToolNames(t *testing.T) {
	got := applyExtensionToolNames([]ToolDefinition{{Name: "now"}}, "extension:time")
	if len(got) != 1 || got[0].Name != "time/now" || got[0].WireName != "now" {
		t.Fatalf("%+v", got)
	}
	plain := applyExtensionToolNames([]ToolDefinition{{Name: "now"}}, "home")
	if plain[0].Name != "now" || plain[0].WireName != "" {
		t.Fatalf("user mcp should stay bare: %+v", plain)
	}
}

func TestValidateServerSpecIsExplicit(t *testing.T) {
	valid := []ServerSpec{{Command: "node"}, {URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "x"}}}
	for _, spec := range valid {
		if err := ValidateServerSpec(spec); err != nil {
			t.Fatalf("valid spec %+v: %v", spec, err)
		}
	}
	invalid := []ServerSpec{{}, {Command: "node", URL: "https://example.com"}, {URL: "https://example.com", Args: []string{"x"}}, {Command: "node", Headers: map[string]string{"X": "y"}}}
	for _, spec := range invalid {
		if err := ValidateServerSpec(spec); err == nil {
			t.Fatalf("invalid spec accepted: %+v", spec)
		}
	}
}

func TestManagerPrepareAndCallUsesSDK(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test-server", Version: "1"}, &sdk.ServerOptions{PageSize: 1})
	type input struct {
		Text string `json:"text"`
	}
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "echo input"}, func(_ context.Context, _ *sdk.CallToolRequest, in input) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
	})
	sdk.AddTool(server, &sdk.Tool{Name: "second"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "missing header", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	manager := NewManager(nil)
	defer manager.Close()
	file := File{MCPServers: map[string]ServerSpec{"demo": {URL: httpServer.URL, Headers: map[string]string{"X-Test": "yes"}}}}
	prepared := manager.Prepare(t.Context(), "session-a", file, session.Toggle{}, nil)
	state := prepared.States["demo"]
	if state.Status != StatusReady || len(state.Tools) != 2 || len(prepared.Tools) != 2 {
		t.Fatalf("prepare = %+v, tools=%d", state, len(prepared.Tools))
	}
	var echoTool = prepared.Tools[0]
	for _, tool := range prepared.Tools {
		if tool.Name() == "echo" {
			echoTool = tool
		}
	}
	result := echoTool.Execute(t.Context(), map[string]any{"text": "pong"})
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "pong" {
		t.Fatalf("call result = %+v", result)
	}

	manager.Prepare(t.Context(), "session-b", file, session.Toggle{}, nil)
	manager.mu.Lock()
	gotSessions := len(manager.by)
	manager.mu.Unlock()
	if gotSessions != 2 {
		t.Fatalf("SDK sessions shared across ki sessions: %d", gotSessions)
	}
}

func TestPrepareFailureDoesNotHideSuccess(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "ok", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "ok"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer httpServer.Close()
	manager := NewManager(nil)
	defer manager.Close()
	file := File{MCPServers: map[string]ServerSpec{"ok": {URL: httpServer.URL}, "bad": {}}}
	prepared := manager.Prepare(t.Context(), "session", file, session.Toggle{}, nil)
	if prepared.States["ok"].Status != StatusReady || prepared.States["bad"].Status != StatusFailed || len(prepared.Tools) != 1 {
		t.Fatalf("partial prepare = %+v tools=%d", prepared.States, len(prepared.Tools))
	}
}

func TestToolListChangedNotifiesWithoutRefreshing(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "dynamic", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "one"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	defer httpServer.Close()
	notifications := make(chan Notification, 1)
	manager := NewManager(func(_ string, notification Notification) { notifications <- notification })
	defer manager.Close()
	file := File{MCPServers: map[string]ServerSpec{"dynamic": {URL: httpServer.URL}}}
	prepared := manager.Prepare(t.Context(), "session", file, session.Toggle{}, nil)
	if len(prepared.States["dynamic"].Tools) != 1 {
		t.Fatalf("initial tools = %+v", prepared.States)
	}
	sdk.AddTool(server, &sdk.Tool{Name: "two"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	select {
	case notification := <-notifications:
		if notification.Kind != "tools_changed" || notification.Server != "dynamic" {
			t.Fatalf("notification = %+v", notification)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tools/list_changed was not delivered")
	}
	// Manager only reports the change. The resources layer keeps this exact
	// catalog until explicit reload.
	if len(prepared.States["dynamic"].Tools) != 1 {
		t.Fatal("notification mutated the pinned catalog")
	}
}
