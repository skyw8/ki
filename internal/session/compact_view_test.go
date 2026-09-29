package session

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ki/internal/types"
)

func compactFixture(turns, steps int) []Entry {
	var entries []Entry
	appendMessage := func(id string, m types.Message) {
		parent := ""
		if len(entries) > 0 {
			parent = entries[len(entries)-1].ID
		}
		entries = append(entries, Entry{Type: "message", ID: id, ParentID: parent, Message: &m})
	}
	for turn := 0; turn < turns; turn++ {
		appendMessage(fmt.Sprintf("u%d", turn), types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "input"}}})
		for step := 0; step < steps; step++ {
			call := fmt.Sprintf("c%d-%d", turn, step)
			appendMessage("a"+call, types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "HIDDEN_BODY"}, {Type: "toolCall", ID: call, Name: "Bash", Arguments: map[string]any{"command": "HIDDEN_ARGUMENT"}}}, Usage: &types.Usage{Input: 10, Output: 2}})
			appendMessage("r"+call, types.Message{Role: "toolResult", ToolCallID: call, Content: []types.Content{{Type: "text", Text: "HIDDEN_RESULT"}}})
		}
		appendMessage(fmt.Sprintf("last%d", turn), types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "Final answer"}}})
	}
	return entries
}

func TestCompactPagesWholeTurnsWithoutHiddenBodies(t *testing.T) {
	entries := compactFixture(7, 320)
	page := BuildCompact(entries, "", "", 1)
	if len(page.Turns) != 4 || len(page.Entries) != 8 || page.OldestID != "u3" || !page.HasMore {
		t.Fatalf("tail: turns=%d entries=%d cursor=%s more=%v", len(page.Turns), len(page.Entries), page.OldestID, page.HasMore)
	}
	for _, turn := range page.Turns {
		if turn.HiddenCount != 640 || turn.Stats.Steps != 321 || turn.Stats.Input != 3200 || turn.Stats.Tools != 320 || turn.Stats.ToolFailures != 0 {
			t.Fatalf("incomplete turn summary: %+v", turn)
		}
	}
	for want := 2; want >= 0; want-- {
		page = BuildCompact(entries, "", page.OldestID, 1)
		if len(page.Turns) != 1 || len(page.Entries) != 2 || page.OldestID != fmt.Sprintf("u%d", want) || page.HasMore != (want > 0) {
			t.Fatalf("older turn %d: %+v", want, page)
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "HIDDEN_") || len(raw) > 4096 {
			t.Fatalf("folded bodies leaked into compact page: %d bytes", len(raw))
		}
	}
}

func TestCompactProjectionFoldsRuntimeUserMessages(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "u0", Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "human"}}}},
		{Type: "message", ID: "a0", ParentID: "u0", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "working"}}}},
		{Type: "message", ID: "notice", ParentID: "a0", Message: &types.Message{Role: "user", Origin: "agent:task-1", Content: []types.Content{{Type: "text", Text: "<task-notification>done</task-notification>"}}}},
		{Type: "message", ID: "a1", ParentID: "notice", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "final"}}}},
		{Type: "message", ID: "u1", ParentID: "a1", Message: &types.Message{Role: "user", Origin: "extension:telegram-bot", Content: []types.Content{{Type: "text", Text: "next human"}}}},
		{Type: "message", ID: "a2", ParentID: "u1", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "reply"}}}},
	}
	page := BuildCompact(entries, "", "", 1)
	if len(page.Turns) != 2 || page.Turns[0].ID != "u0" || page.Turns[1].ID != "u1" {
		t.Fatalf("runtime message split turns: %+v", page.Turns)
	}
	first := page.Turns[0]
	if first.HiddenCount != 2 || !slices.Equal(first.VisibleNodeIDs, []string{"u0", "a1"}) {
		t.Fatalf("runtime message was not folded: %+v", first)
	}
	if slices.ContainsFunc(page.Entries, func(e Entry) bool { return e.ID == "notice" }) {
		t.Fatal("folded runtime message body leaked into compact page")
	}
}

func TestCompactProjectionKeepsFoldAnchorForMachineOnlyTurn(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "directive", Message: &types.Message{Role: "user", Origin: "agent", Content: []types.Content{{Type: "text", Text: "subagent directive"}}}},
		{Type: "message", ID: "answer", ParentID: "directive", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "done"}}}},
	}
	page := BuildCompact(entries, "", "", 1)
	if len(page.Turns) != 1 || page.Turns[0].ID != "directive" || page.Turns[0].HiddenCount != 1 {
		t.Fatalf("machine-only turn: %+v", page)
	}
	if !slices.Equal(page.Turns[0].VisibleNodeIDs, []string{"answer"}) || len(page.Entries) != 2 {
		t.Fatalf("missing hidden fold anchor: %+v", page)
	}
}

