package filetools

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkReadTextPage(b *testing.B) {
	path := filepath.Join(b.TempDir(), "large.log")
	data := bytes.Repeat([]byte("0123456789abcdef0123456789abcdef\n"), 1<<20)
	if err := os.WriteFile(path, data, 0600); err != nil {
		b.Fatal(err)
	}
	tool := readTool{cwd: filepath.Dir(path)}
	for _, tc := range []struct {
		name   string
		offset int
	}{{"first", 1}, {"middle", 524288}, {"tail", 1048500}} {
		b.Run(tc.name, func(b *testing.B) {
			args := map[string]any{"file_path": path, "offset": tc.offset, "limit": 20}
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if r := tool.Execute(b.Context(), args); r.IsError {
					b.Fatal(r)
				}
			}
		})
	}
}
