package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

type occupyGateStreamer struct {
	gate    <-chan struct{}
	mu      sync.Mutex
	prompts []string
}

func (g *occupyGateStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return types.Message{}, ctx.Err()
	}
	var users []string
	for _, m := range req.Messages {
		if m.Role == "user" {
			users = append(users, m.Text())
		}
	}
	g.mu.Lock()
	g.prompts = append(g.prompts, strings.Join(users, "|"))
	n := len(g.prompts)
	g.mu.Unlock()
	return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "asst-" + strings.Repeat("x", n)}}}, nil
}

func waitRunning(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.running(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session never occupied")
}

func waitIdle(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.running(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session still occupied")
}

func waitContextQueueEmpty(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dir, ok := srv.sidx.Lookup(id)
		if ok {
			items, err := session.ReadContextQueue(dir)
			if err == nil && len(items) == 0 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("context queue is not empty")
}

func waitPrompts(t *testing.T, g *occupyGateStreamer, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		got := append([]string{}, g.prompts...)
		g.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	t.Fatalf("prompts %v want %d", g.prompts, n)
	return nil
}

func postJSON(t *testing.T, hs *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, hs.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestExtEnqueueIdleStarts(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatalf("create %d %+v", status, created)
	}
	id, _ := created["id"].(string)
	res, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{
		Content:        []types.Content{{Type: "text", Text: "go"}},
		IdempotencyKey: "telegram:update:started",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "started" {
		t.Fatalf("accepted %q", res.Accepted)
	}
	waitIdle(t, srv, id)
	srv.mu.Lock()
	delete(srv.idempotency, id+"/goal/telegram:update:started")
	srv.mu.Unlock()
	duplicate, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{
		Content:        []types.Content{{Type: "text", Text: "go again"}},
		IdempotencyKey: "telegram:update:started",
	})
	if err != nil || duplicate.Accepted != "duplicate" {
		t.Fatalf("durable duplicate result=%+v err=%v", duplicate, err)
	}
}

func TestAppendMessageCommitsHistoryWithoutStartingRun(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatalf("create %d %+v", status, created)
	}
	id, _ := created["id"].(string)
	res, err := srv.AppendMessage(id, "telegram-bot", extension.AppendMessageRequest{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "group context"}}},
		IdempotencyKey: "telegram:update:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "appended" || srv.running(id) {
		t.Fatalf("append result=%+v running=%v", res, srv.running(id))
	}
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("session dir")
	}
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.MessagesToLeaf(); len(got) != 1 || got[0].Text() != "group context" {
		t.Fatalf("history=%+v", got)
	}
	_ = sess.Close()
	duplicate, err := srv.AppendMessage(id, "telegram-bot", extension.AppendMessageRequest{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "duplicate"}}},
		IdempotencyKey: "telegram:update:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Accepted != "duplicate" {
		t.Fatalf("duplicate result=%+v", duplicate)
	}
	sess, _ = session.Open(dir)
	if got := sess.MessagesToLeaf(); len(got) != 1 {
		t.Fatalf("duplicate changed history=%+v", got)
	}
	_ = sess.Close()
}

func TestAppendMessagePreservesPromptBoundary(t *testing.T) {
	gate := make(chan struct{})
	g := &occupyGateStreamer{gate: gate}
	srv, hs := testServerWith(t, g)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatalf("create %d", status)
	}
	id, _ := created["id"].(string)
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "first"})
	if status != http.StatusAccepted {
		t.Fatalf("prompt %d", status)
	}
	waitRunning(t, srv, id)
	before, err := srv.AppendMessage(id, "telegram-bot", extension.AppendMessageRequest{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "before mention"}}},
		IdempotencyKey: "telegram:update:2",
	})
	if err != nil || before.Accepted != "queued" {
		t.Fatalf("before append result=%+v err=%v", before, err)
	}
	queued, err := srv.Enqueue(id, "telegram-bot", extension.EnqueueRequest{
		Content:        []types.Content{{Type: "text", Text: "mentioned"}},
		DeliverAs:      "queue",
		IdempotencyKey: "telegram:update:3",
	})
	if err != nil || queued.Accepted != "queued" {
		t.Fatalf("enqueue result=%+v err=%v", queued, err)
	}
	after, err := srv.AppendMessage(id, "telegram-bot", extension.AppendMessageRequest{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "after mention"}}},
		IdempotencyKey: "telegram:update:4",
	})
	if err != nil || after.Accepted != "queued" {
		t.Fatalf("after append result=%+v err=%v", after, err)
	}
	close(gate)
	prompts := waitPrompts(t, g, 2)
	waitIdle(t, srv, id)
	waitContextQueueEmpty(t, srv, id)
	if !strings.Contains(prompts[1], "before mention") || !strings.Contains(prompts[1], "mentioned") || strings.Contains(prompts[1], "after mention") {
		t.Fatalf("prompt boundary lost: %v", prompts)
	}
	dir, _ := srv.sidx.Lookup(id)
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	var history []string
	for _, message := range sess.MessagesToLeaf() {
		history = append(history, message.Text())
	}
	joined := strings.Join(history, "|")
	if !strings.Contains(joined, "before mention") || !strings.Contains(joined, "mentioned") || !strings.Contains(joined, "after mention") {
		t.Fatalf("history missing context: %v", history)
	}
}

