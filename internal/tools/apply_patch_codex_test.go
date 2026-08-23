package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchPreflightPreventsPrefixWrites(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "old.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Add File: added.txt\n+new\n*** Update File: old.txt\n@@\n-missing\n+changed\n*** End Patch"
	res := (applyPatchTool{cwd: cwd}).ExecuteRaw(context.Background(), patch)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "verification failed") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cwd, "added.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight wrote prefix: %v", err)
	}
}

func TestApplyPatchRejectsDuplicateCanonicalSource(t *testing.T) {
	cwd := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: a.txt\n+one\n*** Add File: sub/../a.txt\n+two\n*** End Patch"
	res := (applyPatchTool{cwd: cwd}).ExecuteRaw(context.Background(), patch)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "multiple operations target") {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyPatchPreservesMixedLineEndings(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "mixed.txt")
	if err := os.WriteFile(path, []byte("a\r\nkeep\nold\rtail"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: mixed.txt\n@@\n keep\n-old\n+new\n tail\n*** End Patch"
	res := (applyPatchTool{cwd: cwd}).ExecuteRaw(context.Background(), patch)
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
	b, _ := os.ReadFile(path)
	if got, want := string(b), "a\r\nkeep\nnew\r\ntail\r\n"; got != want {
		t.Fatalf("mixed = %q, want %q", got, want)
	}
}

func TestApplyPatchFailureReturnsCommittedPrefix(t *testing.T) {
	cwd := t.TempDir()
	pio := localPatchIO
	realWrite := pio.writeFile
	pio.writeFile = func(path string, data []byte, mode fs.FileMode) error {
		if filepath.Base(path) == "two.txt" {
			return errors.New("injected write failure")
		}
		return realWrite(path, data, mode)
	}
	patch := "*** Begin Patch\n*** Add File: one.txt\n+one\n*** Add File: two.txt\n+two\n*** End Patch"
	res := (applyPatchTool{cwd: cwd, io: &pio}).ExecuteRaw(context.Background(), patch)
	if !res.IsError {
		t.Fatalf("result = %+v", res)
	}
	details, ok := res.Details.(applyPatchDetails)
	if !ok || details.Exact || len(details.Changes) != 1 || details.Changes[0].Kind != "add" {
		t.Fatalf("details = %#v", res.Details)
	}
	if _, err := os.Stat(filepath.Join(cwd, "one.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchMoveRemoveFailureRecordsDestinationAdd(t *testing.T) {
	cwd := t.TempDir()
	source := filepath.Join(cwd, "source.txt")
	if err := os.WriteFile(source, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pio := localPatchIO
	pio.remove = func(path string) error { return errors.New("injected remove failure") }
	patch := "*** Begin Patch\n*** Update File: source.txt\n*** Move to: dest.txt\n@@\n-old\n+new\n*** End Patch"
	res := (applyPatchTool{cwd: cwd, io: &pio}).ExecuteRaw(context.Background(), patch)
	if !res.IsError {
		t.Fatalf("result = %+v", res)
	}
	details := res.Details.(applyPatchDetails)
	if !details.Exact || len(details.Changes) != 1 || details.Changes[0].Kind != "add" || filepath.Base(details.Changes[0].Path) != "dest.txt" {
		t.Fatalf("details = %#v", details)
	}
}

func TestStreamingPatchParserAcceptsEveryCharacterBoundary(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: one.txt\n+one\n*** Update File: old.txt\n@@\n-old\n+new\n*** End Patch"
	p := newStreamingPatchParser()
	for _, r := range patch {
		if _, err := p.Push(string(r)); err != nil {
			t.Fatal(err)
		}
	}
	hunks, err := p.Finish()
	if err != nil || len(hunks) != 2 || hunks[1].chunks[0].new[0] != "new" {
		t.Fatalf("hunks=%#v err=%v", hunks, err)
	}
}

func TestApplyPatchArgumentConsumerFlushesPendingFinalSnapshot(t *testing.T) {
	c := &applyPatchArgumentConsumer{parser: newStreamingPatchParser()}
	if _, ok := c.Consume("*** Begin Patch\n*** Add File: one.txt\n+one\n"); !ok {
		t.Fatal("first complete hunk did not emit")
	}
	if _, ok := c.Consume("*** Add File: two.txt\n+two\n*** End Patch"); ok {
		t.Fatal("second snapshot should be throttled")
	}
	value, ok := c.Finish()
	if !ok {
		t.Fatal("pending final snapshot was not flushed")
	}
	changes := value.(map[string]any)["changes"].([]applyPatchChangeDetail)
	if len(changes) != 2 {
		t.Fatalf("changes = %#v", changes)
	}
}
