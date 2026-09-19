package session

import (
	"fmt"
	"testing"
)

func seedListRoot(tb testing.TB, sessions, turns, toolBytes int) string {
	tb.Helper()
	root := tb.TempDir()
	for i := range sessions {
		s, err := Create(root, tb.TempDir(), "openai", "model")
		if err != nil {
			tb.Fatal(err)
		}
		if err := s.SeedTranscript(SeedSpec{
			Turns:            turns,
			AssistantBytes:   64,
			ToolResultBytes:  toolBytes,
			SystemBytes:      2048,
			RepeatSamePrompt: true,
			Title:            fmt.Sprintf("session-%d", i),
		}); err != nil {
			tb.Fatal(err)
		}
		if err := s.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

func BenchmarkListHistory(b *testing.B) {
	root := seedListRoot(b, 8, 40, 4096)
	b.ReportAllocs()
	for b.Loop() {
		infos, err := List(root)
		if err != nil || len(infos) != 8 {
			b.Fatal(err, len(infos))
		}
	}
}

func BenchmarkListCache(b *testing.B) {
	root := seedListRoot(b, 8, 40, 4096)
	c := NewListCache()
	if _, err := c.List(root); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		infos, err := c.List(root)
		if err != nil || len(infos) != 8 {
			b.Fatal(err, len(infos))
		}
	}
}
