package session

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ki/internal/types"
)

func codeModeViewFixture(finished bool) []Entry {
	var entries []Entry
	add := func(e Entry) {
		if len(entries) > 0 {
			e.ParentID = entries[len(entries)-1].ID
		}
		entries = append(entries, e)
	}
	message := func(id, role string, stamp int64, content ...types.Content) {
		add(Entry{Type: "message", ID: id, Message: &types.Message{Role: role, Timestamp: stamp, Content: content}})
	}
	message("human", "user", 1000, types.Content{Type: "text", Text: "work"})
	message("exec-assistant", "assistant", 1100, types.Content{Type: "text", Text: "HIDDEN_ASSISTANT"},
		types.Content{Type: "toolCall", ID: "exec", Name: "exec", Input: "HIDDEN_SOURCE", ToolType: "custom"})
	audit := func(id, typ, call, parent string, stamp int64, failed bool, result any) {
		add(Entry{ID: id, Type: typ, Details: map[string]any{
			"toolCallId": call, "parentCallId": parent, "cellId": "cell",
			"toolName": "read", "requestedToolName": "Read", "timestamp": stamp,
			"durationMs": 1300, "isError": failed, "args": map[string]any{"file_path": "HIDDEN_PATH"},
			"partialResult": "HIDDEN_PROGRESS", "result": result,
		}})
	}
	audit("child-start", "tool_execution_start", "child", "exec", 1200, false, nil)
	audit("child-old-progress", "tool_execution_update", "child", "exec", 1300, false, nil)
	audit("child-progress", "tool_execution_update", "child", "exec", 1350, false, nil)
	add(Entry{Type: "message", ID: "exec-result", Message: &types.Message{Role: "toolResult", ToolCallID: "exec", Timestamp: 1400}})
	add(Entry{Type: "request_header", ID: "wait-request", System: "later wait request"})
	message("wait-assistant", "assistant", 1500, types.Content{Type: "toolCall", ID: "wait", Name: "wait"})
	audit("notify-old", "tool_execution_update", "exec", "exec", 1600, false, nil)
	audit("notify", "tool_execution_update", "exec", "exec", 1700, false, nil)
	if finished {
		audit("child-end", "tool_execution_end", "child", "exec", 2500, true, map[string]any{
			"Content": []map[string]any{{"type": "text", "text": "HIDDEN_RESULT"}},
			"IsError": true, "Details": map[string]any{"exact": true},
		})
		add(Entry{Type: "message", ID: "wait-result", Message: &types.Message{Role: "toolResult", ToolCallID: "wait", Timestamp: 2600}})
		message("answer", "assistant", 2700, types.Content{Type: "text", Text: "final answer"})
	}
	return entries
}

func TestCodeModeCompactFoldsCompletedNestedBodiesAndRetainsStates(t *testing.T) {
	entries := codeModeViewFixture(true)
	page := BuildCompact(entries, "", "", 1)
	turn := page.Turns[0]
	if !slices.Equal(turn.VisibleNodeIDs, []string{"human", "answer"}) ||
		!slices.Equal(turn.EntryIDs, []string{"human", "answer"}) {
		t.Fatalf("folded nested bodies hid final answer: %+v", turn)
	}
	if turn.Stats.Tools != 3 || turn.Stats.ToolFailures != 1 || turn.Stats.Steps != 3 || turn.HiddenCount != 5 {
		t.Fatalf("nested counts or notify node accounting: %+v", turn)
	}
	if len(turn.ToolStates) != 1 || turn.ToolStates[0] != (TurnToolState{ID: "child", Finished: true, IsError: true}) {
		t.Fatalf("hidden cross-request nested identity not covered: %+v", turn.ToolStates)
	}
	raw, err := json.Marshal(page)
	if err != nil || strings.Contains(string(raw), "HIDDEN_") {
		t.Fatalf("folded body leaked: %s, %v", raw, err)
	}
}

func TestCodeModeCompactVisibleAuditReconstructsGroupedNode(t *testing.T) {
	entries := codeModeViewFixture(true)
	page := BuildCompact(entries, "", "", 20)
	turn := page.Turns[0]
	if turn.HiddenCount != 0 || slices.Contains(turn.VisibleNodeIDs, "notify") ||
		slices.Contains(turn.VisibleNodeIDs, "notify-old") || slices.Contains(turn.VisibleNodeIDs, "child-end") {
		t.Fatalf("audit frames became duplicate nodes: %+v", turn)
	}
	if !slices.Contains(turn.VisibleNodeIDs, "child") || turn.Stats.Tools != 3 {
		t.Fatalf("nested tool missing: %+v", turn)
	}
	for _, id := range []string{"child-start", "child-progress", "child-end", "notify"} {
		at := slices.IndexFunc(page.Entries, func(e Entry) bool { return e.ID == id })
		if at < 0 {
			t.Fatalf("missing reconstructable frame %s", id)
		}
		audit, ok := codeModeToolAudit(page.Entries[at])
		if !ok || audit["parentCallId"] != "exec" || audit["cellId"] != "cell" || audit["requestedToolName"] != "Read" {
			t.Fatalf("request/cell attribution changed under wait: %+v", page.Entries[at])
		}
	}
	if slices.Contains(turn.EntryIDs, "child-old-progress") || slices.Contains(turn.EntryIDs, "notify-old") {
		t.Fatalf("superseded progress inflated compact body: %+v", turn.EntryIDs)
	}
	// Exact expansion remains immutable and contains the complete audit stream.
	full, found := BuildTurn(entries, "", "human", "", 100)
	if !found || !slices.ContainsFunc(full.Entries, func(e Entry) bool { return e.ID == "child-old-progress" }) {
		t.Fatal("compact progress selection changed immutable detailed expansion")
	}
}

