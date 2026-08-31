package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func seedSession(t testing.TB, spec SeedSpec) *Session {
	t.Helper()
	s, err := Create(t.TempDir(), t.TempDir(), "openai", "model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SeedTranscript(spec); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSeedViewSlimsHistoryAndHugeBodies(t *testing.T) {
	history := seedSession(t, SeedSpec{Turns: 80, AssistantBytes: 64, ToolResultBytes: 8 * 1024, SystemBytes: 4096, Title: "perf-history", RepeatSamePrompt: true})
	jsonl, err := os.Stat(filepath.Join(history.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	view := BuildView(history.Entries(), history.LeafID(), DefaultViewLimit)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasMore {
		t.Fatal("80-turn leaf should exceed the default tail")
	}
	unchanged := 0
	for _, e := range view.Entries {
		if e.PromptUnchanged {
			unchanged++
		}
	}
	if unchanged == 0 {
		t.Fatal("repeated request headers should omit system/tools")
	}
	older := BuildBefore(history.Entries(), history.LeafID(), view.OldestID, DefaultViewLimit)
	if len(older.Entries) == 0 {
		t.Fatal("before window should return older leaf entries")
	}
	t.Logf("history jsonl=%dB viewJSON=%dB index=%d entries=%d unchangedHeaders=%d hasMore=%v older=%d",
		jsonl.Size(), len(raw), len(view.Index), len(view.Entries), unchanged, view.HasMore, len(older.Entries))
	if int64(len(raw)) >= jsonl.Size() {
		t.Fatalf("slim view %dB should be smaller than jsonl %dB", len(raw), jsonl.Size())
	}

	huge := seedSession(t, SeedSpec{Turns: 1, AssistantBytes: 300 * 1024, Title: "perf-huge"})
	hjsonl, err := os.Stat(filepath.Join(huge.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hview := BuildView(huge.Entries(), huge.LeafID(), DefaultViewLimit)
	hraw, err := json.Marshal(hview)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range hview.Entries {
		if e.Type != "message" || e.Message == nil || e.Message.Role != "assistant" {
			continue
		}
		found = true
		if !e.Truncated || len(e.Message.Text()) > MaxViewBytes {
			t.Fatalf("huge assistant should truncate: truncated=%v len=%d", e.Truncated, len(e.Message.Text()))
		}
	}
	if !found {
		t.Fatal("missing assistant")
	}
	full, ok := huge.Lookup(huge.LeafID())
	if !ok || full.Truncated || len(full.Message.Text()) < 300*1024 {
		t.Fatal("jsonl must keep the full assistant")
	}
	t.Logf("huge jsonl=%dB viewJSON=%dB", hjsonl.Size(), len(hraw))
	if int64(len(hraw)) > 80*1024 {
		t.Fatalf("truncated huge view %dB still too large", len(hraw))
	}
}

func BenchmarkBuildViewHistory(b *testing.B) {
	s := seedSession(b, SeedSpec{Turns: 400, AssistantBytes: 80, ToolResultBytes: 2048, SystemBytes: 2048, RepeatSamePrompt: true})
	entries := s.Entries()
	leaf := s.LeafID()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := BuildView(entries, leaf, DefaultViewLimit)
		if len(v.Index) == 0 {
			b.Fatal("empty")
		}
	}
}

func BenchmarkOpenHistory(b *testing.B) {
	s := seedSession(b, SeedSpec{Turns: 400, AssistantBytes: 80, ToolResultBytes: 2048, SystemBytes: 2048, RepeatSamePrompt: true})
	dir := s.Dir
	_ = s.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := Open(dir)
		if err != nil {
			b.Fatal(err)
		}
		_ = got.Close()
	}
}

func BenchmarkMarshalView(b *testing.B) {
	s := seedSession(b, SeedSpec{Turns: 400, AssistantBytes: 80, ToolResultBytes: 2048, SystemBytes: 2048, RepeatSamePrompt: true})
	view := BuildView(s.Entries(), s.LeafID(), DefaultViewLimit)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		raw, err := json.Marshal(view)
		if err != nil || len(raw) < 100 {
			b.Fatal(err, len(raw))
		}
	}
}

func TestSeedTranscriptRepeatPrompt(t *testing.T) {
	s := seedSession(t, SeedSpec{Turns: 3, RepeatSamePrompt: true, AssistantBytes: 8})
	if s.LeafID() == "" {
		t.Fatal("leaf")
	}
	if n := len(s.Entries()); n != 9 {
		t.Fatalf("entries %d", n)
	}
	systems := headerSystems(s)
	if len(systems) != 3 || systems[0] == "" || systems[0] != systems[1] || systems[1] != systems[2] {
		t.Fatalf("repeated systems %+v", systems)
	}
}

func TestSeedTranscriptVariesPrompt(t *testing.T) {
	s := seedSession(t, SeedSpec{Turns: 3, AssistantBytes: 8})
	systems := headerSystems(s)
	if len(systems) != 3 || systems[0] == "" || systems[0] == systems[1] || systems[1] == systems[2] {
		t.Fatalf("varied systems %+v", systems)
	}
}

func headerSystems(s *Session) []string {
	var out []string
	for _, e := range s.Entries() {
		if e.Type == "request_header" {
			out = append(out, e.System)
		}
	}
	return out
}
