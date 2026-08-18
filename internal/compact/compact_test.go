package compact

import (
	"context"
	"errors"
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
	defer func() { _ = s.Close() }()
	for range 6 {
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

func TestShouldRunMaxContextTokensCapsWindow(t *testing.T) {
	// Global floor: a small MaxContextTokens triggers compaction way before the
	// model window (128k) is reached.
	cfg := config.Compaction{Enabled: true, ReserveTokens: 1000, MaxContextTokens: 6000}
	if ShouldRun(500, 128000, cfg) {
		t.Fatal("below capped threshold should not compact")
	}
	if !ShouldRun(5500, 128000, cfg) {
		t.Fatal("above capped threshold should compact")
	}
	// Without the cap, 5500 tokens is far below the model window.
	if ShouldRun(5500, 128000, config.Compaction{Enabled: true, ReserveTokens: 1000}) {
		t.Fatal("uncapped small context should not compact")
	}
	// Cap larger than the model window must not inflate it.
	big := config.Compaction{Enabled: true, ReserveTokens: 1000, MaxContextTokens: 1 << 20}
	if ShouldRun(120000, 128000, big) {
		t.Fatal("cap above window must not inflate the window")
	}
	// Cap applies to the default window when the model window is unknown.
	if !ShouldRun(5500, 0, cfg) {
		t.Fatal("cap should apply to the default window too")
	}
	// Disabled still wins.
	if ShouldRun(1<<20, 128000, config.Compaction{Enabled: false, MaxContextTokens: 6000}) {
		t.Fatal("disabled must never compact")
	}
}

func TestPrepareNothingToCompactWhenUnderBudget(t *testing.T) {
	// Tiny session: char/4 budget never reaches keepRecentTokens, so there is
	// nothing to summarize. Prepare must report it (pi returns undefined)
	// instead of producing an empty summary.
	root := t.TempDir()
	s, err := session.Create(root, t.TempDir(), "openai", "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, _ = s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}})
	_, _ = s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "hi"}}})

	prep, err := Prepare(s.LeafEntries(), config.Compaction{KeepRecentTokens: 20000})
	if !errors.Is(err, ErrNothingToCompact) || prep != nil {
		t.Fatalf("want ErrNothingToCompact + nil prep, got prep=%+v err=%v", prep, err)
	}

	// With a tiny budget the same session compacts fine (the user message lands
	// in the turn prefix of a split turn).
	prep, err = Prepare(s.LeafEntries(), config.Compaction{KeepRecentTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if prep == nil || (len(prep.MessagesToSummarize) == 0 && len(prep.TurnPrefixMessages) == 0) {
		t.Fatalf("tiny budget should produce a preparation: %+v", prep)
	}
}

func TestEstimateTokensUsagePriorityAndTrailing(t *testing.T) {
	text := func(s string) []types.Content {
		return []types.Content{{Type: "text", Text: s}}
	}
	// Four-part sum is the fallback when TotalTokens is 0, plus trailing
	// messages after the usage-bearing assistant add char/4 (pi
	// estimateContextTokens: usageTokens + trailingTokens).
	msgs := []types.Message{
		{Role: "user", Content: text("aaaa")}, // 1 token
		{Role: "assistant", Usage: &types.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5}},
		{Role: "user", Content: text("bbbbbbbb")}, // 2 tokens trailing
	}
	if got := EstimateTokens(msgs, 0); got != 157 {
		t.Fatalf("four-part sum + trailing: %d, want 157", got)
	}
	// TotalTokens wins when present (responses API provides it directly).
	msgs[1].Usage.TotalTokens = 200
	if got := EstimateTokens(msgs, 0); got != 202 {
		t.Fatalf("totalTokens + trailing: %d, want 202", got)
	}
	// No usage at all → pure char/4 over everything (1 + 0 + 2).
	msgs[1].Usage = nil
	if got := EstimateTokens(msgs, 0); got != 3 {
		t.Fatalf("char/4 fallback: %d, want 3", got)
	}
	// All-zero usage can't contribute: falls back to char/4 (0 here).
	zero := []types.Message{{Role: "assistant", Usage: &types.Usage{}}}
	if got := EstimateTokens(zero, 0); got != 0 {
		t.Fatalf("zero usage → char/4: %d, want 0", got)
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
