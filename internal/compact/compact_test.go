package compact

import (
	"context"
	"strings"
	"testing"

	"ki/internal/config"
	"ki/internal/session"
	"ki/internal/types"
)

func TestShouldRunAndAppendRebuildsHistory(t *testing.T) {
	if ShouldRun(100, 128000, config.Compaction{Enabled: true, ReserveTokens: 16384}) {
		t.Fatal("small context should not compact")
	}
	if !ShouldRun(120000, 128000, config.Compaction{Enabled: true, ReserveTokens: 16384}) {
		t.Fatal("over threshold")
	}
	root := t.TempDir()
	s, err := session.Create(root, t.TempDir(), "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 6; i++ {
		_, _ = s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "aaaaaaaaaa"}}})
		_, _ = s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "bbbbbbbbbb"}}})
	}
	e, err := Run(context.Background(), s, Static{Text: "PREV"}, config.Compaction{Enabled: true, KeepRecentTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != "compaction" || e.Summary == "" || e.FirstKeptEntryID == "" {
		t.Fatalf("entry: %+v", e)
	}
	hist := s.MessagesToLeaf()
	if len(hist) == 0 || hist[0].Text() == "" || !strings.Contains(hist[0].Text(), "PREV") {
		t.Fatalf("history should start with summary: %+v", hist)
	}
}
