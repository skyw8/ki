package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ki/internal/loop"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	"ki/internal/types"
)

func TestEmitterNestedAuditPersistsWithoutTranscriptMessages(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	for _, typ := range []loop.EventType{loop.ToolExecutionStart, loop.ToolExecutionUpdate, loop.ToolExecutionEnd} {
		ev := loop.Event{
			Type: typ, ToolCallID: "child", ParentCallID: "outer", CellID: "cell",
			ToolName: "read", RequestedToolName: "Read", Timestamp: 123, DurationMs: 2,
			Args: map[string]any{"file_path": "a"}, PartialResult: "progress",
			Result: toolapi.Result{
				Content: []types.Content{{Type: "text", Text: "result"}},
				Details: map[string]any{"oversized": strings.Repeat("x", 128<<10)},
			},
		}
		if err := em.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := session.AllEntries(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || len(sess.MessagesToLeaf()) != 0 {
		t.Fatalf("nested events became model messages: %+v", entries)
	}
	for i, entry := range entries {
		details := entry.Details.(map[string]any)
		if details["toolCallId"] != "child" || details["parentCallId"] != "outer" ||
			details["cellId"] != "cell" || details["toolName"] != "read" || details["requestedToolName"] != "Read" ||
			details["timestamp"] != float64(123) || details["durationMs"] != float64(2) {
			t.Fatalf("lost persisted attribution: %#v", details)
		}
		ev := em.st.evs[i]
		if ev.EntryID != entry.ID || ev.ParentID == nil || *ev.ParentID != entry.ParentID || ev.ParentCallID != "outer" || ev.CellID != "cell" {
			t.Fatalf("SSE/persistence attribution mismatch: %+v, %+v", ev, entry)
		}
		encoded, err := json.Marshal(entry)
		if err != nil || len(encoded) > 64<<10 {
			t.Fatalf("unbounded nested audit: bytes=%d err=%v", len(encoded), err)
		}
	}
}

func TestEmitterSerializesConcurrentNestedCallbacks(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	const calls = 8
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			id := fmt.Sprintf("child-%d", i)
			for _, typ := range []loop.EventType{loop.ToolExecutionStart, loop.ToolExecutionEnd} {
				if err := em.Emit(loop.Event{
					Type: typ, ToolCallID: id, ParentCallID: "outer", CellID: "cell", ToolName: "read", RequestedToolName: "Read",
				}); err != nil {
					t.Errorf("concurrent emit: %v", err)
				}
			}
		})
	}
	wg.Wait()
	entries, err := session.AllEntries(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != calls*2 || len(em.st.evs) != calls*2 {
		t.Fatalf("missing audits: entries=%d buffered=%d", len(entries), len(em.st.evs))
	}
	for i, entry := range entries {
		if ev := em.st.evs[i]; ev.Seq != int64(i+1) || ev.EntryID != entry.ID {
			t.Fatalf("persist/buffer ordering diverged at %d: %+v, %+v", i, entry, ev)
		}
	}
}
