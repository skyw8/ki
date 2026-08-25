package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/mcp"
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

func TestFSPreview(t *testing.T) {
	_, hs := testServer(t)
	dir := t.TempDir()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?files=1&path="+url.QueryEscape(dir), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var empty struct {
		Entries []fsEntry `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if empty.Entries == nil {
		t.Fatal("empty directory entries must encode as [] rather than null")
	}
	imagePath := filepath.Join(dir, "preview.png")
	want := []byte("\x89PNG\r\n\x1a\npreview")
	if err := os.WriteFile(imagePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?preview=1&path="+url.QueryEscape(imagePath), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, want) {
		t.Fatalf("preview status=%d content-type=%q body=%q", res.StatusCode, res.Header.Get("Content-Type"), got)
	}
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?preview=1&path="+url.QueryEscape(textPath), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") || string(got) != "not an image" {
		t.Fatalf("text preview status=%d content-type=%q body=%q", res.StatusCode, res.Header.Get("Content-Type"), got)
	}
	pdfPath := filepath.Join(dir, "document.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\nfixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?preview=1&path="+url.QueryEscape(pdfPath), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("pdf preview status=%d content-type=%q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	binaryPath := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binaryPath, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/fs?preview=1&path="+url.QueryEscape(binaryPath), nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("binary preview status=%d", res.StatusCode)
	}
}

// waitAgentEnd reads the events SSE until agent_end (the prompt turn finished
// and the session is fully persisted).
func waitAgentEnd(t *testing.T, hs *httptest.Server, id string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev loop.Event
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
		if ev.Type == loop.AgentEnd {
			break
		}
	}
	// Scan returns false for both clean EOF and read errors, so inspect Err
	// after the loop to avoid treating a truncated event stream as complete.
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
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

func TestPromptBuildsToolsFromResolvedModel(t *testing.T) {
	recorder := &reqRecorder{}
	srv, hs := testServerWith(t, recorder)
	id := createSession(t, hs, t.TempDir())

	prompt := func(model string) loop.Request {
		t.Helper()
		body, err := marshalJSON(map[string]any{"text": "inspect tools", "model": model})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("prompt status = %d", res.StatusCode)
		}
		waitAgentEnd(t, hs, id)
		return recorder.reqs[len(recorder.reqs)-1]
	}

	gpt := prompt("openai/gpt-5.6-terra")
	gptWant := []string{"Read", "apply_patch", "Grep", "Glob"}
	if srv.shells.BashAvailable() {
		gptWant = append(gptWant, "Bash")
	}
	if srv.shells.PowerShellEnabled() {
		gptWant = append(gptWant, "PowerShell")
	}
	gptWant = append(gptWant, "TaskOutput", "TaskStop")
	if srv.shells.BashAvailable() {
		gptWant = append(gptWant, "Monitor")
	}
	if got := requestToolNames(gpt.Tools); !slices.Equal(got, gptWant) {
		t.Fatalf("GPT tools = %v", got)
	}
	if gpt.Tools[1].Type != "custom" || gpt.Tools[1].Format == nil {
		t.Fatalf("GPT apply_patch spec = %+v", gpt.Tools[1])
	}
	readProps, ok := gpt.Tools[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("GPT Read properties = %#v", gpt.Tools[0].Parameters["properties"])
	}
	if readProps["pages"] == nil {
		t.Fatal("GPT rich Read is missing pages")
	}

	deepseek := prompt("deepseek/deepseek-v4-flash")
	deepseekWant := []string{"Read", "Write", "Edit", "Grep", "Glob"}
	if srv.shells.BashAvailable() {
		deepseekWant = append(deepseekWant, "Bash")
	}
	if srv.shells.PowerShellEnabled() {
		deepseekWant = append(deepseekWant, "PowerShell")
	}
	deepseekWant = append(deepseekWant, "TaskOutput", "TaskStop")
	if srv.shells.BashAvailable() {
		deepseekWant = append(deepseekWant, "Monitor")
	}
	if got := requestToolNames(deepseek.Tools); !slices.Equal(got, deepseekWant) {
		t.Fatalf("DeepSeek tools = %v", got)
	}
	if strings.Contains(deepseek.Tools[0].Description, "PDF") {
		t.Fatalf("DeepSeek Read leaked PDF description: %s", deepseek.Tools[0].Description)
	}
	readProps, ok = deepseek.Tools[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("DeepSeek Read properties = %#v", deepseek.Tools[0].Parameters["properties"])
	}
	if readProps["pages"] != nil {
		t.Fatalf("DeepSeek Read leaked pages: %+v", readProps)
	}
}

func requestToolNames(specs []loop.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
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
	call := func(method, path, body string) (int, map[string]any) {
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
		return res.StatusCode, out
	}

	status, _ := call(http.MethodPost, "/v1/providers", `{"id":"local","name":"Local","api":"completions","baseUrl":"http://127.0.0.1:11434/v1","enabled":true,"models":[{"id":"local/model","contextWindow":8192,"maxTokens":1024,"input":["text"],"reasoning":false,"cost":null}]}`)
	if status != http.StatusOK {
		t.Fatalf("create provider: %d", status)
	}
	status, out := call(http.MethodPut, "/v1/providers/local/credential", `{"apiKey":"secret"}`)
	if status != http.StatusOK || strings.Contains(fmt.Sprint(out), "secret") {
		t.Fatalf("credential response: %d %+v", status, out)
	}
	status, _ = call(http.MethodPut, "/v1/default-model", `{"provider":"local","model":"local/model"}`)
	if status != http.StatusOK {
		t.Fatalf("set last used: %d", status)
	}
	status, out = call(http.MethodPatch, "/v1/providers/local", `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("disable last-used provider: %d %+v", status, out)
	}
	def, _ := out["default"].(map[string]any)
	if def["provider"] == "local" {
		t.Fatalf("disabled last-used must fall back: %+v", out["default"])
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
		"model": "openai/gpt-5.6-terra",
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
	if models[0]["thinkingLevels"] == nil || models[0]["defaultThinking"] == nil {
		t.Fatalf("models missing thinking: %+v", models[0])
	}
}

