package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestToolMessageDetailsRoundTrip(t *testing.T) {
	cwd := t.TempDir()
	s, err := Create(t.TempDir(), cwd, "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendMessage(types.Message{Role: "toolResult", ToolCallID: "call", ToolName: "Edit", Content: []types.Content{{Type: "text", Text: "ok"}}, Details: map[string]any{"diff": "patch"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	_ = s.Close()
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	messages := reopened.MessagesToLeaf()
	details, ok := messages[len(messages)-1].Details.(map[string]any)
	if !ok || details["diff"] != "patch" {
		t.Fatalf("details = %#v", messages[len(messages)-1].Details)
	}
}

func TestSidebandEventDoesNotAdvanceConversationLeaf(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	message, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	event, err := AppendSidebandEvent(s.Dir, "mcp_tools_changed", map[string]any{"server": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if event.ParentID != "" || s.LeafID() != message.ID {
		t.Fatalf("sideband changed leaf: event=%+v leaf=%q", event, s.LeafID())
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "world"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if len(reopened.Entries()) != 3 {
		t.Fatalf("entries = %+v", reopened.Entries())
	}
}

func TestSidebandOnlySessionReopensWithoutConversationLeaf(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSidebandEvent(s.Dir, "mcp_server_failed", map[string]any{"server": "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.LeafID() != "" {
		t.Fatalf("sideband became conversation leaf: %q", reopened.LeafID())
	}
}

func TestCreateReloadFork(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{
		Role:    "user",
		Content: []types.Content{{Type: "text", Text: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{
		Role:    "assistant",
		Content: []types.Content{{Type: "text", Text: "hello"}},
		Usage:   &types.Usage{Input: 10, Output: 4, TotalTokens: 14},
	}); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// events.jsonl starts with session header
	//nolint:gosec // dir is the test's isolated session directory.
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("empty jsonl")
	}
	var hdr Header
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if hdr.Type != "session" || hdr.ID != id {
		t.Fatalf("header: %+v", hdr)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}

	found, err := Find(root, id)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(found)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if s2.LeafID() == "" {
		t.Fatal("leaf empty after reload")
	}
	msgs := s2.MessagesToLeaf()
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("messages: %+v", msgs)
	}
	if msgs[1].Usage == nil || msgs[1].Usage.Input != 10 {
		t.Fatalf("usage not persisted: %+v", msgs[1].Usage)
	}

	if err := s2.SetModelAndThinking("openai", "gpt-4o", "high"); err != nil {
		t.Fatal(err)
	}
	if s2.Config.Model != "gpt-4o" || s2.Config.ThinkingEffort != "high" {
		t.Fatalf("model: %+v", s2.Config)
	}

	forked, err := Fork(root, s2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forked.Close() }()
	if forked.ID() == s2.ID() {
		t.Fatal("fork should mint new id")
	}
	if forked.Header.ParentSession != s2.ID() {
		t.Fatalf("parentSession: %q", forked.Header.ParentSession)
	}
	if forked.Config.Provider != "openai" || forked.Config.Model != "gpt-4o" || forked.Config.ThinkingEffort != "high" {
		t.Fatalf("fork config: %+v", forked.Config)
	}
	if len(forked.MessagesToLeaf()) != 2 {
		t.Fatalf("fork messages: %d", len(forked.MessagesToLeaf()))
	}

	s3, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s3.Close() }()
	if s3.ID() == id {
		t.Fatal("second create must be a new session")
	}
}

func TestBranchLeafPersistsAndForkAtCopiesOnlyActivePath(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	attachmentDir := filepath.Join(s.Dir, "attachments")
	if err := os.MkdirAll(attachmentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	attachment := filepath.Join(attachmentDir, "blob.txt")
	if err := os.WriteFile(attachment, []byte("blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	u1, _ := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "one"}, {Type: "file", Path: attachment, Name: "blob.txt"}}})
	a1, _ := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "old"}}})
	if err := s.SetLeaf(u1.ID); err != nil {
		t.Fatal(err)
	}
	a2, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "new"}}})
	if err != nil {
		t.Fatal(err)
	}
	if a2.ParentID != u1.ID || a1.ParentID != u1.ID {
		t.Fatalf("siblings: old=%q new=%q user=%q", a1.ParentID, a2.ParentID, u1.ID)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.LeafID() != a2.ID {
		t.Fatalf("leaf after reload = %q, want %q", s.LeafID(), a2.ID)
	}
	msgs := s.MessagesToLeaf()
	if len(msgs) != 2 || msgs[1].Text() != "new" {
		t.Fatalf("active messages: %+v", msgs)
	}

	forked, err := ForkAt(root, s, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forked.Close() }()
	if forked.Dir == s.Dir || forked.Header.ParentSession != s.ID() {
		t.Fatalf("fork dir=%q parent=%q", forked.Dir, forked.Header.ParentSession)
	}
	if got := forked.MessagesToLeaf(); len(got) != 2 || got[1].Text() != "old" {
		t.Fatalf("fork messages: %+v", got)
	}
	if len(forked.Entries()) != 2 {
		t.Fatalf("fork copied sibling entries: %+v", forked.Entries())
	}
	path := forked.MessagesToLeaf()[0].Content[1].Path
	if filepath.Dir(filepath.Dir(path)) != forked.Dir {
		t.Fatalf("fork attachment path = %q, dir = %q", path, forked.Dir)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "blob" { //nolint:gosec // path is a fork attachment created under t.TempDir
		t.Fatalf("fork attachment = %q, %v", b, err)
	}
}

func TestRequestHeaderAndTogglesReload(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRequestHeader("sys-body", []ToolSchema{{
		Type: "custom", Name: "apply_patch", Description: "patch", Format: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ToolType: "custom", ID: "c1", Name: "apply_patch", Input: "PATCH"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "toolResult", ToolType: "custom", ToolCallID: "c1", ToolName: "apply_patch", Content: []types.Content{{Type: "text", Text: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	var hdr *Entry
	for i := range s2.Entries() {
		e := s2.Entries()[i]
		if e.Type == "request_header" {
			hdr = &e
		}
	}
	if hdr == nil || hdr.System != "sys-body" || len(hdr.Tools) != 1 || hdr.Tools[0].Name != "apply_patch" || hdr.Tools[0].Type != "custom" || hdr.Tools[0].Format == nil {
		t.Fatalf("header: %+v", hdr)
	}
	messages := s2.MessagesToLeaf()
	if len(messages) != 2 || messages[0].ToolCalls()[0].Input != "PATCH" || messages[1].ToolType != "custom" {
		t.Fatalf("custom tool messages: %+v", messages)
	}
}

func TestCompactionRetainedTailReload(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	m1 := types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "one"}}}
	m2 := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "two"}}}
	if _, err := s.AppendMessage(m1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(m2); err != nil {
		t.Fatal(err)
	}
	// Append a compaction whose retained tail duplicates the kept messages.
	tail := []types.Message{m2}
	if _, err := s.AppendCompaction("SUM", "", 10, &types.Usage{Input: 10, TotalTokens: 10}, tail); err != nil {
		t.Fatal(err)
	}
	// New-style: MessagesToLeaf uses the persisted retained tail verbatim.
	hist := s.MessagesToLeaf()
	if len(hist) != 2 || !strings.Contains(hist[0].Text(), "SUM") || hist[1].Text() != "two" {
		t.Fatalf("history: %+v", hist)
	}
	// Reload: retained tail survives jsonl round-trip.
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	var comp *Entry
	for i := range s2.Entries() {
		e := s2.Entries()[i]
		if e.Type == "compaction" {
			comp = &e
		}
	}
	if comp == nil || len(comp.RetainedTail) != 1 || comp.RetainedTail[0].Text() != "two" {
		t.Fatalf("compaction entry: %+v", comp)
	}
	if got := s2.MessagesToLeaf(); len(got) != 2 || got[1].Text() != "two" {
		t.Fatalf("reloaded history: %+v", got)
	}
	if s2.LastCompactionAt() <= 0 {
		t.Fatal("LastCompactionAt should return the compaction timestamp")
	}
}

func TestMessagesToLeafIncludesMessagesAfterCompaction(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := range 4 {
		_, _ = s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: fmt.Sprintf("u%d", i)}}})
		_, _ = s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: fmt.Sprintf("a%d", i)}}})
	}
	// Compact, keeping the last two messages as the retained tail.
	tail := []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "u1"}}},
		{Role: "assistant", Content: []types.Content{{Type: "text", Text: "a1"}}},
		{Role: "user", Content: []types.Content{{Type: "text", Text: "u2"}}},
		{Role: "assistant", Content: []types.Content{{Type: "text", Text: "a2"}}},
	}
	if _, err := s.AppendCompaction("SUM", "", 10, &types.Usage{Input: 10, TotalTokens: 10}, tail); err != nil {
		t.Fatal(err)
	}
	// Messages sent AFTER the compaction must still reach the model-facing
	// context; the retained tail is the head, not a terminator.
	_, _ = s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "u3"}}})
	_, _ = s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "a3"}}})
	hist := s.MessagesToLeaf()
	if len(hist) != 7 {
		t.Fatalf("want summary+4 tail+2 new = 7 messages, got %d: %+v", len(hist), hist)
	}
	if !strings.Contains(hist[0].Text(), "SUM") || hist[len(hist)-1].Text() != "a3" {
		t.Fatalf("history order: first=%q last=%q", hist[0].Text(), hist[len(hist)-1].Text())
	}
}

