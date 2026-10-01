package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"ki/internal/types"
)

func TestBoundedPagesRemainContiguous(t *testing.T) {
	entries := []Entry{}
	parent := ""
	for i := 0; i < 80; i++ {
		e := Entry{ID: fmt.Sprintf("e%d", i), ParentID: parent, Type: "request_header", System: fmt.Sprintf("%d ", i) + strings.Repeat("中", 19_000)}
		entries = append(entries, e)
		parent = e.ID
	}
	page := BuildView(entries, parent, 100)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if count > len(entries) {
			t.Fatal("paging did not advance")
		}
		raw, _ := json.Marshal(page.Entries)
		if len(raw) > MaxViewPageBytes {
			t.Fatalf("page bytes %d", len(raw))
		}
		for _, e := range page.Entries {
			seen[e.ID] = true
		}
		if !page.HasMore {
			break
		}
		before := page.OldestID
		page = BuildBefore(entries, parent, before, 100)
		if page.OldestID == before || len(page.Entries) == 0 {
			t.Fatal("stalled cursor")
		}
	}
	if len(seen) != len(entries) {
		t.Fatalf("lost entries: %d/%d", len(seen), len(entries))
	}
}

// A tail-first read is a byte window of the file, not the whole branch.
// Reaching the window's oldest entry does not mean the branch root is loaded,
// so HasMore must stay true or the reader can never page back to the entries
// before the window (the "beginning of conversation" the UI then shows).
func TestIncompleteTailWindowKeepsHasMore(t *testing.T) {
	entries := []Entry{}
	parent := ""
	for i := 0; i < 40; i++ {
		e := Entry{ID: fmt.Sprintf("e%d", i), ParentID: parent, Type: "message"}
		e.Message = &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: fmt.Sprintf("turn %d", i)}}}
		entries = append(entries, e)
		parent = e.ID
	}
	// The window is exactly the page limit, so tailStart == 0.
	window := entries[10:]
	tail := BuildTail(window, parent, 30, false)
	if !tail.HasMore {
		t.Fatal("an incomplete window must still report older history")
	}
	if tail.OldestID != window[0].ID {
		t.Fatalf("cursor %q, want %q", tail.OldestID, window[0].ID)
	}
	// The same window read to the branch root genuinely has nothing older.
	if BuildTail(entries, parent, 40, true).HasMore {
		t.Fatal("a complete window of the same length must not report older history")
	}
}

func TestOversizedViewEntryKeepsUTF8AndHydrationIdentity(t *testing.T) {
	e := Entry{Type: "message", ID: "huge", Message: &types.Message{Role: "toolResult", ToolCallID: "call", Content: []types.Content{{Type: "text", Text: strings.Repeat("中🙂", 20_000)}}}}
	view := BuildTail([]Entry{e}, e.ID, 100, true)
	got := view.Entries[0]
	if !got.Truncated || got.ID != e.ID || got.Message.ToolCallID != "call" {
		t.Fatalf("missing preview identity: %+v", got)
	}
	if !utf8.ValidString(got.Message.Content[0].Text) {
		t.Fatal("split UTF-8")
	}
	if len(got.Message.Content[0].Text) > toolPreviewBytes {
		t.Fatal("unbounded collapsed tool result")
	}
	if len(e.Message.Content[0].Text) != 140_000 {
		t.Fatal("mutated persisted body")
	}
}