func TestCreateAndForkKeepModelAndThinking(t *testing.T) {
	_, hs := testServer(t)
	cwd := t.TempDir()
	body, _ := marshalJSON(map[string]any{"cwd": cwd, "model": "openai/gpt-5.6-terra", "thinkingEffort": "high"})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	_ = json.NewDecoder(res.Body).Decode(&created)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create %d %+v", res.StatusCode, created)
	}
	if created["provider"] != "openai" || created["model"] != "gpt-5.6-terra" || created["thinkingEffort"] != "high" {
		t.Fatalf("create: %+v", created)
	}
	id, _ := created["id"].(string)

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	_ = res.Body.Close()
	if meta["provider"] != "openai" || meta["model"] != "gpt-5.6-terra" || meta["thinkingEffort"] == "" {
		t.Fatalf("meta should remember last used: %+v", meta)
	}

	body, _ = marshalJSON(map[string]any{"cwd": cwd})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var defSess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&defSess)
	_ = res.Body.Close()
	if defSess["provider"] != "openai" || defSess["model"] != "gpt-5.6-terra" || defSess["thinkingEffort"] != meta["thinkingEffort"] {
		t.Fatalf("omitted model uses last used %+v want meta %+v", defSess, meta)
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
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fork %d %+v", res.StatusCode, fork)
	}
	if fork["provider"] != "openai" || fork["model"] != "gpt-5.6-terra" || fork["thinkingEffort"] != "high" {
		t.Fatalf("fork: %+v", fork)
	}

	body, _ = marshalJSON(map[string]any{"model": "anthropic/claude-sonnet-5"})
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
	if patched["provider"] != "anthropic" || patched["model"] != "claude-sonnet-5" || patched["thinkingEffort"] != "high" {
		t.Fatalf("switch model must keep thinking: %+v", patched)
	}
}

func TestPromptWaitsForMCPAndContinuesAfterFailure(t *testing.T) {
	srv, hs := testServer(t)
	defer srv.mcp.Close()
	body, _ := marshalJSON(map[string]any{
		"mcpServers": map[string]any{
			"broken": map[string]any{},
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
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
	if !strings.Contains(joined, "mcp_server_failed") || !strings.Contains(joined, "agent_start") {
		t.Fatalf("failure did not continue prompt: %v", types)
	}
}

func TestFirstPromptIncludesFreshMCPTools(t *testing.T) {
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "fresh", Version: "1"}, nil)
	sdk.AddTool(mcpServer, &sdk.Tool{Name: "fresh_tool", Description: "available immediately"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	mcpHTTP := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpServer }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer mcpHTTP.Close()
	srv, hs := testServer(t)
	defer srv.mcp.Close()
	body, _ := marshalJSON(map[string]any{"mcpServers": map[string]any{"fresh": map[string]any{"url": mcpHTTP.URL}}})
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
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	found := false
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data:") {
			continue
		}
		var event loop.Event
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "data:"))), &event)
		if event.Type == loop.RequestHeader {
			for _, tool := range event.Tools {
				if tool.Name == "fresh_tool" {
					found = true
				}
			}
		}
		if event.Type == loop.AgentEnd {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("fresh MCP tool missing from first request")
	}
}

