package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Sessions.Root = filepath.Join(home, "sessions")
	cfg.Providers = map[string]config.Provider{"anthropic": {APIKey: "x"}}
	cfg.Defaults = config.Defaults{Provider: "anthropic", Model: "claude-sonnet-4-5"}
	srv := New(Options{
		Config:   cfg,
		Token:    "tok",
		Streamer: &provider.Scripted{Steps: []types.Message{{Content: []types.Content{{Type: "text", Text: "hi there"}}}}},
	})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, hs
}

func TestAuthAndCreateGetFork(t *testing.T) {
	_, hs := testServer(t)
	res, err := http.Post(hs.URL+"/v1/sessions", "application/json", strings.NewReader(`{"cwd":"`+t.TempDir()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/ffffffffffffffffffffffffffffffff/prompt", strings.NewReader(`{"text":"x"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session prompt: %d", res.StatusCode)
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if got["id"] != id || got["model"] == "" {
		t.Fatalf("get: %+v", got)
	}
	dir, _ := got["dir"].(string)
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	hdr := firstLine(t, filepath.Join(dir, "events.jsonl"))
	if hdr["type"] != "session" {
		t.Fatalf("header: %v", hdr)
	}

	id2 := createSession(t, hs, t.TempDir())
	if id2 == id {
		t.Fatal("second create must differ")
	}

	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/fork", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var fork map[string]any
	_ = json.NewDecoder(res.Body).Decode(&fork)
	res.Body.Close()
	if fork["id"] == id || fork["parentSession"] == nil {
		t.Fatalf("fork: %+v", fork)
	}
}

func TestPromptSSEAndPersist(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("prompt status %d", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var types []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev loop.Event
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
		types = append(types, string(ev.Type))
		if ev.Type == loop.AgentEnd {
			break
		}
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "agent_start") || !strings.Contains(joined, "agent_end") {
		t.Fatalf("events: %s", joined)
	}
	if !strings.Contains(joined, "message_start") {
		t.Fatalf("missing message: %s", joined)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	res.Body.Close()
	dir, _ := meta["dir"].(string)
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"role":"user"`) || !strings.Contains(string(raw), `"role":"assistant"`) {
		t.Fatalf("jsonl missing messages:\n%s", raw)
	}

	// concurrent prompt 409
	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"again"}`))
	req.Header.Set("Authorization", "Bearer tok")
	// after done, should be 202 not 409
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode == 409 {
		t.Fatal("run should have finished")
	}
	res2.Body.Close()
}

func TestAbortAndCompact(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/abort", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("abort %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/compact", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("compact %d %s", res.StatusCode, b)
	}
}

func TestPromptModelWriteback(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hi","model":"openai/gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if got["provider"] != "openai" || got["model"] != "gpt-4o" {
		t.Fatalf("writeback: %+v", got)
	}
}

func TestListHistoryAndUI(t *testing.T) {
	_, hs := testServer(t)
	cwd := t.TempDir()
	id := createSession(t, hs, cwd)
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello ui"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&listed)
	res.Body.Close()
	if len(listed) != 1 || listed[0]["id"] != id || listed[0]["title"] != "hello ui" {
		t.Fatalf("list: %+v", listed)
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	msgs, _ := got["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("history messages: %+v", got["messages"])
	}

	res, err = http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("ui %d", res.StatusCode)
	}
	if !strings.Contains(string(body), `"token":"tok"`) {
		t.Fatalf("index missing injected token:\n%s", body)
	}

	res, err = http.Get(hs.URL + "/home/someone/proj")
	if err != nil {
		t.Fatal(err)
	}
	hostBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(hostBody), `"token":"tok"`) {
		t.Fatalf("host path should still serve SPA: %d", res.StatusCode)
	}
}

