package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