func TestSessionReloadQueuesWhileRunIsBusy(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	before := srv.resources.Load(id, sess.Header.CWD)
	_ = sess.Close()
	st, _, err := srv.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.requestReload(id) {
		t.Fatal("busy reload was not queued")
	}
	if got := srv.resources.Load(id, before.Environment.CWD); got.Revision != before.Revision {
		t.Fatal("busy reload invalidated the live snapshot")
	}
	srv.release(id, st)
	after := srv.resources.Load(id, before.Environment.CWD)
	if after.Revision == before.Revision {
		t.Fatal("queued reload was not applied after release")
	}
}

func waitRunIdle(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		pending := srv.pendingReload[id]
		srv.mu.Unlock()
		if !srv.running(id) && !pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not become idle (running=%v pending=%v)", id, srv.running(id), pending)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPromptReleaseMarksRunIdle(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "hello")
	waitAgentEnd(t, hs, id)
	waitRunIdle(t, srv, id)
	srv.mu.Lock()
	st := srv.runs[id]
	srv.mu.Unlock()
	if st == nil {
		t.Fatal("finished run must remain in s.runs for SSE replay")
	}
}

func TestPromptReleaseAppliesQueuedReload(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	cwd := t.TempDir()
	id := createSession(t, hs, cwd)
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	before := srv.resources.Load(id, sess.Header.CWD)
	_ = sess.Close()

	gate.arm()
	prompt202(t, hs, id, "hello")
	waitBuffered(t, srv, id, 6)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"/reload"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var handled map[string]any
	_ = json.NewDecoder(res.Body).Decode(&handled)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || handled["handled"] != true {
		t.Fatalf("busy /reload %d %+v", res.StatusCode, handled)
	}
	if !strings.Contains(fmt.Sprint(handled["notice"]), "queued") {
		t.Fatalf("expected queued notice: %+v", handled)
	}
	if got := srv.resources.Load(id, before.Environment.CWD); got.Revision != before.Revision {
		t.Fatal("busy reload invalidated the live snapshot")
	}

	gate.release()
	waitAgentEnd(t, hs, id)
	waitRunIdle(t, srv, id)

	after := srv.resources.Load(id, before.Environment.CWD)
	if after.Revision == before.Revision {
		t.Fatal("queued reload was not applied after prompt release")
	}
}

func promptJSON(t *testing.T, hs *httptest.Server, id, text string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{}
	if text != "" {
		body["text"] = text
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := marshalJSON(body)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	_ = res.Body.Close()
	return res.StatusCode, out
}

func sessionGET(t *testing.T, hs *httptest.Server, id string) map[string]any {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET session %d %+v", res.StatusCode, out)
	}
	return out
}

func TestMessageBusyToggle(t *testing.T) {
	_, hs := testServer(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/message", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || got["busy"] != "steer" {
		t.Fatalf("default message %+v %d", got, res.StatusCode)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/message", strings.NewReader(`{"busy":"queue"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&got)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || got["busy"] != "queue" {
		t.Fatalf("patched message %+v %d", got, res.StatusCode)
	}
}

func TestBusyPromptSteersSameRun(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	status, out := promptJSON(t, hs, id, "steer-me", map[string]any{"delivery": "steer"})
	if status != http.StatusAccepted || out["accepted"] != "steered" {
		t.Fatalf("steer %d %+v", status, out)
	}
	if !srv.running(id) {
		t.Fatal("steer must not start a second occupy")
	}
	gate.release()
	waitAgentEnd(t, hs, id)
	waitRunIdle(t, srv, id)
	detail := sessionGET(t, hs, id)
	raw, _ := json.Marshal(detail["messages"])
	if !strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "steer-me") {
		t.Fatalf("messages missing steered user: %s", raw)
	}
}

