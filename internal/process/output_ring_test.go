package process

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputRingRetainsBoundedValidTailAcrossWraps(t *testing.T) {
	var ring outputRing
	const limit = 31
	var input []byte
	dropped := 0
	for _, chunk := range []string{"🙂中", "longer than one chunk", "🙂🙂中中", strings.Repeat("中", 40), "", "ending"} {
		input = append(input, chunk...)
		dropped += ring.append([]byte(chunk), limit)
		want := input
		if len(want) > limit {
			want = want[len(want)-limit:]
			for len(want) > 0 && !utf8.RuneStart(want[0]) {
				want = want[1:]
			}
		}
		got := ring.bytes(0, 0)
		if !bytes.Equal(got, want) || !utf8.Valid(got) || dropped+len(got) != len(input) || len(ring.data) > limit {
			t.Fatalf("chunk %q: got %q want %q dropped=%d retained=%d", chunk, got, want, dropped, len(ring.data))
		}
		for offset := 0; offset <= len(want); offset++ {
			end := min(len(want), offset+5)
			if got := ring.bytes(offset, 5); !bytes.Equal(got, want[offset:end]) {
				t.Fatalf("wrapped range %d: %q want %q", offset, got, want[offset:end])
			}
		}
	}
	ring.reset()
	if got := ring.bytes(0, 0); len(got) != 0 {
		t.Fatalf("reset retained output: %q", got)
	}
}

func TestOutputRingSmallCommandsDoNotReserveFullWindow(t *testing.T) {
	var ring outputRing
	ring.append([]byte("small"), processBufferBytes)
	if len(ring.data) > 1024 || string(ring.bytes(0, 0)) != "small" {
		t.Fatalf("oversized allocation for tiny command: %d bytes", len(ring.data))
	}
}

func TestProcessOutputDropAndObservationPreserveCursor(t *testing.T) {
	p := &shellProcess{snapshot: Snapshot{Status: "running"}}
	input := []byte(strings.Repeat("🙂", processBufferBytes/4+3) + "end")
	p.appendOutputLocked(input)
	first := p.observe(1)
	if !first.Truncated || !utf8.ValidString(first.Output) || first.OutputOffset <= 0 || first.Output != "🙂" {
		t.Fatalf("dropped prefix: %+v", first)
	}
	next := p.observe(1)
	if !next.Truncated || next.OutputOffset != first.OutputOffset+int64(len(first.Output)) || next.Output != "🙂" {
		t.Fatalf("cursor did not advance: %+v", next)
	}
	if int64(p.buffer.size)+p.dropped != p.total {
		t.Fatal("retention accounting mismatch")
	}
}

func TestCompletedProcessReleasesConsumedWindowButKeepsPreview(t *testing.T) {
	for _, initiallyExited := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit_after_drain", true: "already_exited"}[initiallyExited], func(t *testing.T) {
			p := &shellProcess{snapshot: Snapshot{SessionID: 7, Status: "running"}}
			p.appendOutputLocked([]byte(strings.Repeat("x", processBufferBytes+8)))
			preview := p.snapshot.Output
			if initiallyExited {
				p.snapshot.Status = "exited"
			}
			var readBytes int
			for p.cursor < p.total {
				result := p.observe(10000)
				if len(result.Output) == 0 {
					t.Fatal("lost unread retained output")
				}
				readBytes += len(result.Output)
			}
			if readBytes != processBufferBytes {
				t.Fatalf("read %d retained bytes, want %d", readBytes, processBufferBytes)
			}
			if !initiallyExited {
				if p.buffer.data == nil {
					t.Fatal("live preview window released before finalization")
				}
				p.snapshot.Status = "exited"
				p.observe(10000)
			}
			if p.buffer.data != nil || p.dropped != p.total {
				t.Fatal("completed handle retained consumed output allocation")
			}
			next := p.observe(10000)
			if next.Output != "" || next.Truncated || next.OutputOffset != p.total || next.SessionID != 7 {
				t.Fatalf("completed handle changed after releasing its window: %+v", next)
			}
			manager := &Manager{processes: map[int64]*shellProcess{7: p}}
			if got := manager.Snapshots()[0].Output; got != preview {
				t.Fatal("consumption discarded the immutable runtime preview")
			}
		})
	}
}

func BenchmarkProcessOutputTail(b *testing.B) {
	chunk := bytes.Repeat([]byte("x"), 32*1024)
	b.Run("clone_per_write", func(b *testing.B) {
		buffer := bytes.Repeat([]byte("x"), processBufferBytes)
		b.ReportAllocs()
		b.SetBytes(int64(len(chunk)))
		for b.Loop() {
			buffer = append(buffer, chunk...)
			buffer = bytes.Clone(buffer[len(buffer)-processBufferBytes:])
		}
	})
	b.Run("bounded_ring", func(b *testing.B) {
		var ring outputRing
		ring.append(bytes.Repeat([]byte("x"), processBufferBytes), processBufferBytes)
		b.ReportAllocs()
		b.SetBytes(int64(len(chunk)))
		for b.Loop() {
			ring.append(chunk, processBufferBytes)
		}
	})
}