func TestCodeModeCompactRunningNestedStartSurvivesZeroKeep(t *testing.T) {
	entries := codeModeViewFixture(false)
	for _, keep := range []int{0, 1} {
		page := BuildCompact(entries, "", "", keep)
		turn := page.Turns[0]
		if !slices.Contains(turn.VisibleNodeIDs, "child") || !slices.Contains(turn.EntryIDs, "child-start") ||
			!slices.Contains(turn.EntryIDs, "child-progress") {
			t.Fatalf("running nested start lost under newer wait keep=%d: %+v", keep, turn)
		}
		if !slices.Contains(turn.ToolStates, TurnToolState{ID: "child"}) {
			t.Fatalf("running nested state missing after batch reset: %+v", turn.ToolStates)
		}
		if slices.Contains(turn.OmittedNodeIDs, "child") {
			t.Fatal("running nested card omitted from retained snapshot")
		}
	}
	reprojected, found := BuildCompactTurn(entries, "", "child-start", 0)
	if !found || !slices.Contains(reprojected.Turns[0].EntryIDs, "child-start") {
		t.Fatal("per-turn preference reprojection lost running audit")
	}
}

func TestCodeModeSlimAuditRetainsIdentityWithLargeResult(t *testing.T) {
	body := strings.Repeat("large result ", 1<<14)
	entry := Entry{ID: "audit", Type: "tool_execution_end", Details: map[string]any{
		"toolCallId": "child", "parentCallId": "exec", "cellId": "cell",
		"toolName": "read", "requestedToolName": "Read", "timestamp": int64(2500),
		"durationMs": int64(1300), "isError": true,
		"args": map[string]any{"file_path": strings.Repeat("path", 1<<14)},
		"result": map[string]any{
			"Content": []map[string]any{{"type": "text", "text": body}}, "IsError": true,
		},
	}}
	for _, limit := range []int{1024, 64 << 10} {
		view := compactViewEntryLimit(entry, limit)
		details, ok := codeModeToolAudit(view)
		if !ok || !view.Truncated || details["toolCallId"] != "child" || details["parentCallId"] != "exec" ||
			details["cellId"] != "cell" || details["timestamp"] != int64(2500) ||
			details["durationMs"] != int64(1300) || details["isError"] != true || details["requestedToolName"] != "Read" {
			t.Fatalf("slim discarded audit identity/status: %+v", view)
		}
		raw, err := json.Marshal(view)
		if err != nil || len(raw) > limit || !strings.Contains(string(raw), "[nested audit result truncated]") || strings.Contains(string(raw), body) {
			t.Fatalf("unbounded or silent audit truncation: bytes=%d limit=%d err=%v", len(raw), limit, err)
		}
	}
	slim := slimPath([]Entry{entry}, nil, toolsDigests{})[0]
	details, ok := codeModeToolAudit(slim)
	if !ok || !slim.Truncated || details["toolName"] != "read" {
		t.Fatalf("ordinary slim pass dropped nested metadata: %+v", slim)
	}
	if got := entry.Details.(map[string]any)["result"].(map[string]any)["Content"].([]map[string]any)[0]["text"]; got != body {
		t.Fatal("slimming mutated immutable audit body")
	}
}

func TestCodeModeDetailedAuditTailRetainsHumanAnchor(t *testing.T) {
	entries := codeModeViewFixture(false)
	cut := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == "child-progress" })
	tail := BuildTail(entries[:cut+1], "", 1, true)
	if len(tail.Entries) != 2 || tail.Entries[0].ID != "human" || tail.Entries[1].ID != "child-progress" {
		t.Fatalf("audit-only detailed tail lost human turn: %+v", tail)
	}
}

func TestCodeModeCompactBoundsNoisyProgressAndUsesCompletionClock(t *testing.T) {
	entries := codeModeViewFixture(true)
	end := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == "child-end" })
	page := BuildCompact(entries[:end+1], "", "", 20)
	if page.Turns[0].Stats.ElapsedMS != 1500 {
		t.Fatalf("nested completion did not advance turn clock: %+v", page.Turns[0].Stats)
	}
	entries = codeModeViewFixture(false)
	start := entries[:slices.IndexFunc(entries, func(e Entry) bool { return e.ID == "child-start" })+1]
	progress := strings.Repeat("large progress ", 1<<12)
	entries = slices.Clone(start)
	for i := range 128 {
		entries = append(entries, Entry{
			ID: "progress-" + strconv.Itoa(i), ParentID: entries[len(entries)-1].ID,
			Type: "tool_execution_update", Details: map[string]any{
				"toolCallId": "child", "parentCallId": "exec", "cellId": "cell",
				"toolName": "read", "partialResult": progress,
			},
		})
	}
	page = BuildCompact(entries, "", "", 0)
	raw, err := json.Marshal(page)
	if err != nil || len(raw) > MaxViewPageBytes || len(page.Entries) != 3 ||
		page.Entries[len(page.Entries)-1].ID != entries[len(entries)-1].ID {
		t.Fatalf("compact retained unbounded progress history: entries=%d bytes=%d err=%v", len(page.Entries), len(raw), err)
	}
}
