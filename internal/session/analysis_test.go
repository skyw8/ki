package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/types"
)

func TestTraceCacheMissesResetAtCompaction(t *testing.T) {
	usage := func(input, read int) *types.Usage { return &types.Usage{Input: input, CacheRead: read} }
	entries := []Entry{
		{Type: "message", ID: "a1", Message: &types.Message{Role: "assistant", Usage: usage(30_000, 0)}},
		{Type: "message", ID: "a2", ParentID: "a1", Message: &types.Message{Role: "assistant", Usage: usage(100, 29_800)}},
		{Type: "message", ID: "a3", ParentID: "a2", Message: &types.Message{Role: "assistant", Usage: usage(30_000, 0)}},
		{Type: "compaction", ID: "c1", ParentID: "a3", Summary: "summary"},
		{Type: "message", ID: "a4", ParentID: "c1", Message: &types.Message{Role: "assistant", Usage: usage(20_000, 0)}},
	}
	rows := Trace(entries, "a4", TraceFilter{CacheMisses: true})
	if len(rows) != 1 || rows[0].ID != "a3" {
		t.Fatalf("cache misses = %+v, want only a3", rows)
	}
	if rows[0].CacheMiss.MissedTokens != 29_900 {
		t.Fatalf("missed tokens = %d", rows[0].CacheMiss.MissedTokens)
	}
}

func TestClassifyCacheMissThresholds(t *testing.T) {
	cases := []struct {
		name          string
		prev          int
		reported      bool
		usage         *types.Usage
		wantMiss      int
		wantMissRatio float64
	}{
		{name: "first request", usage: &types.Usage{Input: 30_000}},
		{name: "provider never reports cache", prev: 30_000, usage: &types.Usage{Input: 30_000}},
		{name: "full cache hit", prev: 30_000, reported: true, usage: &types.Usage{Input: 1, CacheRead: 30_000}},
		{name: "appended content", prev: 10_000, reported: true, usage: &types.Usage{Input: 10_000, CacheRead: 10_000}},
		{name: "below noise floor", prev: 10_000, reported: true, usage: &types.Usage{Input: 1_024, CacheRead: 8_976}},
		{name: "below both notice gates", prev: 30_000, reported: true, usage: &types.Usage{Input: 22_000, CacheRead: 20_000}},
		{name: "ratio notice gate", prev: 10_000, reported: true, usage: &types.Usage{Input: 6_000, CacheRead: 4_000}, wantMiss: 6_000, wantMissRatio: 0.6},
		{name: "token notice gate", prev: 30_000, reported: true, usage: &types.Usage{Input: 30_000}, wantMiss: 30_000, wantMissRatio: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyCacheMiss(tt.prev, tt.reported, tt.usage)
			if tt.wantMiss == 0 {
				if got != nil {
					t.Fatalf("unexpected miss: %+v", got)
				}
				return
			}
			if got == nil || got.MissedTokens != tt.wantMiss || got.MissRatio != tt.wantMissRatio {
				t.Fatalf("miss = %+v, want tokens=%d ratio=%v", got, tt.wantMiss, tt.wantMissRatio)
			}
		})
	}
}