func TestExtEnqueueBusyQueuesAndUserDrainsFirst(t *testing.T) {
	gate := make(chan struct{})
	g := &occupyGateStreamer{gate: gate}
	srv, hs := testServerWith(t, g)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatalf("create %d", status)
	}
	id, _ := created["id"].(string)
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatalf("prompt %d", status)
	}
	waitRunning(t, srv, id)
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("dir")
	}
	if _, err := session.Enqueue(dir, []types.Content{{Type: "text", Text: "user-next"}}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{Content: []types.Content{{Type: "text", Text: "ext-next"}}, DeliverAs: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "queued" {
		t.Fatalf("ext accepted %q", res.Accepted)
	}
	uq, _ := session.ReadQueue(dir)
	eq, _ := session.ReadExtQueue(dir)
	if len(uq) == 0 || len(eq) == 0 {
		t.Fatalf("want both queues user=%d ext=%d", len(uq), len(eq))
	}
	close(gate)
	prompts := waitPrompts(t, g, 3)
	waitIdle(t, srv, id)
	if prompts[0] != "hello" || prompts[1] != "hello|user-next" || !strings.Contains(prompts[2], "ext-next") {
		t.Fatalf("occupy order %v", prompts)
	}
	if !strings.Contains(prompts[1], "user-next") || strings.Contains(prompts[1], "ext-next") {
		t.Fatalf("user occupy must precede ext: %v", prompts)
	}
}

func TestWhenSettledDoesNotStartWhileBusy(t *testing.T) {
	gate := make(chan struct{})
	g := &occupyGateStreamer{gate: gate}
	srv, hs := testServerWith(t, g)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatal(status)
	}
	waitRunning(t, srv, id)
	res, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{Content: []types.Content{{Type: "text", Text: "later"}}, When: "settled"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "scheduled" {
		t.Fatalf("settled while busy: %q", res.Accepted)
	}
	if !srv.running(id) {
		t.Fatal("occupy ended before settled flush")
	}
	close(gate)
	prompts := waitPrompts(t, g, 2)
	waitIdle(t, srv, id)
	if !strings.Contains(prompts[1], "later") {
		t.Fatalf("settled flush occupy %v", prompts)
	}
}

func TestNextTurnDoesNotStartOccupy(t *testing.T) {
	gate := make(chan struct{})
	g := &occupyGateStreamer{gate: gate}
	srv, hs := testServerWith(t, g)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatal(status)
	}
	waitRunning(t, srv, id)
	res, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{Content: []types.Content{{Type: "text", Text: "next-turn-ctx"}}, DeliverAs: "nextTurn"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "queued" {
		t.Fatalf("%q", res.Accepted)
	}
	close(gate)
	_ = waitPrompts(t, g, 1)
	waitIdle(t, srv, id)
	dir, _ := srv.sidx.Lookup(id)
	eq, _ := session.ReadExtQueue(dir)
	if len(eq) != 1 || eq[0].When != "nextTurn" {
		t.Fatalf("nextTurn must remain queued: %+v", eq)
	}
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "user-two"})
	if status != http.StatusAccepted {
		t.Fatal(status)
	}
	prompts := waitPrompts(t, g, 2)
	waitIdle(t, srv, id)
	if !strings.Contains(prompts[1], "user-two") || !strings.Contains(prompts[1], "next-turn-ctx") {
		t.Fatalf("nextTurn not injected into user occupy: %v", prompts)
	}
	if left, _ := session.ReadExtQueue(dir); len(left) != 0 {
		t.Fatalf("nextTurn leftover %+v", left)
	}
}

