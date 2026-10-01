package process

import (
	"os"
	"path/filepath"
	"testing"
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
	if _, err := p.Write([]byte("lost output")); err == nil {
		t.Fatal("closed spool accepted output")
	}
	p.finish(0, nil)
	snapshot := p.observe(100)
	if snapshot.Error == "" || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
		t.Fatalf("output failure hidden by zero exit: %+v", snapshot)
	}
}