func TestCompactionRetainedTailFallback(t *testing.T) {
	// Old jsonl without retainedTail falls back to FirstKeptEntryID slicing.
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var first string
	for i := range 3 {
		e, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "u" + strconv.Itoa(i)}}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			first = e.ID
		}
	}
	if _, err := s.AppendCompaction("SUM", first, 10, nil, nil); err != nil {
		t.Fatal(err)
	}
	hist := s.MessagesToLeaf()
	if len(hist) != 3 || hist[1].Text() != "u1" || hist[2].Text() != "u2" {
		t.Fatalf("fallback history: %+v", hist)
	}
}

func TestCompactionEventsPersist(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.AppendEvent("compaction_start", "overflow", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendDetailsEvent("patch_apply_updated", map[string]any{"toolCallId": "call-1", "partialResult": map[string]any{"changes": []any{map[string]any{"path": "a.txt"}}}}); err != nil {
		t.Fatal(err)
	}
	hist := s.MessagesToLeaf() // must ignore event entries
	if len(hist) != 0 {
		t.Fatalf("event entries leaked into history: %+v", hist)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	found := false
	previewFound := false
	for _, e := range s2.Entries() {
		if e.Type == "compaction_start" {
			found = true
			if d, ok := e.Details.(map[string]any); !ok || d["reason"] != "overflow" {
				t.Fatalf("details: %+v", e.Details)
			}
		}
		if e.Type == "patch_apply_updated" {
			previewFound = true
			if d, ok := e.Details.(map[string]any); !ok || d["toolCallId"] != "call-1" {
				t.Fatalf("preview details: %+v", e.Details)
			}
		}
	}
	if !found {
		t.Fatal("compaction_start not persisted")
	}
	if !previewFound {
		t.Fatal("patch preview event missing after reopen")
	}
}

func TestList(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s1, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.AppendMessage(types.Message{
		Role:    "user",
		Content: []types.Content{{Type: "text", Text: "first prompt"}},
	}); err != nil {
		t.Fatal(err)
	}
	id1 := s1.ID()
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Create(root, cwd, "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	id2 := s2.ID()
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("list: %+v", infos)
	}
	if infos[0].ID != id2 {
		t.Fatalf("newest first: %+v", infos)
	}
	var found bool
	for _, info := range infos {
		if info.ID == id1 {
			found = true
			if info.Title != "first prompt" || info.CWD != cwd {
				t.Fatalf("info: %+v", info)
			}
		}
	}
	if !found {
		t.Fatal("missing first session")
	}
	empty, err := List(filepath.Join(root, "missing"))
	if err != nil || empty != nil {
		t.Fatalf("missing root: %v %#v", err, empty)
	}
}

func TestEncodeCWD(t *testing.T) {
	got := EncodeCWD("/home/hgy/proj")
	if !strings.HasPrefix(got, "--") || !strings.HasSuffix(got, "--") {
		t.Fatalf("wrap: %q", got)
	}
	if strings.Contains(got, "/") {
		t.Fatalf("slash remains: %q", got)
	}
}

func TestToggleAllowed(t *testing.T) {
	if !(Toggle{}.Allowed("x")) {
		t.Fatal("default allow")
	}
	if (Toggle{Disabled: []string{"x"}}).Allowed("x") {
		t.Fatal("disabled")
	}
	if !(Toggle{Only: []string{"a"}}).Allowed("a") || (Toggle{Only: []string{"a"}}).Allowed("b") {
		t.Fatal("only")
	}
}

func TestTitlePinRemove(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{
		Role:    "user",
		Content: []types.Content{{Type: "text", Text: "auto title"}},
	}); err != nil {
		t.Fatal(err)
	}
	if TitleOf(s) != "auto title" {
		t.Fatalf("auto: %q", TitleOf(s))
	}
	if err := s.SetTitle("pinned name"); err != nil {
		t.Fatal(err)
	}
	if TitleOf(s) != "pinned name" {
		t.Fatalf("override: %q", TitleOf(s))
	}
	if err := s.SetPinned(true); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Config.Pinned || s2.Config.PinnedAt == "" || TitleOf(s2) != "pinned name" {
		t.Fatalf("reload: %+v", s2.Config)
	}
	_ = s2.Close()
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("open after remove")
	}
	infos, err := List(root)
	if err != nil || len(infos) != 0 {
		t.Fatalf("list after remove: %v %+v", err, infos)
	}
}