func TestAppendEntryOwnExtension(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	if err := srv.AppendEntry(id, "goal", "goal-state", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	dir, _ := srv.sidx.Lookup(id)
	got := session.CustomEntries(dir, "goal")
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if session.CustomEntries(dir, "other") != nil && len(session.CustomEntries(dir, "other")) != 0 {
		t.Fatal("leaked other extension")
	}
}

func TestSidecarSetStatusAppearsOnSession(t *testing.T) {
	bin := buildExtSidecar(t)
	srv, hs := testServer(t)
	home := srv.cfg.Home
	dir := filepath.Join(home, "extensions", "goalui")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"goalui","capabilities":["lifecycle"],"runtime":{"kind":"rpc","command":"` + strings.ReplaceAll(bin, `\`, `\\`) + `","env":{"KI_SET_UI":"1"}}}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatal(status)
	}
	waitAgentEnd(t, hs, id)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
		raw, err := json.Marshal(got["extensionUi"])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("Goal · active")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
	t.Fatalf("extensionUi missing chip: %+v", got["extensionUi"])
}

func buildExtSidecar(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "testdata", "extensions", "sidecar")
	bin := filepath.Join(t.TempDir(), "sidecar")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // builds the local sidecar test fixture
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestExtensionUIProjection(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	if err := srv.UISetStatus(id, "goal", "goal", "Goal · active", "active"); err != nil {
		t.Fatal(err)
	}
	if err := srv.UISetPanel(id, "goal", extension.UIPanel{Title: "Goal", Summary: "do the thing"}); err != nil {
		t.Fatal(err)
	}
	_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
	raw, err := json.Marshal(got["extensionUi"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Goal · active")) || !bytes.Contains(raw, []byte("do the thing")) {
		t.Fatalf("extensionUi %s", raw)
	}
}

func TestGlobalExtensionUIProjection(t *testing.T) {
	srv, hs := testServer(t)
	dir := filepath.Join(srv.cfg.Home, "extensions", "goal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"goal","capabilities":[]}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.GlobalUISetStatus("goal", "goal", "Goal", "info"); err != nil {
		t.Fatal(err)
	}
	if err := srv.GlobalUISetPanel("goal", extension.UIPanel{Title: "Goal", Summary: "choose a session"}); err != nil {
		t.Fatal(err)
	}
	status, got := postJSON(t, hs, http.MethodGet, "/v1/extensions", nil)
	if status != http.StatusOK {
		t.Fatalf("extensions status %d", status)
	}
	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("extensions items %+v", got["items"])
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item["name"] != "goal" {
			continue
		}
		ui, ok := item["ui"].(map[string]any)
		if !ok {
			t.Fatalf("global ui missing: %+v", item)
		}
		status, ok := ui["status"].(map[string]any)
		if !ok || status["text"] != "Goal" {
			t.Fatalf("global status %+v", ui["status"])
		}
		panel, ok := ui["panel"].(map[string]any)
		if !ok || panel["summary"] != "choose a session" {
			t.Fatalf("global panel %+v", ui["panel"])
		}
		return
	}
	t.Fatalf("goal extension missing: %+v", items)
}

func TestSnapshotFields(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	snap, err := srv.Snapshot(id, "goal")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Running && snap.Idle {
		t.Fatal("idle xor running")
	}
	if err := srv.SetActiveTools(id, "goal", []string{"Read", "Nope"}); err != nil {
		t.Fatal(err)
	}
	snap, _ = srv.Snapshot(id, "goal")
	if len(snap.ActiveTools) != 1 || snap.ActiveTools[0] != "Read" {
		t.Fatalf("active tools %+v (unknown names dropped)", snap.ActiveTools)
	}
	if err := srv.SetActiveTools(id, "goal", []string{"Nope", "AlsoNope"}); err != nil {
		t.Fatal(err)
	}
	snap, _ = srv.Snapshot(id, "goal")
	if len(snap.ActiveTools) != 1 || snap.ActiveTools[0] != "Read" {
		t.Fatalf("all-unknown must keep previous %+v", snap.ActiveTools)
	}
}

