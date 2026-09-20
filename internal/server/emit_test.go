package server

import (
	"sync"
	"testing"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

// newEmitterForTest builds a funnel over a fresh session. Why: the funnel was
// split out of runPrompt so its stages can be asserted directly — driving a
// whole loop.Run just to observe a jsonl shape is what the split removed.
func newEmitterForTest(t *testing.T) (*runEmitter, *session.Session) {
	t.Helper()
	srv, _ := testServer(t)
	sess, err := session.CreateWithOptions(srv.cfg.Sessions.Root, t.TempDir(), "p", "m", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	st := &runState{runID: "run-1", done: make(chan struct{})}
	st.wait = sync.NewCond(&st.mu)
	em := &runEmitter{
		s: srv, ctx: t.Context(), id: sess.ID(), sess: sess, st: st,
		info: provider.Model{ID: "m", ContextWindow: 1000},
	}
	return em, sess
}

// TestEmitterPersistAndBuffer covers the two stages a request_header goes
// through: the jsonl entry plus the context meter entry, and the SSE replay
// buffer that must also carry the synthesized context_usage event.
func TestEmitterPersistAndBuffer(t *testing.T) {
	em, sess := newEmitterForTest(t)
	ev := loop.Event{Type: loop.RequestHeader, System: "sys", Tools: []loop.ToolSpec{{Type: "function", Name: "Read"}}}
	if err := em.Emit(ev); err != nil {
		t.Fatal(err)
	}
	entries, err := session.AllEntries(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var header, usage *session.Entry
	for i := range entries {
		switch entries[i].Type {
		case "request_header":
			header = &entries[i]
		case "context_usage":
			usage = &entries[i]
		}
	}
	if header == nil || header.System != "sys" || len(header.Tools) != 1 || header.Tools[0].Name != "Read" {
		t.Fatalf("request_header entry: %+v", header)
	}
	if usage == nil || usage.ContextWindow != 1000 || usage.UsedTokens <= 0 {
		t.Fatalf("context_usage entry: %+v", usage)
	}
	if len(em.st.evs) != 2 || em.st.evs[0].Type != loop.RequestHeader || em.st.evs[1].Type != loop.ContextUsage {
		t.Fatalf("buffered events: %+v", em.st.evs)
	}
	for _, buffered := range em.st.evs {
		if buffered.RunID != "run-1" {
			t.Fatalf("buffered event without run id: %+v", buffered)
		}
	}
}

// TestEmitterIdempotencyKeyConsumedOnce pins the state that used to live in a
// captured closure variable: only the first user message_end spends the key,
// and the message_end event carries the appended entry id.
func TestEmitterIdempotencyKeyConsumedOnce(t *testing.T) {
	em, sess := newEmitterForTest(t)
	em.idempotencyKey = "key-1"
	assistant := loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "hello"}}}}
	if err := em.persist(&assistant); err != nil {
		t.Fatal(err)
	}
	if em.idempotencyKey != "key-1" {
		t.Fatal("an assistant message_end spent the idempotency key")
	}
	user := loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "hi"}}}}
	if err := em.persist(&user); err != nil {
		t.Fatal(err)
	}
	if em.idempotencyKey != "" {
		t.Fatal("the user message_end did not spend the idempotency key")
	}
	if user.EntryID == "" {
		t.Fatal("message_end did not carry the appended entry id")
	}
	entries, err := session.AllEntries(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].IdempotencyKey != "" || entries[1].IdempotencyKey != "key-1" {
		t.Fatalf("entries: %+v", entries)
	}
}