func TestRequestHeaderPersistAndPatch(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"snap"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sawHeader bool
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev loop.Event
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
		if ev.Type == loop.RequestHeader && ev.System != "" {
			sawHeader = true
		}
		if ev.Type == loop.AgentEnd {
			break
		}
	}
	res.Body.Close()
	if !sawHeader {
		t.Fatal("SSE missing request_header with system")
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	ents, _ := got["entries"].([]any)
	var found bool
	for _, raw := range ents {
		m, _ := raw.(map[string]any)
		if m["type"] == "request_header" {
			sys, _ := m["system"].(string)
			if sys == "" {
				t.Fatalf("empty system in entry: %+v", m)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("entries missing request_header: %+v", ents)
	}

	body, _ := json.Marshal(map[string]any{
		"model":  "openai/gpt-4o",
		"skills": map[string]any{"disabled": []string{"x"}},
		"mcp":    map[string]any{"only": []string{"y"}},
	})
	req, _ = http.NewRequest("PATCH", hs.URL+"/v1/sessions/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var patched map[string]any
	_ = json.NewDecoder(res.Body).Decode(&patched)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("patch %d %+v", res.StatusCode, patched)
	}
	if patched["model"] != "gpt-4o" {
		t.Fatalf("model: %+v", patched)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var again map[string]any
	_ = json.NewDecoder(res.Body).Decode(&again)
	res.Body.Close()
	skills, _ := again["skills"].(map[string]any)
	dis, _ := skills["disabled"].([]any)
	if len(dis) != 1 || dis[0] != "x" {
		t.Fatalf("skills reload: %+v", again["skills"])
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var models []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&models)
	res.Body.Close()
	if len(models) == 0 || models[0]["spec"] == "" {
		t.Fatalf("models: %+v", models)
	}
}

func TestPromptNotBlockedBySlowMCP(t *testing.T) {
	srv, hs := testServer(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	cmd, args := markerCmd(t, marker)
	body, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"slow": map[string]any{"command": cmd, "args": args},
		},
	})
	if err := os.WriteFile(filepath.Join(srv.cfg.Home, ".mcp.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("prompt %d", res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, "GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events blocked on MCP: %v", err)
	}
	defer res.Body.Close()
	var types []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev loop.Event
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
		types = append(types, string(ev.Type))
		if ev.Type == loop.AgentEnd {
			break
		}
	}
	if !strings.Contains(strings.Join(types, ","), "agent_start") {
		t.Fatalf("no agent_start: %v", types)
	}
}

func TestSessionCatalogNoSpawn(t *testing.T) {
	srv, hs := testServer(t)
	home := srv.cfg.Home
	cwd := t.TempDir()
	skillDir := filepath.Join(home, "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\ndescription: a skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(cwd, ".ki", "skills", "beta")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "SKILL.md"), []byte("---\nname: beta\ndescription: project skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "spawned")
	cmd, args := markerCmd(t, marker)
	mcpBody, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"exa":       map[string]any{"command": cmd, "args": args},
			"context7":  map[string]any{"command": "true"},
			"keep-open": map[string]any{"command": "true"},
		},
	})
	if err := os.WriteFile(filepath.Join(home, ".mcp.json"), mcpBody, 0o644); err != nil {
		t.Fatal(err)
	}

	id := createSession(t, hs, cwd)
	body, _ := json.Marshal(map[string]any{
		"skills": map[string]any{"disabled": []string{"alpha"}},
		"mcp":    map[string]any{"disabled": []string{"exa"}},
	})
	req, _ := http.NewRequest("PATCH", hs.URL+"/v1/sessions/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("patch %d", res.StatusCode)
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("GET spawned MCP command")
	}

	skills, _ := got["availableSkills"].([]any)
	sk := map[string]map[string]any{}
	for _, raw := range skills {
		m, _ := raw.(map[string]any)
		name, _ := m["name"].(string)
		sk[name] = m
	}
	if sk["alpha"]["enabled"] != false || sk["alpha"]["source"] != "home" {
		t.Fatalf("alpha: %+v", sk["alpha"])
	}
	if sk["beta"]["enabled"] != true || sk["beta"]["source"] != "project" {
		t.Fatalf("beta: %+v", sk["beta"])
	}

	mcps, _ := got["availableMcp"].([]any)
	if len(mcps) < 3 {
		t.Fatalf("mcp: %+v", got["availableMcp"])
	}
	mc := map[string]map[string]any{}
	for _, raw := range mcps {
		m, _ := raw.(map[string]any)
		name, _ := m["name"].(string)
		mc[name] = m
	}
	if mc["exa"]["enabled"] != false || mc["context7"]["enabled"] != true {
		t.Fatalf("mcp enabled: %+v", mc)
	}
	if mc["exa"]["source"] != "home" || mc["context7"]["command"] != "true" {
		t.Fatalf("mcp meta: %+v", mc)
	}
}

