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

func TestCompactLifecycleIsDurableUnfoldedMetadata(t *testing.T) {
	for _, status := range []string{"empty", "failed", "committed"} {
		t.Run(status, func(t *testing.T) {
			entries := []Entry{
				{ID: "u", Type: "message", Message: &types.Message{Role: "user"}},
				{ID: "a", ParentID: "u", Type: "message", Message: &types.Message{Role: "assistant"}},
				{ID: "start", ParentID: "a", Type: "compaction_start", Details: map[string]any{"reason": "manual"}},
			}
			parent, row, steps := "start", "end", 1
			details := map[string]any{"reason": "manual", "status": status}
			if status == "committed" {
				entries = append(entries, Entry{ID: "checkpoint", ParentID: parent, Type: "compaction", Summary: "summary", Usage: &types.Usage{Input: 2}})
				parent, row, steps = "checkpoint", "checkpoint", 2
				details["entryId"] = parent
			}
			entries = append(entries, Entry{ID: "end", ParentID: parent, Type: "compaction_end", Details: details})
			for _, keep := range []int{0, 1, 20} {
				page := BuildCompact(entries, "end", "", keep)
				turn := page.Turns[0]
				if turn.HiddenCount != max(0, 1-keep) || turn.StepCount != steps || turn.Stats.Steps != steps {
					t.Fatalf("lifecycle inflated counts for keep=%d: %+v", keep, turn)
				}
				if !slices.Contains(turn.VisibleNodeIDs, row) || slices.Contains(turn.VisibleNodeIDs, "start") {
					t.Fatalf("missing/duplicate lifecycle row: %+v", turn.VisibleNodeIDs)
				}
				for _, id := range []string{"start", "end"} {
					if !slices.Contains(turn.EntryIDs, id) {
						t.Fatalf("lost durable lifecycle %s", id)
					}
				}
			}
			// A detailed page containing only the terminal status still needs
			// its human anchor, just like a page ending at a checkpoint.
			tail := withTurnOpeningUser(entries, entries[len(entries)-1:])
			if len(tail) != 2 || tail[0].ID != "u" {
				t.Fatalf("missing lifecycle opening user: %+v", tail)
			}
		})
	}
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

func TestCompactRuntimeUserMessagesDoNotSpendKeepSlots(t *testing.T) {
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
		t.Fatalf("runtime message consumed a reply slot: %+v", first)
	}
	if slices.ContainsFunc(page.Entries, func(e Entry) bool { return e.ID == "notice" }) {
		t.Fatal("older runtime notification body escaped the fold")
	}
}

func TestCompactRuntimeNotificationsNeverHideFinalReply(t *testing.T) {
	for _, humanOrigin := range []string{"", "extension:telegram"} {
		for _, trailing := range []bool{false, true} {
			for _, runtimeOrigin := range []string{"agent:child", "runtime:mailbox"} {
				for _, keep := range []int{0, 1} {
					t.Run(fmt.Sprintf("human=%s/trailing=%v/runtime=%s/keep=%d", humanOrigin, trailing, runtimeOrigin, keep), func(t *testing.T) {
						entries := []Entry{
							{Type: "message", ID: "u", Message: &types.Message{Role: "user", Origin: humanOrigin}},
							{Type: "message", ID: "work", ParentID: "u", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "working"}}}},
						}
						add := func(id string, m types.Message) {
							entries = append(entries, Entry{Type: "message", ID: id, ParentID: entries[len(entries)-1].ID, Message: &m})
						}
						notice := types.Message{Role: "user", Origin: runtimeOrigin, Content: []types.Content{{Type: "text", Text: "context-only notice"}}}
						if !trailing {
							add("notice", notice)
						}
						add("answer", types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "final answer"}}})
						if trailing {
							add("notice", notice)
						}
						page := BuildCompact(entries, "", "", keep)
						if len(page.Turns) != 1 {
							t.Fatalf("runtime notification split the human turn: %+v", page)
						}
						turn := page.Turns[0]
						wantVisible := []string{"u"}
						wantHidden := 3
						if keep == 1 {
							wantHidden = 2
							if trailing {
								wantVisible = []string{"u", "answer", "notice"}
								wantHidden = 1
							} else {
								wantVisible = []string{"u", "answer"}
							}
						}
						if turn.HiddenCount != wantHidden || !slices.Equal(turn.VisibleNodeIDs, wantVisible) {
							t.Fatalf("notice consumed keep or escaped chronological cutoff: %+v", turn)
						}
						if !slices.Equal(turn.EntryIDs, wantVisible) || turn.Stats.Steps != 2 || turn.Stats.Turn != 1 {
							t.Fatalf("projection changed order or accounting: %+v", turn)
						}
						if turn.FirstHiddenID != "work" || slices.Contains(turn.OmittedNodeIDs, "notice") {
							t.Fatalf("notice affected hidden reply identities: %+v", turn)
						}
						reprojected, found := BuildCompactTurn(entries, "", "notice", keep)
						if !found || !slices.Equal(reprojected.Turns[0].VisibleNodeIDs, wantVisible) {
							t.Fatalf("turn reprojection lost chronological cutoff: %+v", reprojected)
						}
					})
				}
			}
		}
	}
}

