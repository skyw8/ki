package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObservationSummaryDoesNotSkipCursorAndReportsEffectiveBudget(t *testing.T) {
	p := &shellProcess{snapshot: Snapshot{Status: "running"}}
	p.appendOutputLocked([]byte("HEAD" + strings.Repeat("x", 100) + "TAIL"))
	first := p.observeWithOptions(ObservationOptions{MaxOutputTokens: 8, IncludeSummary: true})
	if len(first.Output) != 16 || !strings.HasPrefix(first.OutputSummary, "HEAD") || !strings.HasSuffix(first.OutputSummary, "TAIL") || first.SummaryOmittedBytes != 97 {
		t.Fatalf("summary: %+v", first)
	}
	if len(first.Output)+len(first.OutputSummary) > first.EffectiveOutputTokens*4 || first.SummaryBytes != len(first.OutputSummary) {
		t.Fatalf("shared budget exceeded: %+v", first)
	}
	second := p.observeWithOptions(ObservationOptions{MaxOutputTokens: 20000})
	if second.OutputOffset != 16 || len(second.Output) != 92 || second.EffectiveOutputTokens != 10000 || second.RequestedOutputTokens != 20000 {
		t.Fatalf("cursor/budget: %+v", second)
	}
}

type failingOutputFile struct{ *os.File }

func (f failingOutputFile) Write(p []byte) (int, error) { return 2, os.ErrPermission }

func TestOutputQuotaFailureStillDrainsAndCountsStoredPrefix(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &shellProcess{file: failingOutputFile{f}, changed: make(chan struct{}), snapshot: Snapshot{Status: "running"}}
	for _, chunk := range []string{"first", "second"} {
		if n, err := p.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("drain %d %v", n, err)
		}
	}
	s := p.observe(100)
	if s.Output != "firstsecond" || s.OutputFileBytes != 2 || !s.OutputFileIncomplete || s.Error == "" {
		t.Fatalf("quota result: %+v", s)
	}
}

func BenchmarkObservationSummary(b *testing.B) {
	p := &shellProcess{snapshot: Snapshot{Status: "running"}}
	p.appendOutputLocked([]byte(strings.Repeat("log line\n", 128000)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.mu.Lock()
		_, _ = p.summaryLocked(40000)
		p.mu.Unlock()
	}
}

func TestInvalidatedShellSnapshotsChangeGeneration(t *testing.T) {
	m := NewManager()
	defer m.Close()
	m.InvalidateShellSnapshots()
	if m.snapshots.generation != 1 {
		t.Fatal("generation unchanged")
	}
	_, err := m.InteractWithOptions(context.Background(), 1, "", time.Millisecond, ObservationOptions{}, nil)
	if err == nil {
		t.Fatal("unknown handle accepted")
	}
}