func TestExtEnqueuePersistsOrigin(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	res, err := srv.Enqueue(id, "goal", extension.EnqueueRequest{Content: []types.Content{{Type: "text", Text: "from-ext"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != "started" {
		t.Fatalf("%q", res.Accepted)
	}
	waitAgentEnd(t, hs, id)
	_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
	raw, err := json.Marshal(got["messages"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("extension:goal")) {
		t.Fatalf("origin missing: %s", raw)
	}
}

func TestPatchSessionRejectsUnknownModel(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	if err := srv.PatchSession(id, "no-such-provider/no-such-model", ""); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestUIConfirmRoundTrip(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	done := make(chan bool, 1)
	go func() {
		ok, err := srv.UIConfirm(id, "goal", "Continue?", "really")
		done <- err == nil && ok
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
		raw, err := json.Marshal(got["extensionUi"])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("Continue?")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/extension-ui", map[string]any{
		"kind": "confirm", "extension": "goal", "ok": true,
	})
	if status != http.StatusOK {
		t.Fatalf("answer %d", status)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("confirm returned false")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("confirm did not return")
	}
}

func TestUIActionHTTP(t *testing.T) {
	srv, hs := testServer(t)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	if err := srv.UISetPanel(id, "goal", extension.UIPanel{
		Title: "Goal", Actions: []extension.UIAction{{ID: "ack", Label: "Ack"}},
	}); err != nil {
		t.Fatal(err)
	}
	status, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/extension-ui", map[string]any{
		"kind": "action", "extension": "goal", "value": "ack",
	})
	if status != http.StatusOK {
		t.Fatalf("action %d", status)
	}
}

func waitRuntimeReady(t *testing.T, hs *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var got map[string]any
	for time.Now().Before(deadline) {
		_, got = postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
		rt, _ := got["runtime"].(map[string]any)
		if rt["ready"] == true {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runtime not ready: %+v", got["runtime"])
	return got
}

func installSidecar(t *testing.T, home, name, bin, caps, envJSON string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","capabilities":[` + caps + `],"runtime":{"kind":"rpc","command":"` + strings.ReplaceAll(bin, `\`, `\\`) + `"`
	if envJSON != "" {
		manifest += `,"env":` + envJSON
	}
	manifest += `}}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGetSessionWarmsRuntimeCommands(t *testing.T) {
	bin := buildExtSidecar(t)
	srv, hs := testServer(t)
	installSidecar(t, srv.cfg.Home, "shipper", bin, `"command"`, "")
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	got := waitRuntimeReady(t, hs, id)
	raw, err := json.Marshal(got["commands"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"ship"`)) {
		t.Fatalf("commands after GET warmup: %s", raw)
	}
}

func TestGetHistoricalSessionWarmsWithoutPrompt(t *testing.T) {
	bin := buildExtSidecar(t)
	srv, hs := testServer(t)
	installSidecar(t, srv.cfg.Home, "goalui", bin, `"lifecycle"`, `{"KI_SET_UI":"1"}`)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	waitRuntimeReady(t, hs, id)
	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		_, got := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+id, nil)
		var err error
		raw, err = json.Marshal(got["extensionUi"])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("Goal · active")) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("chip missing after open warmup: %s", raw)
}

func TestServerReloadStartsRuntimeAndListDoesNot(t *testing.T) {
	bin := buildExtSidecar(t)
	srv, hs := testServer(t)
	marker := filepath.Join(t.TempDir(), "sidecar-spawned")
	installSidecar(t, srv.cfg.Home, "mark", bin, `"lifecycle"`, `{"KI_MARKER":"`+strings.ReplaceAll(marker, `\`, `\\`)+`"}`)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	waitRuntimeReady(t, hs, id)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("create/GET should spawn: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	status, reload := postJSON(t, hs, http.MethodPost, "/v1/reload", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("reload %d %+v", status, reload)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("reload must start the server-level sidecar: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("GET /v1/sessions list spawned sidecar")
	}
	waitRuntimeReady(t, hs, id)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("GET by id should reuse the process-global sidecar")
	}
}

func TestRuntimeReadyNotification(t *testing.T) {
	bin := buildExtSidecar(t)
	srv, hs := testServer(t)
	installSidecar(t, srv.cfg.Home, "slow", bin, `"lifecycle"`, `{"KI_INIT_SLEEP_MS":"200"}`)
	status, created := postJSON(t, hs, http.MethodPost, "/v1/sessions", map[string]any{"cwd": t.TempDir()})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	id, _ := created["id"].(string)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events?notifications=1", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	buf := make([]byte, 4096)
	got := ""
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			got += string(buf[:n])
			if strings.Contains(got, "runtime_ready") {
				return
			}
		}
		if readErr != nil {
			t.Fatalf("no runtime_ready in %q: %v", got, readErr)
		}
	}
}
