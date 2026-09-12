package session

import (
	"os"
	"path/filepath"
	"testing"

	"ki/internal/types"
)

// List must not hide a session because one transcript row is undecodable: the
// lite scan skips rows it cannot read and keeps looking for the title.
func TestListToleratesUndecodableEntry(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // test-owned session directory.
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(
		"{\"type\":\"message\",\"message\":\"not an object\"}\n" +
			"{\"type\":\"message\",\"id\":\"u1\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"deep title\"}]}}\n",
	); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Title != "deep title" {
		t.Fatalf("list: %+v", infos)
	}
}

func TestListCacheInvalidatesOnChange(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewListCache()
	s, err := Create(root, cwd, "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	first, err := c.List(root)
	if err != nil || len(first) != 1 || first[0].Title != "" {
		t.Fatalf("first: %v %+v", err, first)
	}
	if _, err := s.AppendMessage(types.Message{
		Role:    "user",
		Content: []types.Content{{Type: "text", Text: "hello cache"}},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := c.List(root)
	if err != nil || len(second) != 1 || second[0].Title != "hello cache" {
		t.Fatalf("jsonl change not picked up: %v %+v", err, second)
	}
	if err := s.SetPinned(true); err != nil {
		t.Fatal(err)
	}
	third, err := c.List(root)
	if err != nil || len(third) != 1 || !third[0].Pinned {
		t.Fatalf("config change not picked up: %v %+v", err, third)
	}
}

func TestListCachePrunesRemovedSession(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewListCache()
	s, err := Create(root, cwd, "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if infos, err := c.List(root); err != nil || len(infos) != 1 {
		t.Fatalf("seed: %v %+v", err, infos)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	infos, err := c.List(root)
	if err != nil || len(infos) != 0 {
		t.Fatalf("after remove: %v %+v", err, infos)
	}
	if _, ok := c.rows[dir]; ok {
		t.Fatal("cache kept a pruned directory")
	}
}
