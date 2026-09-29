package session

import (
	"testing"

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
