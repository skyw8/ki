package compact

import (
	"context"
	"fmt"
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

func msg(role, text string) types.Message {
	return types.Message{Role: role, Content: []types.Content{{Type: "text", Text: text}}}
}

func mkEntries(msgs ...types.Message) []session.Entry {
	out := make([]session.Entry, len(msgs))
	for i := range msgs {
		out[i] = session.Entry{Type: "message", ID: fmt.Sprintf("e%d", i), Message: &msgs[i]}
	}
	return out
}

func TestPrepareCutNeverOnToolResult(t *testing.T) {
	// A turn: user → assistant(toolCall) → toolResult(large) → assistant final.
	// With a tiny budget the naive walk would cut at the big toolResult; the
	// valid-cut-point set must land on a message boundary instead.
	huge := strings.Repeat("x", 4000)
	entries := mkEntries(
		msg("user", "refactor module A"),
		msg("assistant", "[toolCall read]"),
		msg("toolResult", huge),
		msg("assistant", "done"),
	)
	prep, err := Prepare(entries, config.Compaction{Enabled: true, KeepRecentTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// The cut must be at e3 (assistant final) — e2 (toolResult) is not a valid
	// cut point, and the budget (1000) is exceeded only after e1..e2.
	if prep.FirstKeptEntryID != "e3" {
		t.Fatalf("firstKept = %q, want e3 (cut must skip the toolResult)", prep.FirstKeptEntryID)
	}
	if len(prep.RetainedTail) != 1 || prep.RetainedTail[0].Role != "assistant" {
		t.Fatalf("retainedTail: %+v", prep.RetainedTail)
	}
}

func TestPrepareSplitTurn(t *testing.T) {
	entries := mkEntries(
		msg("user", "old work"),
		msg("assistant", "old reply"),
		msg("user", "refactor module A"),
		msg("assistant", "[toolCall edit]"),
		msg("toolResult", strings.Repeat("r", 4000)),
		msg("assistant", "in progress"),
	)
	prep, err := Prepare(entries, config.Compaction{Enabled: true, KeepRecentTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// Budget is consumed inside the second turn (cut at e5, an assistant).
	// The turn prefix (e2..e4) must be split out for a separate summary, and
	// the turn start (e2 user) must not land in messagesToSummarize... e2 is
	// the user opening the split turn, so it belongs to the prefix.
	if !prep.IsSplitTurn {
		t.Fatalf("want split turn, firstKept=%q retained=%d", prep.FirstKeptEntryID, len(prep.RetainedTail))
	}
	if prep.FirstKeptEntryID != "e5" {
		t.Fatalf("firstKept = %q, want e5", prep.FirstKeptEntryID)
	}
	if len(prep.TurnPrefixMessages) != 3 {
		t.Fatalf("turnPrefix len = %d, want 3 (user + toolCall + toolResult)", len(prep.TurnPrefixMessages))
	}
	if len(prep.MessagesToSummarize) != 2 {
		t.Fatalf("messagesToSummarize len = %d, want 2 (old turn)", len(prep.MessagesToSummarize))
	}
}

func TestPrepareIncrementalPreviousSummary(t *testing.T) {
	// Second compaction: a previous compaction entry with a retained tail must
	// feed previousSummary and let the cut go back into the retained tail.
	// Second compaction: the previous compaction's retained tail (copies of
	// e0/e1) virtually expands into the cut search, so recent kept messages
	// can be summarized again on this pass.
	prev := session.Entry{
		Type:         "compaction",
		ID:           "c1",
		Summary:      "PREVIOUS SUMMARY",
		RetainedTail: []types.Message{msg("assistant", "newer"), msg("user", "newest")},
	}
	entries := append([]session.Entry{prev}, mkEntries(msg("assistant", "newer"), msg("user", "newest"))...)
	// keep=6: newest(2)+newer(2)+newest-virtual(2) fill the budget, so the cut
	// lands inside the virtual retained tail (cutIndex=1) → firstKept maps back
	// to e1, and the virtual tail head gets summarized.
	prep, err := Prepare(entries, config.Compaction{Enabled: true, KeepRecentTokens: 6})
	if err != nil {
		t.Fatal(err)
	}
	if prep.PreviousSummary != "PREVIOUS SUMMARY" {
		t.Fatalf("previousSummary = %q", prep.PreviousSummary)
	}
	if prep.FirstKeptEntryID != "e1" {
		t.Fatalf("firstKept = %q, want e1 (cut back into retained tail)", prep.FirstKeptEntryID)
	}
	// The summarized segment includes the virtual retained tail head ("newer"),
	// proving the previous tail participates in this pass's cut.
	if len(prep.MessagesToSummarize) != 1 || prep.MessagesToSummarize[0].Text() != "newer" {
		t.Fatalf("messagesToSummarize = %+v, want [newer] from the virtual retained tail", prep.MessagesToSummarize)
	}
}

func TestEstimateTokensStaleUsageFallsBack(t *testing.T) {
	// Fresh usage wins (input+cacheRead), stale usage (older than the last
	// compaction) falls back to the char/4 lower bound so a pre-compaction
	// total does not re-trigger compaction right after one finished.
	fresh := msg("assistant", "hi")
	fresh.Usage = &types.Usage{Input: 5000, CacheRead: 1000}
	fresh.Timestamp = 2000
	if got := EstimateTokens([]types.Message{fresh}, 1000); got != 6000 {
		t.Fatalf("fresh usage: got %d, want 6000", got)
	}
	stale := fresh
	stale.Timestamp = 500
	if got := EstimateTokens([]types.Message{stale}, 1000); got >= 6000 {
		t.Fatalf("stale usage must fall back to char/4, got %d", got)
	}
}

func TestExecuteSplitTurnJoinsSummaries(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []types.Message{msg("user", "old")},
		TurnPrefixMessages:  []types.Message{msg("user", "refactor")},
		IsSplitTurn:         true,
	}
	summary, _, err := Execute(context.Background(), prep, Static{Text: "S"}, config.Compaction{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "**Turn Context (split turn):**") {
		t.Fatalf("summary missing split-turn marker: %q", summary)
	}
	if strings.Count(summary, "(source chars=") != 2 {
		t.Fatalf("expected two summarizer calls (history + turn prefix): %q", summary)
	}
}
