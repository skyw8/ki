package session

import (
	"strings"
	"testing"

	"ki/internal/types"
)

func TestBuildViewOmitsUnchangedPromptAndWindowsTail(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	tools := []ToolSchema{{Name: "Read", Description: "read"}}
	if _, err := s.AppendRequestHeader("sys-a", tools); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "one"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRequestHeader("sys-a", tools); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "two"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRequestHeader("sys-b", tools); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "three"}}}); err != nil {
		t.Fatal(err)
	}

	view := BuildView(s.Entries(), s.LeafID(), 100)
	if len(view.Index) != len(s.Entries()) {
		t.Fatalf("index %d want %d", len(view.Index), len(s.Entries()))
	}
	var headers []Entry
	for _, e := range view.Entries {
		if e.Type == "request_header" {
			headers = append(headers, e)
		}
	}
	if len(headers) != 3 {
		t.Fatalf("headers %d: %+v", len(headers), headers)
	}
	if headers[0].System != "sys-a" || headers[0].PromptUnchanged {
		t.Fatalf("first header: %+v", headers[0])
	}
	if !headers[1].PromptUnchanged || headers[1].System != "" || headers[1].Tools != nil {
		t.Fatalf("unchanged header kept body: %+v", headers[1])
	}
	if headers[2].System != "sys-b" || headers[2].PromptUnchanged {
		t.Fatalf("changed header: %+v", headers[2])
	}
}

func TestBuildViewTruncatesLargeToolResult(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "read"}}}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", MaxViewBytes+80)
	if _, err := s.AppendMessage(types.Message{Role: "toolResult", ToolCallID: "c1", ToolName: "Read", Content: []types.Content{{Type: "text", Text: big}}}); err != nil {
		t.Fatal(err)
	}
	view := BuildView(s.Entries(), s.LeafID(), 100)
	var found bool
	for _, e := range view.Entries {
		if e.Type != "message" || e.Message == nil || e.Message.Role != "toolResult" {
			continue
		}
		found = true
		if !e.Truncated {
			t.Fatal("expected truncated tool result")
		}
		if got := e.Message.Text(); len(got) > MaxViewBytes {
			t.Fatalf("truncated len %d", len(got))
		}
	}
	if !found {
		t.Fatal("missing tool result")
	}
	full, ok := s.Lookup(s.LeafID())
	if !ok || full.Truncated || len(full.Message.Text()) <= MaxViewBytes {
		t.Fatalf("persisted entry must stay full: truncated=%v", full.Truncated)
	}
}

func TestBuildViewWindowsLeafTail(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < 8; i++ {
		if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "u" + strings.Repeat(".", i)}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "a"}}}); err != nil {
			t.Fatal(err)
		}
	}
	view := BuildView(s.Entries(), s.LeafID(), 4)
	if !view.HasMore {
		t.Fatal("expected hasMore")
	}
	if len(view.Entries) != 4 {
		t.Fatalf("tail %d: %+v", len(view.Entries), view.Entries)
	}
	if view.OldestID == "" || view.OldestID != view.Entries[0].ID {
		t.Fatalf("oldest %q entries[0]=%q", view.OldestID, view.Entries[0].ID)
	}
	older := BuildBefore(s.Entries(), s.LeafID(), view.OldestID, 20)
	if older.HasMore {
		t.Fatalf("older should drain: %+v", older)
	}
	if len(older.Entries) == 0 {
		t.Fatal("expected older entries")
	}
	for _, e := range older.Entries {
		if e.ID == view.OldestID {
			t.Fatal("before window includes the cursor")
		}
	}
}

func TestLookupEntriesPreservesOrderAndCap(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	u, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "u"}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := LookupEntries(s.Entries(), []string{a.ID, "missing", u.ID})
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != u.ID {
		t.Fatalf("%+v", got)
	}
}
