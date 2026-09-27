package session

import (
	"encoding/json"
	"fmt"
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
		if turn.HiddenCount != 640 || turn.Stats.Steps != 321 || turn.Stats.Input != 3200 {
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