func markerCmd(t *testing.T, marker string) (string, []string) {
	t.Helper()
	if os.PathSeparator == '\\' {
		bat := filepath.Join(t.TempDir(), "mark.bat")
		if err := os.WriteFile(bat, []byte("@echo spawned > \""+marker+"\"\r\nping -n 20 127.0.0.1 >nul\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return bat, nil
	}
	sh := filepath.Join(t.TempDir(), "mark.sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\nprintf spawned > \"$1\"\nsleep 20\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sh, []string{marker}
}

func createSession(t *testing.T, hs *httptest.Server, cwd string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"cwd": cwd})
	req, _ := http.NewRequest("POST", hs.URL+"/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("create %d %s", res.StatusCode, b)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("no id: %s", b)
	}
	return id
}

func TestWorkspacesFSSearch(t *testing.T) {
	srv, hs := testServer(t)
	proj := t.TempDir()
	id := createSession(t, hs, proj)

	req, _ := http.NewRequest("GET", hs.URL+"/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	res.Body.Close()
	if _, ok := meta["cwd"]; ok {
		t.Fatalf("meta still has cwd: %+v", meta)
	}
	if meta["home"] == nil {
		t.Fatal("meta home")
	}

	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tmpSess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&tmpSess)
	res.Body.Close()
	cwd, _ := tmpSess["cwd"].(string)
	if !strings.Contains(cwd, filepath.Join("workspace", "tmp+")) {
		t.Fatalf("temp cwd %q", cwd)
	}
	if tmpSess["workspaceId"] == "" {
		t.Fatal("temp workspaceId")
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var spaces []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&spaces)
	res.Body.Close()
	if len(spaces) < 2 {
		t.Fatalf("workspaces: %+v", spaces)
	}

	other := filepath.Join(t.TempDir(), "fresh")
	body, _ := json.Marshal(map[string]any{"path": other, "title": "Fresh"})
	req, _ = http.NewRequest("POST", hs.URL+"/v1/workspaces", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		t.Fatalf("create ws %d", res.StatusCode)
	}
	var ws map[string]any
	_ = json.NewDecoder(res.Body).Decode(&ws)
	res.Body.Close()
	wsID, _ := ws["id"].(string)

	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions", bytes.NewReader([]byte(`{"workspaceId":"`+wsID+`"}`)))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var inWS map[string]any
	_ = json.NewDecoder(res.Body).Decode(&inWS)
	res.Body.Close()
	if inWS["cwd"] != other && !strings.HasPrefix(fmt.Sprint(inWS["cwd"]), other) {
		// Normalize may EvalSymlinks
		if inWS["workspaceId"] != wsID {
			t.Fatalf("create in ws: %+v", inWS)
		}
	}

	patch, _ := json.Marshal(map[string]any{"title": "Renamed", "pinned": true})
	req, _ = http.NewRequest("PATCH", hs.URL+"/v1/sessions/"+id, bytes.NewReader(patch))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var patched map[string]any
	_ = json.NewDecoder(res.Body).Decode(&patched)
	res.Body.Close()
	if patched["title"] != "Renamed" || patched["pinned"] != true {
		t.Fatalf("patch: %+v", patched)
	}

	req, _ = http.NewRequest("GET", hs.URL+"/v1/fs?path="+proj, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listing map[string]any
	_ = json.NewDecoder(res.Body).Decode(&listing)
	res.Body.Close()
	if listing["separator"] == nil || listing["home"] == nil {
		t.Fatalf("listing: %+v", listing)
	}
	crumbs, _ := listing["crumbs"].([]any)
	if len(crumbs) == 0 {
		t.Fatal("crumbs")
	}

	child := map[string]any{"path": proj, "name": "nested"}
	b, _ := json.Marshal(child)
	req, _ = http.NewRequest("POST", hs.URL+"/v1/fs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("mkdir %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ = http.NewRequest("POST", hs.URL+"/v1/fs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 409 {
		t.Fatalf("dup mkdir %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ = http.NewRequest("POST", hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"searchable unique token xyzzy"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	req, _ = http.NewRequest("GET", hs.URL+"/v1/sessions/search?q=xyzzy", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	_ = json.NewDecoder(res.Body).Decode(&found)
	res.Body.Close()
	items, _ := found["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("search: %+v", found)
	}

	req, _ = http.NewRequest("DELETE", hs.URL+"/v1/sessions/"+fmt.Sprint(inWS["id"]), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("del session %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ = http.NewRequest("DELETE", hs.URL+"/v1/workspaces/"+wsID, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 204 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("del ws %d %s", res.StatusCode, b)
	}
	res.Body.Close()
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("workspace dir must remain: %v", err)
	}
	_ = srv
}

func firstLine(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