func TestCompactProjectionKeepsRunAbortedVisible(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "u0", Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "human"}}}},
		{Type: "message", ID: "a0", ParentID: "u0", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "older"}}}},
		{Type: "message", ID: "a1", ParentID: "a0", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "newer"}}}},
		{Type: "message", ID: "a2", ParentID: "a1", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "partial"}}, StopReason: "aborted"}},
		{Type: "run_aborted", ID: "x0", ParentID: "a2", Details: map[string]any{"reason": "user_request", "source": "webui"}},
	}
	page := BuildCompact(entries, "", "", 1)
	if len(page.Turns) != 1 || page.Turns[0].HiddenCount != 1 ||
		!slices.Equal(page.Turns[0].VisibleNodeIDs, []string{"u0", "a1", "a2", "x0"}) {
		t.Fatalf("run_aborted was folded or consumed keep: %+v", page)
	}
	if !slices.ContainsFunc(page.Entries, func(e Entry) bool { return e.ID == "x0" }) {
		t.Fatal("run_aborted body missing from compact page")
	}
}

func TestCompactProjectionKeepsFoldAnchorForMachineOnlyTurn(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "directive", Message: &types.Message{Role: "user", Origin: "agent", Content: []types.Content{{Type: "text", Text: "subagent directive"}}}},
		{Type: "message", ID: "answer", ParentID: "directive", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "done"}}}},
	}
	for _, keep := range []int{0, 1} {
		page := BuildCompact(entries, "", "", keep)
		if len(page.Turns) != 1 || page.Turns[0].ID != "directive" || page.Turns[0].HiddenCount != 2-keep {
			t.Fatalf("machine-only turn keep=%d: %+v", keep, page)
		}
		if page.Turns[0].Stats.Turn != 1 {
			t.Fatalf("runtime-only turn must start at ordinal 1: %+v", page.Turns[0])
		}
		want := []string{}
		wantEntries := []string{"directive"}
		if keep == 1 {
			want = append(want, "answer")
			wantEntries = append(wantEntries, "answer")
		}
		if !slices.Equal(page.Turns[0].VisibleNodeIDs, want) || !slices.Equal(page.Turns[0].EntryIDs, wantEntries) {
			t.Fatalf("missing stable folded runtime anchor keep=%d: %+v", keep, page)
		}
	}
}

func TestCompactRuntimeCutoffWhenAllOrNoRealRepliesAreKept(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "directive", Message: &types.Message{Role: "user", Origin: "agent"}},
		{Type: "message", ID: "first", ParentID: "directive", Message: &types.Message{Role: "assistant"}},
		{Type: "message", ID: "middle", ParentID: "first", Message: &types.Message{Role: "user", Origin: "agent:child"}},
		{Type: "message", ID: "final", ParentID: "middle", Message: &types.Message{Role: "assistant"}},
		{Type: "message", ID: "trailing", ParentID: "final", Message: &types.Message{Role: "user", Origin: "agent:child"}},
		{Type: "compaction", ID: "checkpoint", ParentID: "trailing", Summary: "summary"},
	}
	for _, keep := range []int{2, 20} {
		page := BuildCompact(entries, "", "", keep)
		turn := page.Turns[0]
		if turn.ID != "directive" || turn.HiddenCount != 1 ||
			!slices.Equal(turn.VisibleNodeIDs, []string{"first", "middle", "final", "trailing", "checkpoint"}) {
			t.Fatalf("all real replies kept should fold only the preceding directive: %+v", turn)
		}
		if !slices.Contains(turn.EntryIDs, "directive") {
			t.Fatal("folded directive lost its stable anchor body")
		}
	}
	onlyNotices := []Entry{entries[0], {Type: "message", ID: "notice", ParentID: "directive", Message: &types.Message{Role: "user", Origin: "agent:child"}}}
	for _, keep := range []int{0, 1, 20} {
		page := BuildCompact(onlyNotices, "", "", keep)
		turn := page.Turns[0]
		if turn.ID != "directive" || turn.HiddenCount != 2 || len(turn.VisibleNodeIDs) != 0 ||
			!slices.Equal(turn.EntryIDs, []string{"directive"}) {
			t.Fatalf("notice-only turns have no real reply cutoff keep=%d: %+v", keep, turn)
		}
	}
}