func TestBusyPromptQueuesUntilRelease(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	status, out := promptJSON(t, hs, id, "queued-me", map[string]any{"delivery": "queue"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("queue %d %+v", status, out)
	}
	detail := sessionGET(t, hs, id)
	queued, _ := detail["queued"].([]any)
	if len(queued) != 1 {
		t.Fatalf("queued = %+v", detail["queued"])
	}
	gate.release()
	waitAgentEnd(t, hs, id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		detail = sessionGET(t, hs, id)
		queued, _ = detail["queued"].([]any)
		raw, _ := json.Marshal(detail["messages"])
		if len(queued) == 0 && strings.Contains(string(raw), "queued-me") && !srv.running(id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued follow-up did not run: queued=%+v messages=%s running=%v", queued, raw, srv.running(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBusyParentIDStillConflicts(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	status, _ := promptJSON(t, hs, id, "branch", map[string]any{"parentId": "nope"})
	if status != http.StatusConflict {
		t.Fatalf("parentId busy status %d", status)
	}
	gate.release()
	waitAgentEnd(t, hs, id)
}

func TestQueueDefaultEnterSteerOverride(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/message", strings.NewReader(`{"busy":"queue"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	status, out := promptJSON(t, hs, id, "queued-default", nil)
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("default queue %d %+v", status, out)
	}
	status, out = promptJSON(t, hs, id, "enter-steer", map[string]any{"delivery": "steer"})
	if status != http.StatusAccepted || out["accepted"] != "steered" {
		t.Fatalf("enter steer override %d %+v", status, out)
	}
	found := false
	srv.mu.Lock()
	st := srv.runs[id]
	srv.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		for _, ev := range st.evs {
			if ev.Type == loop.SteerAccepted {
				found = true
				break
			}
		}
		st.mu.Unlock()
	}
	if !found {
		t.Fatal("missing steer_accepted on live run")
	}
	gate.release()
	waitAgentEnd(t, hs, id)
}

func TestBusyPromptPromotesQueueId(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	if status, out := promptJSON(t, hs, id, "keep-queued", map[string]any{"delivery": "queue"}); status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("queue keep %d %+v", status, out)
	}
	if status, out := promptJSON(t, hs, id, "promote-me", map[string]any{"delivery": "queue"}); status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("queue promote %d %+v", status, out)
	}
	detail := sessionGET(t, hs, id)
	queued, _ := detail["queued"].([]any)
	if len(queued) != 2 {
		t.Fatalf("queued = %+v", detail["queued"])
	}
	tail, _ := queued[1].(map[string]any)
	tailID, _ := tail["id"].(string)
	status, out := promptJSON(t, hs, id, "", map[string]any{"delivery": "steer", "queueId": tailID})
	if status != http.StatusAccepted || out["accepted"] != "steered" {
		t.Fatalf("promote %d %+v", status, out)
	}
	detail = sessionGET(t, hs, id)
	queued, _ = detail["queued"].([]any)
	if len(queued) != 1 {
		t.Fatalf("after promote queued = %+v", detail["queued"])
	}
	keep, _ := queued[0].(map[string]any)
	if keep["id"] == tailID {
		t.Fatal("promoted tail still in queue")
	}
	found := false
	srv.mu.Lock()
	st := srv.runs[id]
	srv.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		for _, ev := range st.evs {
			if ev.Type == loop.SteerAccepted {
				found = true
				break
			}
		}
		st.mu.Unlock()
	}
	if !found {
		t.Fatal("missing steer_accepted on live run")
	}
	if !srv.running(id) {
		t.Fatal("promote must not occupy a second run")
	}
	gate.release()
	waitAgentEnd(t, hs, id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		detail = sessionGET(t, hs, id)
		queued, _ = detail["queued"].([]any)
		raw, _ := json.Marshal(detail["messages"])
		if len(queued) == 0 && strings.Contains(string(raw), "promote-me") && strings.Contains(string(raw), "keep-queued") && !srv.running(id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("promote follow-up: queued=%+v messages=%s running=%v", queued, raw, srv.running(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPromoteQueueIdRejectsUnknownAndContent(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	status, _ := promptJSON(t, hs, id, "", map[string]any{"delivery": "steer", "queueId": "missing"})
	if status != http.StatusBadRequest {
		t.Fatalf("missing queueId status %d", status)
	}
	status, _ = promptJSON(t, hs, id, "also-body", map[string]any{"delivery": "steer", "queueId": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("queueId+content status %d", status)
	}
	status, _ = promptJSON(t, hs, id, "", map[string]any{"delivery": "queue", "queueId": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("queueId+queue delivery status %d", status)
	}
	status, _ = promptJSON(t, hs, id, "", map[string]any{"delivery": "steer", "queueId": "x", "parentId": "y"})
	if status != http.StatusBadRequest {
		t.Fatalf("queueId+parentId status %d", status)
	}
	gate.release()
	waitAgentEnd(t, hs, id)
}

func TestIdlePromoteQueueIdStartsRun(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("session dir")
	}
	item, err := session.Enqueue(dir, []types.Content{{Type: "text", Text: "from-queue"}})
	if err != nil {
		t.Fatal(err)
	}
	status, out := promptJSON(t, hs, id, "", map[string]any{"delivery": "steer", "queueId": item.ID})
	if status != http.StatusAccepted || out["accepted"] != "started" {
		t.Fatalf("idle promote %d %+v", status, out)
	}
	waitAgentEnd(t, hs, id)
	waitRunIdle(t, srv, id)
	detail := sessionGET(t, hs, id)
	queued, _ := detail["queued"].([]any)
	raw, _ := json.Marshal(detail["messages"])
	if len(queued) != 0 || !strings.Contains(string(raw), "from-queue") {
		t.Fatalf("idle promote result queued=%+v messages=%s", queued, raw)
	}
}

func TestCompactPromoteQueueIdRestoresHead(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	st, _, err := srv.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("session dir")
	}
	item, err := session.Enqueue(dir, []types.Content{{Type: "text", Text: "during-compact"}})
	if err != nil {
		t.Fatal(err)
	}
	status, out := promptJSON(t, hs, id, "", map[string]any{"delivery": "steer", "queueId": item.ID})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("compact promote %d %+v", status, out)
	}
	left, err := session.ReadQueue(dir)
	if err != nil || len(left) != 1 || left[0].ID != item.ID {
		t.Fatalf("restored %+v %v", left, err)
	}
	srv.release(id, st)
	deadline := time.Now().Add(5 * time.Second)
	for {
		detail := sessionGET(t, hs, id)
		queued, _ := detail["queued"].([]any)
		raw, _ := json.Marshal(detail["messages"])
		running, _ := detail["running"].(bool)
		// Wait until occupy finished so TempDir cleanup is not racing jsonl writers.
		if !running && len(queued) == 0 && strings.Contains(string(raw), "during-compact") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("compact promote dispatch: running=%v queued=%+v messages=%s", running, queued, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAbortEmitsRunAborted(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "first")
	waitBuffered(t, srv, id, 6)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/abort", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abort %d", res.StatusCode)
	}
	found := false
	srv.mu.Lock()
	st := srv.runs[id]
	srv.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		for _, ev := range st.evs {
			if ev.Type == loop.RunAborted {
				found = true
				break
			}
		}
		st.mu.Unlock()
	}
	if !found {
		t.Fatal("missing run_aborted on live run")
	}
	gate.release()
	waitAgentEnd(t, hs, id)
}

func TestCompactOccupySteersAsQueue(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	st, _, err := srv.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	status, out := promptJSON(t, hs, id, "during-compact", map[string]any{"delivery": "steer"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("compact steer %d %+v", status, out)
	}
	srv.release(id, st)
	deadline := time.Now().Add(5 * time.Second)
	for {
		detail := sessionGET(t, hs, id)
		queued, _ := detail["queued"].([]any)
		raw, _ := json.Marshal(detail["messages"])
		if len(queued) == 0 && strings.Contains(string(raw), "during-compact") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("compact queue not dispatched: queued=%+v messages=%s", queued, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMCPNotificationStreamReplaysActionableState(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	_ = srv.resources.Load(id, sess.Header.CWD)
	_ = sess.Close()
	srv.publishMCPEvent(id, mcp.Notification{Kind: "tools_changed", Server: "demo", Message: "changed"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events?notifications=1", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event loop.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != loop.MCPToolsChanged || event.Server != "demo" || !event.ReloadRequired || event.EntryID == "" {
			t.Fatalf("event = %+v", event)
		}
		return
	}
	t.Fatalf("notification stream ended: %v", scanner.Err())
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
	body, _ := marshalJSON(map[string]any{"disabled": []string{"alpha"}})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/skills", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch skills %d", res.StatusCode)
	}
	body, _ = marshalJSON(map[string]any{"disabled": []string{"exa"}})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch mcp %d", res.StatusCode)
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
// Dedicated tests for the events (SSE) wait loop.
//
// Deterministically exercise the three key paths in the events handler wait loop:
//  1) Wait path: a reader connects mid-run and waits on Cond when the buffer is empty;
//  2) Multi-reader broadcast: two SSE readers subscribe to one run and each receives
//     the same complete event stream;
//  3) Client disconnect: the reader is awakened by the sentinel and exits without
//     leaking a goroutine or blocking later runs.
// Together with `go test -race`, -race verifies lock discipline while these tests
// verify behavior. The tests cannot prove concurrent correctness; that requires
// invariants/model checking (see tmp/tla-demo). They provide regression protection:
// refactoring the wait loop must preserve the byte-for-byte behavior.
// ---------------------------------------------------------------------------

// gateStreamer blocks in Stream until release(), allowing a test to freeze a run
// before streaming output. At that point the buffer contains only the five events
// from agent_start through request_header, so an SSE reader connecting afterward
// must enter the Cond.Wait path (without releasing the gate, it cannot be elsewhere).
// arm() installs a gate for the next run; release() opens it and clears the gate so
// later runs pass through directly.
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

// wantSSE is the event sequence (type[:role]) for one complete prompt using the
// gated Scripted model.
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

func TestEditBranchesInPlaceAndForkAtCreatesPathSession(t *testing.T) {
	srv, hs := testServerWith(t, &provider.Scripted{})
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "original")
	waitAgentEnd(t, hs, id)

	post := func(body map[string]any) {
		t.Helper()
		b, err := marshalJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("edit prompt = %d", res.StatusCode)
		}
	}
	post(map[string]any{"parentId": "", "content": []map[string]any{{"type": "text", "text": "edited"}}})
	waitAgentEnd(t, hs, id)

	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("source session missing from index")
	}
	src, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	var users, assistants []session.Entry
	for _, e := range src.Entries() {
		if e.Type != "message" || e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "user":
			users = append(users, e)
		case "assistant":
			assistants = append(assistants, e)
		}
	}
	if len(users) != 2 || users[0].ParentID != "" || users[1].ParentID != "" {
		t.Fatalf("user branches: %+v", users)
	}
	if src.MessagesToLeaf()[0].Text() != "edited" {
		t.Fatalf("active branch: %+v", src.MessagesToLeaf())
	}

	b, err := marshalJSON(map[string]any{"entryId": assistants[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/fork", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fork = %d", res.StatusCode)
	}
	var child struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	childDir, ok := srv.sidx.Lookup(child.ID)
	if !ok || childDir == dir {
		t.Fatalf("fork directory = %q source = %q", childDir, dir)
	}
	forked, err := session.Open(childDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forked.Close() }()
	if forked.Header.ParentSession != id {
		t.Fatalf("fork parent = %q", forked.Header.ParentSession)
	}
	if got := forked.MessagesToLeaf(); len(got) != 2 || got[0].Text() != "original" {
		t.Fatalf("fork path: %+v", got)
	}
}

func TestUploadAttachmentStoresContentAddressedBlob(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "pasted.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-image")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/attachments", &body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d", res.StatusCode)
	}
	var got types.Content
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	dir, _ := srv.sidx.Lookup(id)
	if got.ID == "" || got.Name != "pasted.png" || !strings.HasPrefix(got.Path, filepath.Join(dir, "attachments")) {
		t.Fatalf("attachment: %+v", got)
	}
	if b, err := os.ReadFile(got.Path); err != nil || string(b) != "fake-image" {
		t.Fatalf("stored blob = %q, %v", b, err)
	}
}

