package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestCreateReloadFork(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
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
	f.Close()
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
	defer s2.Close()
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

	if err := s2.SetModel("openai", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if s2.Config.Model != "gpt-4o" {
		t.Fatalf("model: %s", s2.Config.Model)
	}

	forked, err := Fork(root, s2)
	if err != nil {
		t.Fatal(err)
	}
	defer forked.Close()
	if forked.ID() == s2.ID() {
		t.Fatal("fork should mint new id")
	}
	if forked.Header.ParentSession != s2.Dir {
		t.Fatalf("parentSession: %q", forked.Header.ParentSession)
	}
	if len(forked.MessagesToLeaf()) != 2 {
		t.Fatalf("fork messages: %d", len(forked.MessagesToLeaf()))
	}

	s3, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if s3.ID() == id {
		t.Fatal("second create must be a new session")
	}
}

func TestList(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
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
