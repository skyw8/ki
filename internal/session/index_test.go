package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexLookupAddRemove(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s1, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s1.Close() }()
	s2, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	// NewIndex built from the same walk List does (the server path).
	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(infos)

	dir, ok := ix.Lookup(s1.ID())
	if !ok || dir != s1.Dir {
		t.Fatalf("lookup s1: ok=%v dir=%q want %q", ok, dir, s1.Dir)
	}
	dir, ok = ix.Lookup(s2.ID())
	if !ok || dir != s2.Dir {
		t.Fatalf("lookup s2: ok=%v dir=%q want %q", ok, dir, s2.Dir)
	}
	if _, ok := ix.Lookup("deadbeef"); ok {
		t.Fatal("unknown id must miss")
	}

	// Add after the fact (create/fork path).
	s3, err := Create(root, cwd, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s3.Close() }()
	ix.Add(s3.ID(), s3.Dir)
	if dir, ok := ix.Lookup(s3.ID()); !ok || dir != s3.Dir {
		t.Fatalf("added entry missing: %v %q", ok, dir)
	}

	ix.Remove(s1.ID())
	if _, ok := ix.Lookup(s1.ID()); ok {
		t.Fatal("removed entry still present")
	}
}

func TestIndexConcurrent(_ *testing.T) {
	ix := NewIndex(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 500 {
			ix.Add(fmt.Sprintf("id%03d", i), fmt.Sprintf("dir%03d", i))
		}
	}()
	for i := range 500 {
		ix.Lookup(fmt.Sprintf("id%03d", i))
		if i%2 == 0 {
			ix.Remove(fmt.Sprintf("id%03d", i))
		}
	}
	<-done
}