// waitBuffered polls until the runState buffer contains at least n events. It reads
// internal state in this package, locking in srv.mu / st.mu order.
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

// sseLabel reduces a data: line to "type[:role]".
func sseLabel(line string) string {
	var ev loop.Event
	_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
	s := string(ev.Type)
	if ev.Message != nil && (ev.Type == loop.MessageStart || ev.Type == loop.MessageEnd) {
		s += ":" + ev.Message.Role
	}
	return s
}

// scanN reads n SSE events from scanner, counting only data: lines.
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

// scanAll reads through agent_end, or until the stream ends.
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

// collectSSE reads an entire SSE response through agent_end.
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

// readSSE asynchronously reads the complete event stream and signals ready after
// reading the first n events. This lets the test confirm both readers replayed the
// buffer and entered the wait before releasing the run.
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

// TestEventsWaitPathSingleReader: a reader connecting mid-run must wait on Cond
// after draining the buffer, then be awakened by Broadcast and receive the
// remaining events. This is the core path of the events wait loop (single-select
// version).
func TestEventsWaitPathSingleReader(t *testing.T) {
	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	id := createSession(t, hs, t.TempDir())

	gate.arm()
	prompt202(t, hs, id, "hello")
	waitBuffered(t, srv, id, 6) // context_usage is published after request_header.

	res := mustOpenEvents(t, hs, id)
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	// After reading the fifth event, the server reader has replayed the buffer and
	// must be in Cond.Wait: the gate is closed, so no new event is possible, and the
	// run is unfinished, so done cannot be closed.
	first := scanN(t, sc, 6)
	gate.release() // Release the run; Broadcast must awaken the reader.
	rest := scanAll(t, sc)

	got := append(append([]string{}, first...), rest...)
	if want := wantSSE(); !slices.Equal(got, want) {
		t.Fatalf("stream mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestEventsMultiReader: two SSE readers subscribe to one run (the WebUI + CLI
// scenario); one Broadcast wakes both, and each receives the same complete stream.
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
	gate.release() // Both readers are now in Cond.Wait.

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

// TestEventsClientDisconnect: after a client disconnects halfway through the
// stream, the sentinel broadcast must wake and exit the reader without leaking a
// goroutine, blocking later runs, or affecting buffer replay.
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
	scanN(t, sc, 6) // The reader replayed request_header/context_usage and is waiting.
	cancel()        // Client disconnects; the sentinel broadcasts and the reader exits.
	_ = res.Body.Close()
	gate.release() // The run completes normally.

	// Disconnecting does not affect the run: all events are buffered and remain
	// available for complete replay.
	waitBuffered(t, srv, id, 12)
	//nolint:bodyclose // collectSSE owns and closes the response body.
	if replay := collectSSE(t, mustOpenEvents(t, hs, id)); !slices.Equal(replay, wantSSE()) {
		t.Fatalf("replay after disconnect: %v", replay)
	}

	// A new prompt works normally, showing that no goroutine holds a lock that blocks it.
	prompt202(t, hs, id, "again")
	//nolint:bodyclose // collectSSE owns and closes the response body.
	if got := collectSSE(t, mustOpenEvents(t, hs, id)); len(got) == 0 || got[len(got)-1] != "agent_end" {
		t.Fatalf("run after disconnect: %v", got)
	}

	// The disconnected reader goroutine should have exited, returning to the baseline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}

// TestEventsReplayAfterDone: after a run ends, runState remains in the runs table,
// so a new connection can replay the complete event stream from the beginning
// (frontend refresh and reconnect depend on this behavior).
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

// TestEventsNoRunEmptyStream: when there is no run (st == nil), return an empty
// 200 stream.
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
	line, _, _ := strings.Cut(string(b), "\n")
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

// TestReloadInvalidatesCachedResources pins the server-level reload entry
// point: one session's resources stay pinned until srv.Reload(), after which
// the next session catalog sees disk changes.
func TestReloadInvalidatesCachedResources(t *testing.T) {
	srv, hs := testServer(t)
	cwd := t.TempDir()
	home := srv.cfg.Home

	// Seed one home skill before first discovery.
	skillDir := filepath.Join(home, "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\ndescription: first\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL-ONE"), 0o600)
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("CWD-ONE"), 0o600)

	id := createSession(t, hs, cwd)
	names := func() map[string]bool {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
		req.Header.Set("Authorization", "Bearer tok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var got map[string]any
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		avail, _ := got["availableSkills"].([]any)
		out := map[string]bool{}
		for _, a := range avail {
			m, _ := a.(map[string]any)
			if n, _ := m["name"].(string); n != "" {
				out[n] = true
			}
		}
		return out
	}

	if got := names(); !got["alpha"] {
		t.Fatalf("first catalog: %+v", got)
	}

	// Disk changes after first discovery: cache must stay pinned.
	beta := filepath.Join(home, "skills", "beta")
	if err := os.MkdirAll(beta, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "SKILL.md"), []byte("---\nname: beta\ndescription: added later\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("CWD-TWO"), 0o600)
	if got := names(); got["beta"] {
		t.Fatalf("catalog picked up disk change before reload: %+v", got)
	}

	// srv.Reload() invalidates the unified snapshot; next catalog sees beta.
	srv.Reload()
	if got := names(); !got["beta"] {
		t.Fatalf("catalog stale after reload: %+v", got)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"/reload"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var handled map[string]any
	_ = json.NewDecoder(res.Body).Decode(&handled)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || handled["handled"] != true {
		t.Fatalf("slash reload %d %+v", res.StatusCode, handled)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"/nope"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	handled = map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&handled)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || handled["error"] != true {
		t.Fatalf("unknown slash %d %+v", res.StatusCode, handled)
	}
}

// TestCompactInvalidatesCachedResources pins the contract that a successful
// compaction is a reload point: the next prompt build re-reads skills and
// AGENTS/CLAUDE from disk instead of the session-pinned snapshot.
func TestCompactInvalidatesCachedResources(t *testing.T) {
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

	cwd := t.TempDir()
	// Seed one skill and one AGENTS.md before first discovery.
	skillDir := filepath.Join(home, "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\ndescription: first\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("CWD-ONE"), 0o600)

	id := createSession(t, hs, cwd)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res0, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res0.Body.Close()
	waitAgentEnd(t, hs, id) // first prompt discovered the cached snapshot

	// Add a skill on disk after discovery; the running snapshot must not see it.
	beta := filepath.Join(home, "skills", "beta")
	if err := os.MkdirAll(beta, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "SKILL.md"), []byte("---\nname: beta\ndescription: added later\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("CWD-TWO"), 0o600)

	// Manual compact succeeds and must invalidate the caches.
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

	// Next prompt must rebuild the prompt from disk: beta visible now.
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"again"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res1, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res1.Body.Close()
	waitAgentEnd(t, hs, id)

	// The rebuilt system prompt must carry beta and the new AGENTS content.
	if len(rec.reqs) == 0 {
		t.Fatal("no stream requests captured")
	}
	sys := rec.reqs[len(rec.reqs)-1].System
	if sys == "" {
		t.Fatal("no system prompt captured")
	}
	if !strings.Contains(sys, "beta") {
		t.Fatalf("beta missing after compact reload: %s", sys)
	}
	if !strings.Contains(sys, "CWD-TWO") {
		t.Fatalf("AGENTS.md stale after compact reload: %s", sys)
	}
}

func TestExtensionsHTTPToggle(t *testing.T) {
	srv, hs := testServer(t)
	home := srv.cfg.Home
	dir := filepath.Join(home, "extensions", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), []byte("EXT-APPEND"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/extensions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || len(listed.Items) != 1 || listed.Items[0]["enabled"] != true {
		t.Fatalf("get extensions %d %+v", res.StatusCode, listed.Items)
	}
	if _, ok := listed.Items[0]["path"].(string); !ok {
		t.Fatal("path must be a string, not href")
	}
	body, _ := marshalJSON(map[string]any{"disabled": []string{"alpha"}})
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/extensions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if listed.Items[0]["enabled"] != false {
		t.Fatalf("patched %+v", listed.Items)
	}
	id := createSession(t, hs, t.TempDir())
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	_ = res.Body.Close()
	exts, _ := sess["availableExtensions"].([]any)
	if len(exts) != 1 {
		t.Fatalf("session catalog %+v", sess["availableExtensions"])
	}
}

func TestPromptWithDeclarativeExtensionFinishes(t *testing.T) {
	srv, hs := testServer(t)
	home := srv.cfg.Home
	dir := filepath.Join(home, "extensions", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), []byte("KEEP-OUT"), 0o600); err != nil {
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
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt %d", res.StatusCode)
	}
	_ = res.Body.Close()
	waitAgentEnd(t, hs, id)
}

func TestPromptWithSidecarExtensionFinishes(t *testing.T) {
	srv, hs := testServer(t)
	bin := filepath.Join(t.TempDir(), "sidecar")
	src := filepath.Join("..", "..", "e2e", "testdata", "extensions", "sidecar")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sidecar: %v\n%s", err, out)
	}
	dir := filepath.Join(srv.cfg.Home, "extensions", "protected-paths")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"protected-paths","capabilities":["prompt.append","lifecycle"],"prompt":{"append":["APPEND.md"]},"runtime":{"kind":"rpc","command":"` + bin + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "APPEND.md"), []byte("KEEP-OUT"), 0o600); err != nil {
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
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt %d", res.StatusCode)
	}
	_ = res.Body.Close()
	waitAgentEnd(t, hs, id)
}