func TestCompactRuntimeCutoffPreservesLiveNestedToolAndFollowingNodes(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "u", Message: &types.Message{Role: "user"}},
		{Type: "message", ID: "old", ParentID: "u", Message: &types.Message{Role: "assistant"}},
		{Type: "message", ID: "before", ParentID: "old", Message: &types.Message{Role: "user", Origin: "agent:child"}},
		{Type: "tool_execution_start", ID: "live-start", ParentID: "before", Details: map[string]any{
			"toolCallId": "live-tool", "parentCallId": "exec", "cellId": "cell", "toolName": "read",
		}},
		{Type: "message", ID: "after", ParentID: "live-start", Message: &types.Message{Role: "user", Origin: "agent:child"}},
		{Type: "message", ID: "latest", ParentID: "after", Message: &types.Message{Role: "assistant"}},
		{Type: "compaction", ID: "checkpoint", ParentID: "latest", Summary: "summary"},
	}
	for _, keep := range []int{0, 1} {
		turn := BuildCompact(entries, "", "", keep).Turns[0]
		if turn.HiddenCount != 2 ||
			!slices.Equal(turn.VisibleNodeIDs, []string{"u", "live-tool", "after", "latest", "checkpoint"}) {
			t.Fatalf("live real reply did not bound the fold keep=%d: %+v", keep, turn)
		}
		if !slices.Contains(turn.EntryIDs, "live-start") {
			t.Fatal("live nested tool lost its reconstructable start")
		}
	}
}

func TestTurnClockKeepsFirstInputAcrossNotifications(t *testing.T) {
	for _, origin := range []string{"", "agent"} {
		var clock turnClock
		for _, message := range []types.Message{
			{Role: "user", Origin: origin, Timestamp: 1000},
			{Role: "assistant", Timestamp: 2000},
			{Role: "user", Origin: "agent:child", Timestamp: 3000},
			{Role: "user", Origin: "agent:child", Timestamp: 4000},
			{Role: "assistant", Timestamp: 5000},
		} {
			clock.add(Entry{Type: "message", Message: &message})
		}
		if got := clock.elapsed(); got != 4000 {
			t.Fatalf("origin=%q elapsed=%d, want 4000", origin, got)
		}
	}
	var prelude turnClock
	for _, m := range []types.Message{
		{Role: "user", Origin: "agent:prelude", Timestamp: 1000},
		{Role: "user", Timestamp: 2000},
		{Role: "user", Origin: "agent:notice", Timestamp: 3000},
		{Role: "assistant", Timestamp: 5000},
	} {
		prelude.add(Entry{Type: "message", Message: &m})
	}
	if prelude.start != 2000 || prelude.elapsed() != 3000 {
		t.Fatalf("prelude overrode human clock: %+v", prelude)
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

func TestCompactProjectionNeverFoldsCompactionIntoKeepSlot(t *testing.T) {
	entries := []Entry{
		{Type: "message", ID: "u1", Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "one"}}}},
		{Type: "message", ID: "a1", ParentID: "u1", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "first"}}}},
		{Type: "message", ID: "a2", ParentID: "a1", Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "final"}}}},
		{Type: "compaction", ID: "c1", ParentID: "a2", Summary: "sum", Timestamp: "1970-01-01T00:00:03Z"},
	}
	// A compaction trailing the turn is metadata, not a reply: it must not take
	// keep=1's slot and hide the turn's final answer.
	page := BuildCompact(entries, "", "", 1)
	turn := page.Turns[0]
	if turn.HiddenCount != 1 || !slices.Equal(turn.VisibleNodeIDs, []string{"u1", "a2", "c1"}) {
		t.Fatalf("compaction consumed the keep slot: %+v", turn)
	}
	if slices.ContainsFunc(page.Entries, func(e Entry) bool { return e.ID == "a1" }) {
		t.Fatal("folded reply leaked into the compact page")
	}
	if !slices.ContainsFunc(page.Entries, func(e Entry) bool { return e.ID == "c1" }) {
		t.Fatal("compaction body must stay visible on its own row")
	}
	// Even keep=0 shows the compaction while folding every reply.
	page = BuildCompact(entries, "", "", 0)
	if page.Turns[0].HiddenCount != 2 || !slices.Equal(page.Turns[0].VisibleNodeIDs, []string{"u1", "c1"}) {
		t.Fatalf("keep=0: %+v", page.Turns[0])
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
