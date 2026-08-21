package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"ki/internal/session"
)

func TestLoadProjectOverridesGlobalAndToggle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, ".mcp.json"), []byte(`{
	  "mcpServers": {
	    "github": {"command": "npx", "args": ["-y", "gh"]},
	    "old": {"command": "true"}
	  }
	}`), 0o600)
	_ = os.MkdirAll(filepath.Join(cwd, ".ki"), 0o700)
	_ = os.WriteFile(filepath.Join(cwd, ".ki", ".mcp.json"), []byte(`{
	  "mcpServers": {
	    "github": {"command": "echo", "args": ["hi"]}
	  }
	}`), 0o600)
	f := Load(home, cwd)
	if f.MCPServers["github"].Command != "echo" {
		t.Fatalf("project should win: %+v", f.MCPServers["github"])
	}
	if _, ok := f.MCPServers["old"]; !ok {
		t.Fatal("global-only server missing")
	}
	names := FilterNames(f, session.Toggle{Disabled: []string{"old"}})
	for _, n := range names {
		if n == "old" {
			t.Fatal("disabled still present")
		}
	}
	only := FilterNames(f, session.Toggle{Only: []string{"github"}})
	if len(only) != 1 || only[0] != "github" {
		t.Fatalf("only: %v", only)
	}

	listed := List(f, session.Toggle{Disabled: []string{"old"}})
	if len(listed) != 2 {
		t.Fatalf("list len: %+v", listed)
	}
	byName := map[string]ServerInfo{}
	for _, s := range listed {
		byName[s.Name] = s
	}
	if !byName["github"].Enabled || byName["old"].Enabled {
		t.Fatalf("enabled: %+v", listed)
	}
	if byName["github"].Source != "project" || byName["old"].Source != "home" {
		t.Fatalf("source: %+v", listed)
	}
	if byName["github"].Command != "echo" {
		t.Fatalf("command: %+v", byName["github"])
	}
}

func TestCachedPinsUntilInvalidate(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"true"}}}`), 0o600)
	f := Cached(home, cwd, "s1")
	if _, ok := f.MCPServers["a"]; !ok {
		t.Fatal("missing a")
	}
	_ = os.WriteFile(filepath.Join(home, ".mcp.json"), []byte(`{"mcpServers":{"b":{"command":"true"}}}`), 0o600)
	f2 := Cached(home, cwd, "s1")
	if _, ok := f2.MCPServers["b"]; ok {
		t.Fatal("cache should hide b")
	}
	if Cached(home, cwd, "s2").MCPServers["b"].Command != "true" {
		t.Fatal("new session should re-read")
	}
	InvalidateAll()
	if Cached(home, cwd, "s1").MCPServers["b"].Command != "true" {
		t.Fatal("after invalidate")
	}
}

func TestHTTPURLFromMcpRemote(t *testing.T) {
	u := httpURL(ServerSpec{Command: "npx", Args: []string{"-y", "mcp-remote", "https://mcp.exa.ai/mcp"}})
	if u != "https://mcp.exa.ai/mcp" {
		t.Fatalf("url: %q", u)
	}
	if httpURL(ServerSpec{Command: "true"}) != "" {
		t.Fatal("stdio should have empty url")
	}
}

func TestPoolBindDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	cmd, args := markerCmd(t, marker)
	home := t.TempDir()
	p := NewPool(home)
	defer p.Close()
	file := File{MCPServers: map[string]ServerSpec{
		"slow": {Command: cmd, Args: args},
	}}
	if tools := p.Bind(file, session.Toggle{}); len(tools) != 0 {
		t.Fatalf("cold bind should have no schemas: %d", len(tools))
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("bind spawned")
	}
}

func TestPoolHTTPReuseAndToggle(t *testing.T) {
	var inits atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			inits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "echo", "description": "e", "inputSchema": map[string]any{"type": "object"}}}},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}},
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer hs.Close()

	home := t.TempDir()
	p := NewPool(home)
	defer p.Close()
	file := File{MCPServers: map[string]ServerSpec{"exa": {URL: hs.URL}}}

	if n := len(p.Bind(file, session.Toggle{Disabled: []string{"exa"}})); n != 0 {
		t.Fatalf("disabled bind: %d", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Prefetch(ctx, file, session.Toggle{Disabled: []string{"exa"}})
	if inits.Load() != 0 {
		t.Fatalf("disabled prefetch spawned: %d", inits.Load())
	}

	p.Prefetch(ctx, file, session.Toggle{})
	tools := p.Bind(file, session.Toggle{})
	if len(tools) != 1 || tools[0].Name() != "echo" {
		t.Fatalf("bind after prefetch: %+v", tools)
	}
	res := tools[0].Execute(ctx, map[string]any{})
	if res.IsError || len(res.Content) == 0 || res.Content[0].Text != "pong" {
		t.Fatalf("exec: %+v", res)
	}
	_ = tools[0].Execute(ctx, map[string]any{})
	if inits.Load() != 1 {
		t.Fatalf("expected one handshake, got %d", inits.Load())
	}

	p2 := NewPool(home)
	defer p2.Close()
	cached := p2.Bind(file, session.Toggle{})
	if len(cached) != 1 || cached[0].Name() != "echo" {
		t.Fatalf("disk cache: %+v", cached)
	}
}

func markerCmd(t *testing.T, marker string) (string, []string) {
	t.Helper()
	if os.PathSeparator == '\\' {
		bat := filepath.Join(t.TempDir(), "mark.bat")
		if err := os.WriteFile(bat, []byte("@echo spawned > \""+marker+"\"\r\nping -n 20 127.0.0.1 >nul\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return bat, nil
	}
	sh := filepath.Join(t.TempDir(), "mark.sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\nprintf spawned > \"$1\"\nsleep 20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "sh", []string{sh, marker}
}