func TestCompactTurnExpansionHasIndependentCursor(t *testing.T) {
	entries := compactFixture(3, 320)
	seen := map[string]bool{}
	cursor := ""
	for {
		page, found := BuildTurn(entries, "", "u1", cursor, 100)
		if !found {
			t.Fatal("missing turn")
		}
		for _, e := range page.Entries {
			if e.ID == "u0" || e.ID == "u2" {
				t.Fatal("expansion crossed into another turn")
			}
			seen[e.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.OldestID == cursor {
			t.Fatal("cursor stuck")
		}
		cursor = page.OldestID
	}
	if len(seen) != 642 {
		t.Fatalf("expanded %d entries, want 642", len(seen))
	}
}

func TestCompactProjectionKeepsToolPairsAndZeroKeep(t *testing.T) {
	entries := compactFixture(1, 3)
	// A turn ending at the last tool result: keep=1 selects that tool, not
	// the helper assistant node emitted by its call entry.
	page := BuildCompact(entries[:len(entries)-1], "", "", 1)
	turn := page.Turns[0]
	if len(page.Entries) != 3 || turn.HiddenCount != 5 || len(turn.OmittedNodeIDs) != 1 || turn.VisibleNodeIDs[1] != "c0-2" {
		t.Fatalf("tool projection: %+v", turn)
	}
	raw, _ := json.Marshal(page.Entries)
	if strings.Contains(string(raw), "HIDDEN_BODY") || !page.Entries[1].Truncated {
		t.Fatal("visible tool included hidden assistant prose")
	}
	page = BuildCompact(entries, "", "", 0)
	if len(page.Entries) != 1 || page.Turns[0].HiddenCount != 7 {
		t.Fatalf("keep=0: %+v", page)
	}
	// Sibling entries outside the chosen leaf never enter a compact turn.
	branched := append(entries, Entry{Type: "message", ID: "sibling", ParentID: entries[0].ID, Message: &types.Message{Role: "assistant"}})
	page = BuildCompact(branched, "sibling", "", 1)
	if len(page.Entries) != 2 || page.Turns[0].HiddenCount != 0 || page.Entries[1].ID != "sibling" {
		t.Fatalf("wrong branch: %+v", page)
	}
}

func TestCompactTurnCountsToolFailuresAndCacheMisses(t *testing.T) {
	usage := func(input, read int) *types.Usage { return &types.Usage{Input: input, CacheRead: read} }
	var entries []Entry
	add := func(id string, m types.Message) {
		parent := ""
		if len(entries) > 0 {
			parent = entries[len(entries)-1].ID
		}
		entries = append(entries, Entry{Type: "message", ID: id, ParentID: parent, Message: &m})
	}
	add("u0", types.Message{Role: "user"})
	// Reads the whole 30K prompt back: healthy, no miss.
	add("a0", types.Message{Role: "assistant", Usage: usage(100, 29900)})
	add("a0c", types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "t1", Name: "Bash"}}})
	add("r1", types.Message{Role: "toolResult", ToolCallID: "t1"})
	add("r2", types.Message{Role: "toolResult", ToolCallID: "t2", IsError: true})
	// Re-bills the previous 30K prompt → one notable miss.
	add("a1", types.Message{Role: "assistant", Usage: usage(30000, 0)})

	page := BuildCompact(entries, "", "", 1)
	turn := page.Turns[0]
	if turn.Stats.Tools != 2 || turn.Stats.ToolFailures != 1 || turn.Stats.CacheMisses != 1 {
		t.Fatalf("stats: tools=%d failures=%d misses=%d", turn.Stats.Tools, turn.Stats.ToolFailures, turn.Stats.CacheMisses)
	}
}

func TestCompactCacheMissBaselineSurvivesPaging(t *testing.T) {
	usage := func(input, read int) *types.Usage { return &types.Usage{Input: input, CacheRead: read} }
	var entries []Entry
	add := func(id string, m types.Message) {
		parent := ""
		if len(entries) > 0 {
			parent = entries[len(entries)-1].ID
		}
		entries = append(entries, Entry{Type: "message", ID: id, ParentID: parent, Message: &m})
	}
	// Turn 0 warms a 30K prompt. Turn 1 re-bills it, but the page below only
	// carries turns 1..4, so turn 1 owes its miss count to the baseline.
	add("u0", types.Message{Role: "user"})
	add("a0", types.Message{Role: "assistant", Usage: usage(100, 29900)})
	add("u1", types.Message{Role: "user"})
	add("a1", types.Message{Role: "assistant", Usage: usage(30000, 0)})
	for _, id := range []string{"u2", "u3", "u4"} {
		add(id, types.Message{Role: "user"})
		add("a"+id, types.Message{Role: "assistant", Usage: usage(10, 100)})
	}

	page := BuildCompact(entries, "", "", 1)
	if len(page.Turns) != 4 || page.Turns[0].ID != "u1" {
		t.Fatalf("page window: %+v", page.Turns)
	}
	if got := page.Turns[0].Stats.CacheMisses; got != 1 {
		t.Fatalf("turn 1 misses = %d, want 1 (baseline not threaded)", got)
	}
	for _, turn := range page.Turns[1:] {
		if turn.Stats.CacheMisses != 0 {
			t.Fatalf("turn %s unexpected misses: %d", turn.ID, turn.Stats.CacheMisses)
		}
	}
}

