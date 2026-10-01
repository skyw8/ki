package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

func TestRuntimeFieldsSkipTranscriptDecodeAndTurnStillHydrates(t *testing.T) {
	s, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	dir, _ := s.sidx.Lookup(id)
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("invalid history record\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	runtime := sessionGETFields(t, hs, id, "runtime")
	if _, ok := runtime["entries"]; ok {
		t.Fatal("runtime fields decoded the transcript")
	}

	id = createSession(t, hs, t.TempDir())
	dir, _ = s.sidx.Lookup(id)
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	user, err := sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "question"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "full turn answer"}}}); err != nil {
		t.Fatal(err)
	}
	got := sessionGETURL(t, hs, "/v1/sessions/"+id+"?turn="+user.ID+"&fields=runtime")
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), "full turn answer") {
		t.Fatal("explicit turn was mistaken for runtime-only fields")
	}
}

func TestCompletedReplayBudgetAndAttachedReader(t *testing.T) {
	s, hs := testServer(t)
	s.replay.limit = 12 << 10
	finish := func(id string) *runState {
		st, _, err := s.occupy(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: strings.Repeat("reply", 800)}}}
		st.mu.Lock()
		st.appendLocked(&loop.Event{Type: loop.MessageEnd, Message: &m})
		st.mu.Unlock()
		s.release(id, st)
		return st
	}
	a := createSession(t, hs, t.TempDir())
	b := createSession(t, hs, t.TempDir())
	first := finish(a)
	reader := &runReader{}
	first.mu.Lock()
	first.addReader(reader)
	first.mu.Unlock()
	_ = finish(b)
	s.mu.Lock()
	if s.replay.bytes > s.replay.limit || s.runs[a] != nil || s.runs[b] == nil {
		t.Fatalf("budget: bytes=%d runs=%v", s.replay.bytes, s.runs)
	}
	s.mu.Unlock()
	first.mu.Lock()
	if first.evs[0].Message.Text() != strings.Repeat("reply", 800) {
		t.Fatal("eviction cleared an attached reader's output")
	}
	first.removeReader(reader)
	first.mu.Unlock()
	res, err := openEventsRaw(hs, a)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("evicted replay status=%d", res.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "replay_unavailable" {
		t.Fatal(body)
	}
}

func TestCompletedReplayExpiresWithoutRequestsAndPreservesReplacement(t *testing.T) {
	s, hs := testServer(t)
	s.replay.ttl = 30 * time.Millisecond
	id := createSession(t, hs, t.TempDir())
	st, _, err := s.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s.release(id, st)
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		expired := s.runs[id] == nil && s.replay.bytes == 0
		s.mu.Unlock()
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle replay never expired")
		}
		time.Sleep(time.Millisecond)
	}
	old, _, err := s.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s.release(id, old)
	live, _, err := s.occupy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.pruneReplaysLocked(time.Now().Add(time.Hour))
	if s.runs[id] != live || s.replay.bytes != 0 {
		t.Fatal("old deadline evicted a live replacement")
	}
	s.mu.Unlock()
	s.release(id, live)
}

func TestOversizedReplayRecoversDurableBodies(t *testing.T) {
	s, hs := testServer(t)
	s.replay.limit = 1
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "reply with durable output")
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		missing := s.runs[id] == nil
		s.mu.Unlock()
		if missing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("oversized replay remained cached")
		}
		time.Sleep(time.Millisecond)
	}
	res, err := openEventsRaw(hs, id)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatal(res.StatusCode)
	}
	got := sessionGET(t, hs, id)
	entries, _ := got["entries"].([]any)
	if got["running"] != false || len(entries) == 0 {
		t.Fatal("durable snapshot lost terminal work")
	}
}