func TestCacheMissProjectionUsesFullActiveLeafAndStaysViewOnly(t *testing.T) {
	usage := func(input, read int) *types.Usage { return &types.Usage{Input: input, CacheRead: read} }
	entries := []Entry{
		{Type: "message", ID: "prior", Message: &types.Message{Role: "assistant", Usage: usage(100, 29_900)}},
		{Type: "message", ID: "miss", ParentID: "prior", Message: &types.Message{Role: "assistant", Usage: usage(30_000, 0)}},
		{Type: "message", ID: "sibling", ParentID: "prior", Message: &types.Message{Role: "assistant", Usage: usage(40_000, 0)}},
		{Type: "compaction", ID: "compact", ParentID: "miss"},
		{Type: "message", ID: "after", ParentID: "compact", Message: &types.Message{Role: "assistant", Usage: usage(30_000, 0)}},
	}
	misses := CacheMissesByID(entries, "after")
	if got := misses["miss"]; got == nil || got.MissedTokens != 30_000 || got.MissRatio != 1 {
		t.Fatalf("active-branch miss = %+v", got)
	}
	if misses["sibling"] != nil || misses["after"] != nil {
		t.Fatalf("sibling or post-compaction entry was classified: %+v", misses)
	}
	page := WithCacheMisses([]Entry{entries[1]}, entries, "after")
	if len(page) != 1 || page[0].CacheMiss == nil || page[0].CacheMiss.MissedTokens != 30_000 {
		t.Fatalf("sparse page missed annotation: %+v", page)
	}
	if entries[1].CacheMiss != nil {
		t.Fatal("view decoration mutated the source transcript entry")
	}

	sess, err := Create(t.TempDir(), t.TempDir(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	prior, err := sess.AppendMessage(types.Message{Role: "assistant", Usage: usage(100, 29_900)})
	if err != nil {
		t.Fatal(err)
	}
	current, err := sess.AppendMessage(types.Message{Role: "assistant", Usage: usage(30_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	view := WithCacheMisses([]Entry{current}, sess.Entries(), sess.LeafID())
	if view[0].CacheMiss == nil {
		t.Fatalf("view miss not derived from persisted usage after %s", prior.ID)
	}
	parent := sess.LeafID()
	sess.mu.Lock()
	err = sess.appendLocked(Entry{
		Type: "message", ID: "view-only", ParentID: parent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   &types.Message{Role: "assistant"},
		CacheMiss: &types.CacheMiss{MissedTokens: 30_000, MissRatio: 1},
	})
	sess.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	parent = sess.LeafID()
	sess.mu.Lock()
	err = sess.appendPreparedLocked(Entry{
		Type: "compaction", ID: "prepared-view-only", ParentID: parent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CacheMiss: &types.CacheMiss{MissedTokens: 30_000, MissRatio: 1},
	}, parent)
	sess.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(sess.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"cacheMiss"`) {
		t.Fatal("derived cache miss was persisted into events.jsonl")
	}
}

func TestAnalyzePromptChangesAndToolFailures(t *testing.T) {
	entries := []Entry{
		{Type: "request_header", ID: "h1", System: "one", Tools: []ToolSchema{{Name: "Read"}}},
		{Type: "message", ID: "a1", ParentID: "h1", Message: &types.Message{
			Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "call", Name: "Read"}},
		}},
		{Type: "message", ID: "r1", ParentID: "a1", Message: &types.Message{
			Role: "toolResult", ToolCallID: "call", ToolName: "Read", IsError: true,
			Content: []types.Content{{Type: "text", Text: "failed"}},
		}},
		{Type: "request_header", ID: "h2", ParentID: "r1", System: "two", Tools: []ToolSchema{{Name: "Read"}}},
	}
	got := Analyze(entries, "h2")
	if got.ToolCalls != 1 || len(got.ToolFailures) != 1 {
		t.Fatalf("tool analysis = %+v", got)
	}
	if len(got.PromptChanges) != 2 {
		t.Fatalf("prompt changes = %+v", got.PromptChanges)
	}
}

func TestRuntimeTraceDoesNotChangeModelBranchAndTimingUsesIntervalUnions(t *testing.T) {
	message := func(id, parent, role, name string, stamp, duration int64) Entry {
		return Entry{Type: "message", ID: id, ParentID: parent, Timestamp: time.UnixMilli(stamp).UTC().Format(time.RFC3339Nano), Message: &types.Message{Role: role, ToolName: name, DurationMs: duration}}
	}
	entries := []Entry{
		message("u", "", "user", "", 1000, 0),
		{Type: "agent_updated", ID: "progress", Sideband: true, Details: map[string]any{"agent": map[string]any{"agent_id": "a", "generation": 2, "phase": "waiting_message"}}},
		message("r1", "u", "toolResult", "read", 2000, 800),
		message("r2", "r1", "toolResult", "exec_command", 2500, 1000),
		message("wait", "r2", "toolResult", "WaitAgent", 3000, 500),
		message("answer", "wait", "assistant", "", 3500, 0),
	}
	analysis := Analyze(entries, "answer")
	if analysis.Timing.ToolMs != 1300 || analysis.Timing.MessageWaitMs != 500 || analysis.Timing.ElapsedMs != 2500 || analysis.Timing.UnknownMs != 700 {
		t.Fatalf("parallel durations added: %+v", analysis.Timing)
	}
	if len(analysis.Runtime) != 1 || len(LeafChain(entries, "answer")) != 5 {
		t.Fatalf("runtime changed branch: %+v", analysis)
	}
	trace := Trace(entries, "answer", TraceFilter{Types: []string{"agent_updated"}})
	if len(trace) != 1 || trace[0].Runtime == nil || !trace[0].Sideband {
		t.Fatalf("runtime trace %+v", trace)
	}
}
