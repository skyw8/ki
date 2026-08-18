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
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

func marshalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	return testServerWith(t, &provider.Scripted{Steps: []types.Message{{Content: []types.Content{{Type: "text", Text: "hi there"}}}}})
}

func testServerWith(t *testing.T, st loop.Streamer) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Sessions.Root = filepath.Join(home, "sessions")
	srv, err := New(Options{
		Config:   cfg,
		Token:    "tok",
		Streamer: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, hs
}

// waitAgentEnd reads the events SSE until agent_end (the prompt turn finished
// and the session is fully persisted).
func waitAgentEnd(t *testing.T, hs *httptest.Server, id string) []string {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
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
	// Scan returns false for both clean EOF and read errors, so inspect Err
	// after the loop to avoid treating a truncated event stream as complete.
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return types
}

// reqRecorder captures every Stream request so tests can assert what the
// server passes through (provider/model on the compaction summarizer).
type reqRecorder struct {
	reqs []loop.Request
}

func (r *reqRecorder) Stream(_ context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	r.reqs = append(r.reqs, req)
	return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "summary"}}, StopReason: "stop", Usage: &types.Usage{TotalTokens: 3}}, nil
}

func TestSummarizerCarriesSessionProviderModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Sessions.Root = filepath.Join(home, "sessions")
	cfg.Compaction.KeepRecentTokens = 1 // tiny budget: any session compacts
	rec := &reqRecorder{}
	srv, err := New(Options{Config: cfg, Token: "tok", Streamer: rec})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	id := createSession(t, hs, t.TempDir())
	// Prompt is async (goroutine); wait for agent_end via the events stream so
	// the assistant turn is persisted before compacting.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res0, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res0.Body.Close()
	waitAgentEnd(t, hs, id)
	// Manual compact drives compactSession, whose summarizer must call the
	// streamer with the session's provider/model — otherwise liveFromConfig
	// resolves an empty base URL and the summarization request fails.
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/compact", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("compact %d %s", res.StatusCode, b)
	}
	if len(rec.reqs) < 2 {
		t.Fatalf("streamer calls: %d, want prompt + summarizer", len(rec.reqs))
	}
	last := rec.reqs[len(rec.reqs)-1]
	if last.Provider != "anthropic" || last.Model != "claude-sonnet-5" {
		t.Fatalf("summarizer request must carry session provider/model, got %q/%q", last.Provider, last.Model)
	}
}