func TestCompactPageBoundsLargeVisibleSuffixWithoutSplittingTurn(t *testing.T) {
	entries := compactFixture(2, 25)
	for i := range entries {
		if m := entries[i].Message; m.Role == "assistant" {
			m.Content = []types.Content{{Type: "text", Text: strings.Repeat("x", 24*1024)}, {Type: "thinking", Thinking: strings.Repeat("y", 24*1024)}}
		}
	}
	page := BuildCompact(entries, "", "u1", 20)
	raw, _ := json.Marshal(page)
	if len(raw) > MaxViewPageBytes || len(page.Turns) != 1 || page.OldestID != "u0" || page.HasMore {
		t.Fatalf("page: bytes=%d turns=%d cursor=%s more=%v", len(raw), len(page.Turns), page.OldestID, page.HasMore)
	}
}

func TestCompactTimingAndLatestToolStates(t *testing.T) {
	entries := []Entry{
		{ID: "u", Type: "message", Message: &types.Message{Role: "user", Timestamp: 1000}},
		{ID: "a", ParentID: "u", Type: "message", Message: &types.Message{Role: "assistant", Timestamp: 2000, Content: []types.Content{{Type: "toolCall", ID: "slow"}, {Type: "toolCall", ID: "fast"}}}},
		{ID: "r", ParentID: "a", Type: "message", Message: &types.Message{Role: "toolResult", Timestamp: 5000, ToolCallID: "fast", DurationMs: 3000, IsError: true}},
	}
	page := BuildCompact(entries, "", "", 0)
	raw, err := json.Marshal(page.Turns[0])
	if err != nil {
		t.Fatal(err)
	}
	var turn struct {
		AssistantAt         int64
		ToolStates          []TurnToolState
		CumulativeElapsedMs int64
		Stats               struct {
			StartedAt int64
			ElapsedMs int64
		}
	}
	if err := json.Unmarshal(raw, &turn); err != nil {
		t.Fatal(err)
	}
	if turn.AssistantAt != 2000 || len(turn.ToolStates) != 2 || turn.ToolStates[0].ID != "slow" || turn.ToolStates[0].Finished || turn.ToolStates[1].ID != "fast" || !turn.ToolStates[1].Finished || !turn.ToolStates[1].IsError || turn.Stats.StartedAt != 1000 || turn.Stats.ElapsedMs != 4000 || turn.CumulativeElapsedMs != 4000 {
		t.Fatalf("missing sparse timing/pending state: %s", raw)
	}
	entries = append(entries, Entry{ID: "r2", ParentID: "r", Type: "message", Message: &types.Message{Role: "toolResult", Timestamp: 4000, ToolCallID: "slow", DurationMs: 2000}})
	page = BuildCompact(entries, "", "", 0)
	if page.Turns[0].Stats.ElapsedMS != 4000 {
		t.Fatal("parallel completion timestamp moved backwards")
	}
	entries = append(entries, Entry{ID: "c", ParentID: "r2", Type: "compaction", Timestamp: "1970-01-01T00:00:07Z"})
	page = BuildCompact(entries, "", "", 0)
	raw, _ = json.Marshal(page.Turns[0])
	if err := json.Unmarshal(raw, &turn); err != nil {
		t.Fatal(err)
	}
	if page.Turns[0].Stats.ElapsedMS != 6000 || turn.AssistantAt != 2000 || len(turn.ToolStates) != 2 || !turn.ToolStates[0].Finished || !turn.ToolStates[1].Finished {
		t.Fatalf("compaction must extend duration but not assistant boundary: %s", raw)
	}
	entries = append(entries, Entry{ID: "next", ParentID: "c", Type: "message", Message: &types.Message{Role: "assistant", Timestamp: 8000}})
	page = BuildCompact(entries, "", "", 0)
	if len(page.Turns[0].ToolStates) != 0 || page.Turns[0].AssistantAt != 8000 || page.Turns[0].EntryCount != len(entries) {
		t.Fatalf("next assistant must retire old batch: %+v", page.Turns[0])
	}
}

func TestCompactCumulativeElapsedIncludesUnloadedTurns(t *testing.T) {
	entries := compactFixture(7, 0)
	for i := range entries {
		entries[i].Message.Timestamp = int64(1000 + (i/2)*10000 + (i%2)*2000)
	}
	page := BuildCompact(entries, "", "", 0)
	for _, turn := range page.Turns {
		raw, _ := json.Marshal(turn)
		var got struct{ CumulativeElapsedMs int64 }
		_ = json.Unmarshal(raw, &got)
		if got.CumulativeElapsedMs != int64(turn.Stats.Turn)*2000 {
			t.Fatalf("cumulative timing: %s", raw)
		}
	}
	projected, found := BuildCompactTurn(entries, "", "u5", 0)
	if !found {
		t.Fatal("turn missing")
	}
	raw, _ := json.Marshal(projected.Turns[0])
	if !strings.Contains(string(raw), `"cumulativeElapsedMs":12000`) {
		t.Fatalf("reprojected cumulative timing: %s", raw)
	}
}
