package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

// newEmitterForTest builds a funnel over a fresh session. Why: the funnel was
// split out of runPrompt so its stages can be asserted directly — driving a
// whole loop.Run just to observe a jsonl shape is what the split removed. hs is
// the httptest server backing srv, for the stages that reach a subscriber over
// HTTP (the WebUI push stream).
func newEmitterForTest(t *testing.T) (*runEmitter, *session.Session, *httptest.Server) {
	t.Helper()
	srv, hs := testServer(t)
	sess, err := session.CreateWithOptions(srv.cfg.Sessions.Root, t.TempDir(), "p", "m", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	st := &runState{runID: "run-1", done: make(chan struct{}), partial: -1}
	st.wait = sync.NewCond(&st.mu)
	em := &runEmitter{
		s: srv, ctx: t.Context(), id: sess.ID(), sess: sess, st: st,
		info: provider.Model{ID: "m", ContextWindow: 1000},
	}
	return em, sess, hs
}

// TestEmitterPersistAndBuffer covers the two stages a request_header goes
// through: the jsonl entry plus the context meter entry, and the SSE replay
// buffer that must also carry the synthesized context_usage event.
func TestEmitterPersistAndBuffer(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	ev := loop.Event{Type: loop.RequestHeader, System: "sys", Tools: []loop.ToolSpec{{Name: "Read"}}}
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
	em, sess, _ := newEmitterForTest(t)
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

// TestEmitterStampsIdentityOnCallerEvent covers the stage boundary a refactor
// broke: buffer used to take the event by value, so only the buffered copy got
// the run id and external metadata while the stages after it — the push
// completion frame and the extension lifecycle notification — saw an anonymous
// event. A channel connector drops a lifecycle event without a run id, so the
// symptom was a bot that stopped replying at all.
func TestEmitterStampsIdentityOnCallerEvent(t *testing.T) {
	em, _, hs := newEmitterForTest(t)
	em.st.external = map[string]string{"connector": "telegram-bot"}
	events := pushEvents(t, hs, "tok")
	if err := em.Emit(loop.Event{Type: loop.AgentEnd}); err != nil {
		t.Fatal(err)
	}
	got := waitPush(t, events, "agent_end frame", func(ev pushEvent) bool { return ev.Type == loop.AgentEnd })
	if got.RunID != "run-1" {
		t.Fatalf("agent_end frame without the run id: %+v", got)
	}
	if got.External["connector"] != "telegram-bot" {
		t.Fatalf("agent_end frame without external metadata: %+v", got)
	}
}

func TestEmitterMessageCarriesPersistedParentThroughWire(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	parent := ""
	for _, role := range []string{"user", "assistant", "toolResult"} {
		ev := loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: role}}
		if err := em.persist(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.ParentID == nil || *ev.ParentID != parent {
			t.Fatalf("%s parent = %v, want %q", role, ev.ParentID, parent)
		}
		var encoder loop.MessageEncoder
		raw, err := json.Marshal(encoder.Encode(ev))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if got, exists := wire["parentId"]; !exists || got != parent {
			t.Fatalf("wire lost parent (including root): %s", raw)
		}
		entry, ok := sess.Lookup(ev.EntryID)
		if !ok || entry.ParentID != parent {
			t.Fatal("wire does not match persisted edge")
		}
		parent = ev.EntryID
	}
}

func TestEmitterPromotesServerCompactionToPrivateCheckpoint(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	em.info = provider.Model{
		Provider: "openai", ID: "gpt", API: "responses",
		BaseURL: "https://api.openai.com/v1", ContextWindow: 1000,
	}
	em.binding = em.s.providerBinding(em.info)
	em.replayBinding = em.binding
	opaque := json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}`)
	answer := json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"after compact"}]}`)
	message := types.Message{
		Role:           "assistant",
		Content:        []types.Content{{Type: "text", Text: "after compact"}},
		ResponsesItems: []json.RawMessage{opaque, answer},
	}
	ev := loop.Event{Type: loop.MessageEnd, Message: &message}
	if err := em.Emit(ev); err != nil {
		t.Fatal(err)
	}
	// Persisting/buffering must not mutate the loop's in-memory assistant: its
	// next tool round still needs the item to prune the previous request.
	if len(message.ResponsesItems) != 2 {
		t.Fatal("emitter cleared live provider context")
	}
	entries := sess.Entries()
	var checkpoint *session.Entry
	for i := range entries {
		if entries[i].Type == "compaction" && entries[i].Responses != nil {
			checkpoint = &entries[i]
		}
		if entries[i].Message != nil && len(entries[i].Message.ResponsesItems) != 0 {
			t.Fatal("opaque context leaked into persisted message")
		}
	}
	if checkpoint == nil || len(checkpoint.Responses.Items) != 2 {
		t.Fatalf("checkpoint: %+v", checkpoint)
	}
	ctx := sess.ContextToLeaf(em.s.providerBinding(em.info))
	if ctx.Responses == nil || len(ctx.Responses.Items) != 2 || len(ctx.Messages) != 0 {
		t.Fatalf("projected context: %+v", ctx)
	}
	for _, buffered := range em.st.evs {
		if buffered.Message != nil && len(buffered.Message.ResponsesItems) != 0 {
			t.Fatal("opaque context leaked into buffered SSE")
		}
	}
}

func TestEmitterDoesNotPersistOpaqueCheckpointAcrossLifecyclePolicy(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	em.info = provider.Model{Provider: "openai", ID: "gpt", API: "responses", BaseURL: "https://api.openai.com/v1"}
	em.binding = em.s.providerBinding(em.info)
	em.replayBinding = types.ProviderBinding{}
	message := types.Message{
		Role: "assistant", Content: []types.Content{{Type: "text", Text: "visible"}},
		ResponsesItems: []json.RawMessage{
			json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`),
		},
	}
	if err := em.Emit(loop.Event{Type: loop.MessageEnd, Message: &message}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range sess.Entries() {
		if entry.Responses != nil {
			t.Fatal("opaque checkpoint persisted across message-visible lifecycle policy")
		}
	}
	if got := sess.MessagesToLeaf(); len(got) != 1 || got[0].Text() != "visible" {
		t.Fatalf("portable assistant missing: %+v", got)
	}
}

func TestStrictRemoteCompactionDoesNotFallBackForUnsupportedModel(t *testing.T) {
	srv, _ := testServer(t)
	srv.cfg.Compaction.Mode = "remote"
	sess, err := session.Create(srv.cfg.Sessions.Root, t.TempDir(), "openrouter", "free")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	for range 4 {
		_, _ = sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "history"}}})
	}
	_, err = srv.compactSession(t.Context(), sess, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support remote compaction") {
		t.Fatalf("strict remote error = %v", err)
	}
	for _, entry := range sess.Entries() {
		if entry.Type == "compaction" {
			t.Fatalf("strict remote wrote local fallback: %+v", entry)
		}
	}
}
