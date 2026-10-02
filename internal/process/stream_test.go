package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProcessOutputSplitUTF8IsIncrementalAndRawSpoolIsLossless(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	p := &shellProcess{file: file, changed: make(chan struct{}), snapshot: Snapshot{Status: "running"}}
	data := []byte("🙂中")
	if _, err = p.Write(data[:2]); err != nil {
		t.Fatal(err)
	}
	if got := p.observe(100).Output; got != "" {
		t.Fatalf("partial rune consumed: %q", got)
	}
	if _, err = p.Write(data[2:5]); err != nil {
		t.Fatal(err)
	}
	first := p.observe(100).Output
	if first != "🙂" || !utf8.ValidString(first) {
		t.Fatalf("first output %q", first)
	}
	if _, err = p.Write(data[5:]); err != nil {
		t.Fatal(err)
	}
	if got := p.observe(100).Output; got != "中" {
		t.Fatalf("continuation %q", got)
	}
	if got := p.observe(100).Output; got != "" {
		t.Fatalf("output replayed: %q", got)
	}
	raw, err := os.ReadFile(file.Name())
	if err != nil || string(raw) != string(data) {
		t.Fatalf("raw %q %v", raw, err)
	}
}

func TestPTYSpoolFailureIsReportedEvenWhenCommandExitsSuccessfully(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "closed-spool"))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	p := &shellProcess{file: file, done: make(chan struct{}), changed: make(chan struct{}), snapshot: Snapshot{Status: "running"}}
	if n, err := p.Write([]byte("lost output")); n != len("lost output") || err != nil {
		t.Fatalf("spool failure stopped output draining: %d %v", n, err)
	}
	if got := p.observe(100); got.Output != "lost output" || got.Error == "" {
		t.Fatalf("spool failure lost bounded output or live diagnostic: %+v", got)
	}
	p.finish(0, nil)
	snapshot := p.observe(100)
	if snapshot.Error == "" || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
		t.Fatalf("output failure hidden by zero exit: %+v", snapshot)
	}
}

func TestProcessFinishPublishesPendingOutputAndIncompleteRune(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	var updates []Update
	p := &shellProcess{
		file: file, done: make(chan struct{}), changed: make(chan struct{}),
		snapshot: Snapshot{Status: "running"},
		publish:  func(update Update) { updates = append(updates, update) },
	}
	// Suppress timed publication until finalization. No fixed sleep is needed
	// to exercise the elapsed-time branch that formerly discarded the update.
	p.lastPublished = time.Now()
	if _, err := p.Write([]byte("tail\xF0")); err != nil {
		t.Fatal(err)
	}
	p.lastPublished = time.Time{}
	p.finish(0, nil)
	if len(updates) != 1 || updates[0].Delta != "tail\uFFFD" || updates[0].Process.Status != "exited" {
		t.Fatalf("final delta lost: %+v", updates)
	}
	if got := p.observe(100).Output; got != "tail\uFFFD" {
		t.Fatalf("final incremental output: %q", got)
	}
}

func TestProcessPendingDeltaOverflowKeepsUTF8Boundary(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	var final Update
	p := &shellProcess{
		file: file, done: make(chan struct{}), changed: make(chan struct{}),
		snapshot: Snapshot{Status: "running"}, lastPublished: time.Now(),
		publish: func(update Update) { final = update },
	}
	input := strings.Repeat("中", 8192) + "🙂"
	if _, err := p.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	p.finish(0, nil)
	if !utf8.ValidString(final.Delta) || len(final.Delta) > 8192 || !strings.HasSuffix(final.Delta, "🙂") || strings.ContainsRune(final.Delta, utf8.RuneError) {
		t.Fatalf("invalid bounded final delta: %q", final.Delta)
	}
}
