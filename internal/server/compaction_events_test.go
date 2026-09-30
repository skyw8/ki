package server

import (
	"encoding/json"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
)

func TestEmitterCompactionLifecycleIdentity(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	checkCompactionLifecyclePersistence(t, sess, func(ev loop.Event) loop.Event {
		t.Helper()
		if err := em.Emit(ev); err != nil {
			t.Fatal(err)
		}
		buffered := em.st.evs[len(em.st.evs)-1]
		var encoder loop.MessageEncoder
		raw, err := json.Marshal(encoder.Encode(*buffered))
		if err != nil {
			t.Fatal(err)
		}
		var wire loop.Event
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		return wire
	})
}

func TestStandaloneCompactionLifecycleIdentity(t *testing.T) {
	em, sess, hs := newEmitterForTest(t)
	events := pushEvents(t, hs, "tok")
	waitPush(t, events, "ready", func(ev pushEvent) bool { return ev.Type == "ready" })
	checkCompactionLifecyclePersistence(t, sess, func(ev loop.Event) loop.Event {
		t.Helper()
		if err := em.s.publishStandaloneCompactionEvent(t.Context(), sess, ev); err != nil {
			t.Fatal(err)
		}
		got := waitPush(t, events, string(ev.Type), func(got pushEvent) bool {
			return got.SessionID == sess.ID() && got.Type == ev.Type
		})
		return got.Event
	})
}

func checkCompactionLifecyclePersistence(t *testing.T, sess *session.Session, publish func(loop.Event) loop.Event) {
	t.Helper()
	parent := ""
	seen := map[string]bool{}
	check := func(ev loop.Event) {
		t.Helper()
		// Persistence, not an emitter's guesses, owns all three graph fields.
		wrongParent := "not-the-session-leaf"
		ev.ParentID, ev.Timestamp, ev.LifecycleEntryID = &wrongParent, -1, "not-persisted"
		got := publish(ev)
		if got.Type != ev.Type || got.EntryID != ev.EntryID || got.Status != ev.Status {
			t.Fatalf("lifecycle changed checkpoint/status: got %+v, want %+v", got, ev)
		}
		if got.LifecycleEntryID == "" || got.LifecycleEntryID == ev.LifecycleEntryID ||
			got.LifecycleEntryID == got.EntryID || seen[got.LifecycleEntryID] {
			t.Fatalf("missing or reused lifecycle identity: %+v", got)
		}
		if got.ParentID == nil || *got.ParentID != parent {
			t.Fatalf("lifecycle parent = %v, want %q (including explicit root)", got.ParentID, parent)
		}
		entries, err := session.AllEntries(sess.Dir)
		if err != nil {
			t.Fatal(err)
		}
		persisted := entries[len(entries)-1]
		if persisted.ID != got.LifecycleEntryID || persisted.ParentID != parent || persisted.Type != string(got.Type) {
			t.Fatalf("wire does not match persisted lifecycle: event %+v, entry %+v", got, persisted)
		}
		stamp, err := time.Parse(time.RFC3339Nano, persisted.Timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if got.Timestamp <= 0 || got.Timestamp != stamp.UnixMilli() {
			t.Fatalf("wire timestamp %d does not match persisted %q", got.Timestamp, persisted.Timestamp)
		}
		details, ok := persisted.Details.(map[string]any)
		if !ok || details["entryId"] != ev.EntryID || details["status"] != ev.Status || details["reason"] != ev.Reason {
			t.Fatalf("persisted lifecycle lost checkpoint/status: %+v", persisted)
		}
		seen[got.LifecycleEntryID] = true
		parent = got.LifecycleEntryID
	}
	for _, status := range []string{"committed", "empty", "failed", "cancelled"} {
		check(loop.Event{Type: loop.CompactionStart, Reason: "manual"})
		checkpointID := ""
		if status == "committed" {
			checkpoint, err := sess.AppendCompaction("summary", "", 100, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			checkpointID, parent = checkpoint.ID, checkpoint.ID
		}
		check(loop.Event{
			Type: loop.CompactionEnd, Reason: "manual", Status: status,
			OK: status == "committed" || status == "empty", EntryID: checkpointID,
		})
	}
}
