package filetools

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func streamRead(t *testing.T, text string, args map[string]any) (string, *truncationDetails) {
	t.Helper()
	r := readTextStream(t.Context(), strings.NewReader(text), args)
	if r.IsError {
		t.Fatalf("read failed: %+v", r)
	}
	return r.Content[0].Text, r.Details.(readDetails).Truncation
}

func TestStreamReadLinePagingTotalsAndCRLF(t *testing.T) {
	for _, text := range []string{"", "one", "one\n", "one\r\ntwo\r\nlast"} {
		for offset := 1; offset <= strings.Count(text, "\n")+1; offset++ {
			got, d := streamRead(t, text, map[string]any{"offset": offset, "limit": 1})
			expected := strings.Split(text, "\n")[offset-1]
			if !strings.HasPrefix(got, expected) || d.TotalBytes != len(text) || d.TotalLines != strings.Count(text, "\n")+1 || d.OutputBytes != len(expected) {
				t.Fatalf("%q offset=%d got=%q %+v", text, offset, got, d)
			}
		}
	}
}

func TestStreamReadLongUTF8LineContinuesWithoutSkipping(t *testing.T) {
	text := strings.Repeat("中🙂", 20000) + "\r\nlast"
	cursor := int64(0)
	var assembled strings.Builder
	for i := 0; i < 10; i++ {
		got, d := streamRead(t, text, map[string]any{"byte_offset": cursor, "limit": 1})
		if !utf8.ValidString(got) || d.OutputBytes > maxBytes {
			t.Fatalf("invalid page %+v", d)
		}
		assembled.WriteString(got[:d.OutputBytes])
		if d.NextByteOffset == nil {
			break
		}
		if d.NextOffset != 0 || *d.NextByteOffset <= cursor {
			t.Fatalf("invalid continuation %+v", d)
		}
		cursor = *d.NextByteOffset
	}
	if assembled.String() != strings.Split(text, "\n")[0] {
		t.Fatalf("recovery bytes=%d want %d", assembled.Len(), len(strings.Split(text, "\n")[0]))
	}
}

func TestStreamReadUTF8ChunkBoundaryAndBinaryTail(t *testing.T) {
	text := strings.Repeat("a", 65535) + "🙂中\nend"
	_, d := streamRead(t, text, map[string]any{"offset": 2})
	if d.TotalBytes != len(text) || d.OutputBytes != 3 {
		t.Fatalf("boundary totals %+v", d)
	}
	for _, data := range []string{text + "\xff", text + "\xf0\x9f"} {
		r := readTextStream(t.Context(), strings.NewReader(data), map[string]any{"limit": 1})
		if !r.IsError {
			t.Fatal("binary tail accepted")
		}
	}
	r := readTextStream(t.Context(), strings.NewReader("🙂"), map[string]any{"byte_offset": 1})
	if !r.IsError {
		t.Fatal("mid-rune byte offset accepted")
	}
	r = readTextStream(t.Context(), strings.NewReader("🙂"), map[string]any{"byte_offset": 0, "offset": 1})
	if !r.IsError {
		t.Fatal("ambiguous offsets accepted")
	}
}

type cancellingReader struct {
	cancel context.CancelFunc
	calls  int
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == 2 {
		r.cancel()
	}
	return copy(p, bytes.Repeat([]byte("x"), len(p))), nil
}
func TestStreamReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := readTextStream(ctx, &cancellingReader{cancel: cancel}, nil)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "aborted") {
		t.Fatalf("cancellation %+v", r)
	}
}

func TestStreamUTF8MatchesStandardValidation(t *testing.T) {
	samples := [][]byte{[]byte("abc中🙂"), {0xff}, {0xe4, 0xb8}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xc0, 0x80}, {0xe4, 0xb8, 0xad, 0x80}}
	for _, sample := range samples {
		for step := 1; step <= 5; step++ {
			v := streamUTF8{}
			valid := true
			for start := 0; start < len(sample); start += step {
				if !v.valid(sample[start:min(start+step, len(sample))]) {
					valid = false
					break
				}
			}
			valid = valid && v.count == 0
			if valid != utf8.Valid(sample) {
				t.Fatalf("%x step=%d valid=%v", sample, step, valid)
			}
		}
	}
}

func TestTextReadUsesOpenWithoutReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "text")
	if err := os.WriteFile(path, []byte("abc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ops := streamOnlyReadOps{}
	r := readTool{ops: ops}.Execute(t.Context(), map[string]any{"file_path": path})
	if r.IsError || r.Content[0].Text != "abc\n" {
		t.Fatalf("stream path %+v", r)
	}
}

type streamOnlyReadOps struct{ localReadOperations }

func (streamOnlyReadOps) ReadFile(context.Context, string) ([]byte, error) {
	return nil, io.ErrUnexpectedEOF
}