// indexOf must keep durationMs for tool results that finished in under a
// millisecond: the WebUI reads the index to render per-tool timing.
func TestIndexEntryKeepsZeroDuration(t *testing.T) {
	ix := indexOf(Entry{Type: "message", ID: "e1", Message: &types.Message{
		Role:       "toolResult",
		ToolCallID: "c1",
		ToolName:   "Read",
		DurationMs: 0,
	}})
	raw, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["durationMs"]; !ok {
		t.Fatalf("index entry dropped zero durationMs: %s", raw)
	}
}

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
	for i := range 8 {
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

func TestLookupEntriesRedactsRemoteCompaction(t *testing.T) {
	entries := []Entry{{
		Type: "compaction", ID: "cmp",
		Responses: &types.ResponsesContext{
			Binding: types.ProviderBinding{Provider: "openai", API: "responses", BaseURL: "https://api.openai.com/v1", Model: "gpt"},
			Items:   []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"secret"}`)},
		},
	}}
	got := LookupEntries(entries, []string{"cmp"})
	if len(got) != 1 || got[0].Responses != nil || !got[0].RemoteContext {
		t.Fatalf("exact lookup leaked remote checkpoint: %+v", got)
	}
}

func TestRemoteContextPublicProjections(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	user, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "input"}}})
	if err != nil {
		t.Fatal(err)
	}
	binding := types.ProviderBinding{Provider: "openai", API: "responses", BaseURL: "https://api.openai.com/v1", Model: "gpt", Compaction: "openai"}
	remote, err := s.AppendResponsesCompaction(types.ResponsesContext{
		Binding: binding,
		Items:   []json.RawMessage{json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"CHECKPOINT_SECRET"}`)},
	}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	end, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "reply"}}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := AllEntries(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ReadTranscript(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	assertEntries := func(name string, got []Entry) {
		t.Helper()
		found := false
		for _, e := range got {
			if e.Responses != nil {
				t.Fatalf("%s exposes provider payload", name)
			}
			if e.ID == remote.ID {
				found = true
				if !e.RemoteContext {
					t.Fatalf("%s lost remote marker", name)
				}
			} else if e.RemoteContext {
				t.Fatalf("%s marked ordinary entry as remote: %s", name, e.ID)
			}
		}
		raw, err := json.Marshal(got)
		if err != nil || !found || strings.Contains(string(raw), "CHECKPOINT_SECRET") || strings.Contains(string(raw), `"responses":`) {
			t.Fatalf("%s invalid public projection: found=%v err=%v body=%s", name, found, err, raw)
		}
	}
	assertIndex := func(name string, got []IndexEntry) {
		t.Helper()
		found := false
		for _, e := range got {
			if e.ID == remote.ID {
				found = true
				if !e.RemoteContext || e.Preview != "Provider remote compaction" {
					t.Fatalf("%s lost remote kind: %+v", name, e)
				}
			} else if e.RemoteContext {
				t.Fatalf("%s marked ordinary entry as remote: %s", name, e.ID)
			}
		}
		raw, err := json.Marshal(got)
		if err != nil || !found || strings.Contains(string(raw), "CHECKPOINT_SECRET") || strings.Contains(string(raw), `"responses":`) {
			t.Fatalf("%s invalid public index: %s (%v)", name, raw, err)
		}
	}
	assertEntries("tail", BuildTail(entries, end.ID, 100, true).Entries)
	view := BuildView(entries, end.ID, 100)
	assertEntries("view", view.Entries)
	assertIndex("view index", view.Index)
	assertEntries("before", BuildBefore(entries, end.ID, end.ID, 100).Entries)
	assertEntries("lookup", LookupEntries(entries, []string{remote.ID}))
	redacted := RedactProviderContext(entries)
	assertEntries("redaction", redacted)
	assertEntries("repeated redaction", RedactProviderContext(redacted))
	assertIndex("redacted index", BuildIndex(redacted))
	assertEntries("compact", BuildCompact(entries, end.ID, "", 1).Entries)
	assertEntries("redacted compact", BuildCompact(redacted, end.ID, "", 1).Entries)
	turn, ok := BuildTurn(entries, end.ID, user.ID, "", 100)
	if !ok {
		t.Fatal("turn not found")
	}
	assertEntries("turn", turn.Entries)
	compactTurn, ok := BuildCompactTurn(entries, end.ID, user.ID, 1)
	if !ok {
		t.Fatal("compact turn not found")
	}
	assertEntries("compact turn", compactTurn.Entries)
	assertIndex("transcript index", tr.Index())
	exact, err := tr.Lookup([]string{remote.ID})
	if err != nil {
		t.Fatal(err)
	}
	assertEntries("transcript exact lookup", exact)
	tail, ok, err := tr.Tail(end.ID, "", "", 100)
	if err != nil || !ok {
		t.Fatalf("transcript tail: %v, %v", ok, err)
	}
	assertEntries("transcript tail", tail.Entries)
	for _, turnID := range []string{"", user.ID} {
		page, ok, err := tr.Compact(end.ID, "", turnID, 1)
		if err != nil || !ok {
			t.Fatalf("transcript compact: %v, %v", ok, err)
		}
		assertEntries("transcript compact "+turnID, page.Entries)
	}
	for _, e := range entries {
		if e.RemoteContext {
			t.Fatal("public projection changed persisted entries")
		}
	}
	raw, err := json.Marshal(remote)
	if err != nil || strings.Contains(string(raw), `"remoteContext":`) || !strings.Contains(string(raw), "CHECKPOINT_SECRET") {
		t.Fatalf("persisted checkpoint changed: %s (%v)", raw, err)
	}
	if ctx := s.ContextToLeaf(binding); ctx.Responses == nil || len(ctx.Responses.Items) != 1 {
		t.Fatal("public projection changed provider replay")
	}
}