func TestAuthAndCreateGetFork(t *testing.T) {
	_, hs := testServer(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", strings.NewReader(`{"cwd":"`+t.TempDir()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	id := createSession(t, hs, t.TempDir())
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/ffffffffffffffffffffffffffffffff/prompt", strings.NewReader(`{"text":"x"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session prompt: %d", res.StatusCode)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
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

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/fork", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var fork map[string]any
	_ = json.NewDecoder(res.Body).Decode(&fork)
	_ = res.Body.Close()
	if fork["id"] == id || fork["parentSession"] == nil {
		t.Fatalf("fork: %+v", fork)
	}
}

func TestProviderConfigurationAPI(t *testing.T) {
	_, hs := testServer(t)
	call := func(method, path, body string) (*http.Response, map[string]any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var out map[string]any
		if res.StatusCode != http.StatusNoContent {
			_ = json.NewDecoder(res.Body).Decode(&out)
		}
		return res, out
	}

	res, _ := call(http.MethodPost, "/v1/providers", `{"id":"local","name":"Local","api":"completions","baseUrl":"http://127.0.0.1:11434/v1","enabled":true,"models":[{"id":"local/model","contextWindow":8192,"maxTokens":1024,"input":["text"],"reasoning":false,"cost":null}]}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create provider: %d", res.StatusCode)
	}
	res, out := call(http.MethodPut, "/v1/providers/local/credential", `{"apiKey":"secret"}`)
	if res.StatusCode != http.StatusOK || strings.Contains(fmt.Sprint(out), "secret") {
		t.Fatalf("credential response: %d %+v", res.StatusCode, out)
	}
	res, _ = call(http.MethodPut, "/v1/default-model", `{"provider":"local","model":"local/model"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set default: %d", res.StatusCode)
	}
	res, _ = call(http.MethodPatch, "/v1/providers/local", `{"enabled":false}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("disable default: %d", res.StatusCode)
	}
}

func TestPromptSSEAndPersist(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status %d", res.StatusCode)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
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
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "agent_start") || !strings.Contains(joined, "agent_end") {
		t.Fatalf("events: %s", joined)
	}
	if !strings.Contains(joined, "message_start") {
		t.Fatalf("missing message: %s", joined)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	_ = res.Body.Close()
	dir, _ := meta["dir"].(string)
	//nolint:gosec // dir is a session directory created by this test.
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"role":"user"`) || !strings.Contains(string(raw), `"role":"assistant"`) {
		t.Fatalf("jsonl missing messages:\n%s", raw)
	}

	// concurrent prompt 409
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"again"}`))
	req.Header.Set("Authorization", "Bearer tok")
	// after done, should be 202 not 409
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode == http.StatusConflict {
		t.Fatal("run should have finished")
	}
	_ = res2.Body.Close()
}

func TestAbortAndCompact(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/abort", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abort %d", res.StatusCode)
	}
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/compact", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("compact tiny session: %d %s (want 409 nothing to compact)", res.StatusCode, b)
	}
}

func TestPromptModelWriteback(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hi","model":"openai/gpt-5.6-terra"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
	if got["provider"] != "openai" || got["model"] != "gpt-5.6-terra" {
		t.Fatalf("writeback: %+v", got)
	}
}

func TestListHistoryAndUI(t *testing.T) {
	_, hs := testServer(t)
	cwd := t.TempDir()
	id := createSession(t, hs, cwd)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello ui"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&listed)
	_ = res.Body.Close()
	if len(listed) != 1 || listed[0]["id"] != id || listed[0]["title"] != "hello ui" {
		t.Fatalf("list: %+v", listed)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
	msgs, _ := got["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("history messages: %+v", got["messages"])
	}

	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ui %d", res.StatusCode)
	}
	if !strings.Contains(string(body), `"token":"tok"`) {
		t.Fatalf("index missing injected token:\n%s", body)
	}

	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/home/someone/proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	hostBody, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(hostBody), `"token":"tok"`) {
		t.Fatalf("host path should still serve SPA: %d", res.StatusCode)
	}
}

func TestRequestHeaderPersistAndPatch(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"snap"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sawHeader, sawContext bool
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
		if ev.Type == loop.ContextUsage && ev.ContextWindow > 0 {
			sawContext = true
		}
		if ev.Type == loop.AgentEnd {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !sawHeader || !sawContext {
		t.Fatalf("SSE metadata: header=%v context=%v", sawHeader, sawContext)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
	ents, _ := got["entries"].([]any)
	var found bool
	for _, raw := range ents {
		m, _ := raw.(map[string]any)
		if m["type"] == "request_header" {
			sys, _ := m["system"].(string)
			if sys == "" {
				t.Fatalf("empty system in entry: %+v", m)
			}
			if m["provider"] != "anthropic" || m["modelId"] != "claude-sonnet-5" || m["catalogVersion"] != float64(provider.CatalogVersion) || m["pricing"] == nil {
				t.Fatalf("request metadata: %+v", m)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("entries missing request_header: %+v", ents)
	}

	body, _ := marshalJSON(map[string]any{
		"model":  "openai/gpt-5.6-terra",
		"skills": map[string]any{"disabled": []string{"x"}},
		"mcp":    map[string]any{"only": []string{"y"}},
	})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/sessions/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var patched map[string]any
	_ = json.NewDecoder(res.Body).Decode(&patched)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch %d %+v", res.StatusCode, patched)
	}
	if patched["model"] != "gpt-5.6-terra" {
		t.Fatalf("model: %+v", patched)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var again map[string]any
	_ = json.NewDecoder(res.Body).Decode(&again)
	_ = res.Body.Close()
	skills, _ := again["skills"].(map[string]any)
	dis, _ := skills["disabled"].([]any)
	if len(dis) != 1 || dis[0] != "x" {
		t.Fatalf("skills reload: %+v", again["skills"])
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var models []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&models)
	_ = res.Body.Close()
	if len(models) == 0 || models[0]["spec"] == "" {
		t.Fatalf("models: %+v", models)
	}
}

func TestPromptNotBlockedBySlowMCP(t *testing.T) {
	srv, hs := testServer(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	cmd, args := markerCmd(t, marker)
	body, _ := marshalJSON(map[string]any{
		"mcpServers": map[string]any{
			"slow": map[string]any{"command": cmd, "args": args},
		},
	})
	if err := os.WriteFile(filepath.Join(srv.cfg.Home, ".mcp.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt %d", res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events blocked on MCP: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
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
	if err := sc.Err(); err != nil {
		t.Fatal(err)
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
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\ndescription: a skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(cwd, ".ki", "skills", "beta")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "SKILL.md"), []byte("---\nname: beta\ndescription: project skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "spawned")
	cmd, args := markerCmd(t, marker)
	mcpBody, _ := marshalJSON(map[string]any{
		"mcpServers": map[string]any{
			"exa":       map[string]any{"command": cmd, "args": args},
			"context7":  map[string]any{"command": "true"},
			"keep-open": map[string]any{"command": "true"},
		},
	})
	if err := os.WriteFile(filepath.Join(home, ".mcp.json"), mcpBody, 0o600); err != nil {
		t.Fatal(err)
	}

	id := createSession(t, hs, cwd)
	body, _ := marshalJSON(map[string]any{
		"skills": map[string]any{"disabled": []string{"alpha"}},
		"mcp":    map[string]any{"disabled": []string{"exa"}},
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/sessions/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch %d", res.StatusCode)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
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

func authedGet(t *testing.T, hs *httptest.Server, path string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+path, nil)
	req.Header.Set("Authorization", "Bearer tok")
	return http.DefaultClient.Do(req)
}

func createSession(t *testing.T, hs *httptest.Server, cwd string) string {
	t.Helper()
	body, _ := marshalJSON(map[string]any{"cwd": cwd})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
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

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	_ = res.Body.Close()
	if _, ok := meta["cwd"]; ok {
		t.Fatalf("meta still has cwd: %+v", meta)
	}
	if meta["home"] == nil {
		t.Fatal("meta home")
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tmpSess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&tmpSess)
	_ = res.Body.Close()
	cwd, _ := tmpSess["cwd"].(string)
	if !strings.Contains(cwd, filepath.Join("workspace", "tmp+")) {
		t.Fatalf("temp cwd %q", cwd)
	}
	if tmpSess["workspaceId"] == "" {
		t.Fatal("temp workspaceId")
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var spaces []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&spaces)
	_ = res.Body.Close()
	if len(spaces) < 2 {
		t.Fatalf("workspaces: %+v", spaces)
	}

	other := filepath.Join(t.TempDir(), "fresh")
	body, _ := marshalJSON(map[string]any{"path": other, "title": "Fresh"})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/workspaces", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create ws %d", res.StatusCode)
	}
	var ws map[string]any
	_ = json.NewDecoder(res.Body).Decode(&ws)
	_ = res.Body.Close()
	wsID, _ := ws["id"].(string)

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", bytes.NewReader([]byte(`{"workspaceId":"`+wsID+`"}`)))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var inWS map[string]any
	_ = json.NewDecoder(res.Body).Decode(&inWS)
	_ = res.Body.Close()
	if inWS["cwd"] != other && !strings.HasPrefix(fmt.Sprint(inWS["cwd"]), other) {
		// Normalize may EvalSymlinks
		if inWS["workspaceId"] != wsID {
			t.Fatalf("create in ws: %+v", inWS)
		}
	}

	patch, _ := marshalJSON(map[string]any{"title": "Renamed", "pinned": true})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/sessions/"+id, bytes.NewReader(patch))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var patched map[string]any
	_ = json.NewDecoder(res.Body).Decode(&patched)
	_ = res.Body.Close()
	if patched["title"] != "Renamed" || patched["pinned"] != true {
		t.Fatalf("patch: %+v", patched)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?path="+proj, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listing map[string]any
	_ = json.NewDecoder(res.Body).Decode(&listing)
	_ = res.Body.Close()
	if listing["separator"] == nil || listing["home"] == nil {
		t.Fatalf("listing: %+v", listing)
	}
	crumbs, _ := listing["crumbs"].([]any)
	if len(crumbs) == 0 {
		t.Fatal("crumbs")
	}

	child := map[string]any{"path": proj, "name": "nested"}
	b, _ := marshalJSON(child)
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/fs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mkdir %d", res.StatusCode)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/fs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("dup mkdir %d", res.StatusCode)
	}
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"searchable unique token xyzzy"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/search?q=xyzzy", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	_ = json.NewDecoder(res.Body).Decode(&found)
	_ = res.Body.Close()
	items, _ := found["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("search: %+v", found)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodDelete, hs.URL+"/v1/sessions/"+fmt.Sprint(inWS["id"]), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("del session %d", res.StatusCode)
	}
	_ = res.Body.Close()

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodDelete, hs.URL+"/v1/workspaces/"+wsID, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("del ws %d %s", res.StatusCode, b)
	}
	_ = res.Body.Close()
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("workspace dir must remain: %v", err)
	}
	_ = srv
}

// ---------------------------------------------------------------------------
// events（SSE）等待循环专项测试
//
// 目标：确定性走到 events handler 等待循环的三条关键路径——
//  1) Wait 路径：reader 在运行中途连接，缓冲里没有新事件时必须睡在 Cond 上；
//  2) 多读者广播：一个 run 被两个 SSE 读者同时订阅，各自收到完整一致的事件流；
//  3) 客户端断开：reader 被哨兵唤醒退出，不泄漏 goroutine、不阻塞后续运行。
// 配合 `go test -race`：-race 验证锁纪律（数据竞争），这些测试验证行为。
// 注意：测试无法"证明"并发正确性（那要靠不变式 / 模型检查，见
// tmp/tla-demo），它们的作用是回归保护——重构等待循环后行为必须逐字节不变。
// ---------------------------------------------------------------------------

// gateStreamer 在 Stream 内阻塞直到 release()，让测试把一次运行"冻"在
// 流式输出前：此时缓冲里只有 agent_start..request_header 五个事件，之后
// 连接 SSE 的 reader 必然走进 Cond.Wait 的等待路径（闸门不放开，reader
// 不可能有别的状态）。arm() 为下一次运行装上新闸门；release() 放行并把
// 闸门置空，之后的运行直接通过。
type gateStreamer struct {
	inner loop.Streamer
	mu    sync.Mutex
	gate  chan struct{}
}

func (g *gateStreamer) arm() {
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.mu.Unlock()
}

func (g *gateStreamer) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	g.mu.Lock()
	ch := g.gate
	g.mu.Unlock()
	if ch == nil {
		msg, err := g.inner.Stream(ctx, req, emit)
		if err != nil {
			return msg, fmt.Errorf("stream gated provider: %w", err)
		}
		return msg, nil
	}
	select {
	case <-ctx.Done():
		return types.Message{}, ctx.Err()
	case <-ch:
	}
	msg, err := g.inner.Stream(ctx, req, emit)
	if err != nil {
		return msg, fmt.Errorf("stream gated provider: %w", err)
	}
	return msg, nil
}

func (g *gateStreamer) release() {
	g.mu.Lock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
	g.mu.Unlock()
}

// wantSSE 是 gate 版 Scripted 一次完整 prompt 的事件序列（type[:role]）。
func wantSSE() []string {
	return []string{
		"agent_start",
		"turn_start",
		"message_start:user", "message_end:user",
		"request_header",
		"context_usage",
		"message_start:assistant", "message_update", "message_end:assistant",
		"context_usage",
		"turn_end",
		"agent_end",
	}
}

func prompt202(t *testing.T, hs *httptest.Server, id, text string) {
	t.Helper()
	body, _ := marshalJSON(map[string]any{"text": text})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt %d", res.StatusCode)
	}
}

// waitBuffered 轮询直到 runState 的缓冲里至少有 n 个事件（同包直接读内部状态，
// 注意按 srv.mu / st.mu 的顺序加锁）。
func waitBuffered(t *testing.T, srv *Server, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		st := srv.runs[id]
		var got int
		if st != nil {
			st.mu.Lock()
			got = len(st.evs)
			st.mu.Unlock()
		}
		srv.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %d buffered events (got %d)", id, n, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func openEventsRaw(hs *httptest.Server, id string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	return http.DefaultClient.Do(req)
}

func mustOpenEvents(t *testing.T, hs *httptest.Server, id string) *http.Response {
	t.Helper()
	res, err := openEventsRaw(hs, id)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// sseLabel 把一条 data: 行压成 "type[:role]"。
func sseLabel(line string) string {
	var ev loop.Event
	_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
	s := string(ev.Type)
	if ev.Message != nil && (ev.Type == loop.MessageStart || ev.Type == loop.MessageEnd) {
		s += ":" + ev.Message.Role
	}
	return s
}

// scanN 从 scanner 读 n 条 SSE 事件（只数 data: 行）。
func scanN(t *testing.T, sc *bufio.Scanner, n int) []string {
	t.Helper()
	var out []string
	for len(out) < n && sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		out = append(out, sseLabel(line))
	}
	if len(out) != n {
		t.Fatalf("expected %d events, got %d (err=%v)", n, len(out), sc.Err())
	}
	return out
}

// scanAll 读到 agent_end（或流结束）。
func scanAll(t *testing.T, sc *bufio.Scanner) []string {
	t.Helper()
	var out []string
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		s := sseLabel(line)
		out = append(out, s)
		if s == "agent_end" {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// collectSSE 把一条 SSE 响应完整读到 agent_end。
func collectSSE(t *testing.T, res *http.Response) []string {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	var out []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		s := sseLabel(line)
		out = append(out, s)
		if s == "agent_end" {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// readSSE 异步读完整事件流；读完前 n 条后向 ready 发一次信号
// （用于确定两个 reader 都重放完缓冲、进入等待后再放行运行）。
type sseResult struct {
	stream []string
	err    error
}

func readSSE(hs *httptest.Server, id string, n int, ready chan<- struct{}) sseResult {
	res, err := openEventsRaw(hs, id)
	if err != nil {
		return sseResult{err: err}
	}
	defer func() { _ = res.Body.Close() }()
	var out []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		s := sseLabel(line)
		out = append(out, s)
		if ready != nil && len(out) == n {
			ready <- struct{}{}
		}
		if s == "agent_end" {
			break
		}
	}
	return sseResult{stream: out, err: sc.Err()}
}

// TestEventsWaitPathSingleReader：reader 在运行中途连接，缓冲读空后必须
// 睡在 Cond.Wait 上；放行后必须被 Broadcast 唤醒并收到剩余事件。
// 这条路径正是 events 等待循环的核心（单 select 版本）。
func TestEventsWaitPathSingleReader(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())

	gate.arm()
	prompt202(t, hs, id, "hello")
	waitBuffered(t, srv, id, 6) // request_header 后还会发布 context_usage

	res := mustOpenEvents(t, hs, id)
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	// 读到第 5 条时，服务端 reader 已重放完缓冲、必然在 Cond.Wait 里：
	// gate 未放行 → 不可能有新事件；run 未结束 → done 不可能关闭。
	first := scanN(t, sc, 6)
	gate.release() // 放行运行：reader 必须被 Broadcast 唤醒
	rest := scanAll(t, sc)

	got := append(append([]string{}, first...), rest...)
	if want := wantSSE(); !slices.Equal(got, want) {
		t.Fatalf("stream mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestEventsMultiReader：一个 run 被两个 SSE 读者同时订阅（WebUI + CLI 场景），
// 一次 Broadcast 唤醒全部，各自收到完整一致的事件流。
func TestEventsMultiReader(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())

	gate.arm()
	prompt202(t, hs, id, "hello")
	waitBuffered(t, srv, id, 6)

	ready1 := make(chan struct{}, 1)
	ready2 := make(chan struct{}, 1)
	r1 := make(chan sseResult, 1)
	r2 := make(chan sseResult, 1)
	go func() { r1 <- readSSE(hs, id, 6, ready1) }()
	go func() { r2 <- readSSE(hs, id, 6, ready2) }()
	for i, ready := range []<-chan struct{}{ready1, ready2} {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatalf("reader %d never replayed 5 events", i)
		}
	}
	gate.release() // 两个 reader 都已在 Cond.Wait 里

	want := wantSSE()
	for i, rc := range []<-chan sseResult{r1, r2} {
		r := <-rc
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !slices.Equal(r.stream, want) {
			t.Fatalf("reader %d mismatch:\n got %v\nwant %v", i, r.stream, want)
		}
	}
}

// TestEventsClientDisconnect：客户端读到一半断开，reader 必须被哨兵广播
// 唤醒并退出——不泄漏 goroutine、不阻塞后续运行、缓冲重放不受影响。
func TestEventsClientDisconnect(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	baseline := runtime.NumGoroutine()

	gate.arm()
	prompt202(t, hs, id, "hello")
	waitBuffered(t, srv, id, 6)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(res.Body)
	scanN(t, sc, 6) // reader 已重放 request_header/context_usage 并进入等待
	cancel()        // 客户端断开 → 服务端哨兵广播 → reader 退出
	_ = res.Body.Close()
	gate.release() // 运行照常完成

	// 断开不影响运行本身：事件已全部进缓冲，完整重放仍可用。
	waitBuffered(t, srv, id, 12)
	//nolint:bodyclose // collectSSE owns and closes the response body.
	if replay := collectSSE(t, mustOpenEvents(t, hs, id)); !slices.Equal(replay, wantSSE()) {
		t.Fatalf("replay after disconnect: %v", replay)
	}

	// 新 prompt 照常工作（说明没有 goroutine 持锁阻塞新运行）。
	prompt202(t, hs, id, "again")
	//nolint:bodyclose // collectSSE owns and closes the response body.
	if got := collectSSE(t, mustOpenEvents(t, hs, id)); len(got) == 0 || got[len(got)-1] != "agent_end" {
		t.Fatalf("run after disconnect: %v", got)
	}

	// 断开的 reader goroutine 应已退出，回到基线。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}

// TestEventsReplayAfterDone：运行结束后 runState 仍留在 runs 表里，
// 再次连接仍能从头重放完整事件（前端刷新 / 断线重连依赖这个行为）。
func TestEventsReplayAfterDone(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "hi")
	//nolint:bodyclose // collectSSE owns and closes the response body.
	first := collectSSE(t, mustOpenEvents(t, hs, id))
	if want := wantSSE(); !slices.Equal(first, want) {
		t.Fatalf("first stream: %v", first)
	}
	//nolint:bodyclose // collectSSE owns and closes the response body.
	second := collectSSE(t, mustOpenEvents(t, hs, id))
	if want := wantSSE(); !slices.Equal(second, want) {
		t.Fatalf("replay after done: %v", second)
	}
}

// TestEventsNoRunEmptyStream：没有运行（st == nil）时返回空的 200 流。
func TestEventsNoRunEmptyStream(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	res, err := openEventsRaw(hs, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	if sc.Scan() {
		t.Fatalf("expected empty stream, got %q", sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
}

func firstLine(t *testing.T, path string) map[string]any {
	t.Helper()
	//nolint:gosec // path is created inside the test's temporary workspace.
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

func TestOpenIndexLifecycle(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	if _, ok := srv.sidx.Lookup(id); !ok {
		t.Fatal("create must index the new session")
	}

	// DELETE drops the index entry (dir removal succeeded).
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if _, ok := srv.sidx.Lookup(id); ok {
		t.Fatal("delete must drop the index entry")
	}

	// A session created outside the server (direct session.Create) is an index
	// miss: open falls back to a scan and self-heals.
	root := srv.cfg.Sessions.Root
	ext, err := session.Create(root, t.TempDir(), "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	extID := ext.ID()
	_ = ext.Close()
	if _, ok := srv.sidx.Lookup(extID); ok {
		t.Fatal("external session must not be indexed yet")
	}
	res2, err := authedGet(t, hs, "/v1/sessions/"+extID)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("external get: %d", res2.StatusCode)
	}
	if _, ok := srv.sidx.Lookup(extID); !ok {
		t.Fatal("fallback must self-heal the index")
	}

	// Stale entry (dir deleted behind the server's back) heals on open too.
	dir, _ := srv.sidx.Lookup(extID)
	if err := session.Remove(dir); err != nil {
		t.Fatal(err)
	}
	res3, err := authedGet(t, hs, "/v1/sessions/"+extID)
	if err != nil {
		t.Fatal(err)
	}
	_ = res3.Body.Close()
	if res3.StatusCode != http.StatusNotFound {
		t.Fatalf("stale get: %d", res3.StatusCode)
	}
	if _, ok := srv.sidx.Lookup(extID); ok {
		t.Fatal("stale entry must be dropped after failed open")
	}
}

func TestForkIndexesChild(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/fork", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fork: %d", res.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	child, _ := out["id"].(string)
	if child == "" {
		t.Fatal("fork response missing id")
	}
	if _, ok := srv.sidx.Lookup(child); !ok {
		t.Fatal("fork must index the child session")
	}
}
