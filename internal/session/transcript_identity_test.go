package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestTranscriptMetadataRetainsAcceptedIdentity(t *testing.T) {
	entry := Entry{Type: "message", ID: "notice", Message: &types.Message{
		Role: "user", Origin: "agent:/root/child", ClientRequestID: "accepted-notice",
		Content: []types.Content{{Type: "text", Text: "notification"}},
	}}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _, err := decodeMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Message == nil || metadata.Message.ClientRequestID != entry.Message.ClientRequestID ||
		metadata.Message.Origin != entry.Message.Origin {
		t.Fatalf("metadata lost accepted identity: %+v", metadata.Message)
	}
}

func TestTranscriptIndexRetainsBoundedNestedAudit(t *testing.T) {
	for _, typ := range []string{"tool_execution_start", "tool_execution_update", "tool_execution_end"} {
		t.Run(typ, func(t *testing.T) {
			entry := Entry{Type: typ, ID: "audit", ParentID: "parent-entry", Details: map[string]any{
				"toolCallId": "child", "parentCallId": "exec", "cellId": "cell",
				"toolName": "read", "requestedToolName": "Read", "timestamp": int64(2000),
				"durationMs": int64(7), "isError": true,
				"args": map[string]any{"path": "HIDDEN_ARGS"}, "partialResult": "HIDDEN_PROGRESS",
				"result": strings.Repeat("HIDDEN_RESULT", 1000),
			}}
			raw, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			metadata, index, err := decodeMetadata(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(index, BuildIndex([]Entry{entry})[0]) {
				t.Fatalf("body-free and direct index differ: %+v", index)
			}
			if index.ToolCallID != "child" || index.ParentCallID != "exec" || index.CellID != "cell" ||
				index.Name != "read" || index.RequestedToolName != "Read" || !index.IsError || index.DurationMs != 7 {
				t.Fatalf("index lost nested identity/outcome: %+v", index)
			}
			bounded, err := json.Marshal(struct {
				Entry Entry
				Index IndexEntry
			}{metadata, index})
			if err != nil || strings.Contains(string(bounded), "HIDDEN_") || len(bounded) > 1500 {
				t.Fatalf("metadata retained audit bodies: len=%d err=%v", len(bounded), err)
			}
			audit, ok := codeModeToolAudit(metadata)
			if !ok || audit["timestamp"] != int64(2000) {
				t.Fatalf("structural metadata lost audit: %+v", metadata)
			}
		})
	}
}

func TestTranscriptMetadataNestedAuditCountsSelectedBranch(t *testing.T) {
	entries := codeModeViewFixture(true)
	entries = append(entries, Entry{Type: "tool_execution_end", ID: "sibling", ParentID: "human", Details: map[string]any{
		"toolCallId": "sibling-call", "parentCallId": "exec", "toolName": "write", "isError": true,
	}})
	var metadata []Entry
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		meta, _, err := decodeMetadata(raw)
		if err != nil {
			t.Fatal(err)
		}
		metadata = append(metadata, meta)
	}
	page := BuildCompact(metadata, "answer", "", 1)
	if len(page.Turns) != 1 || page.Turns[0].Stats.Tools != 3 || page.Turns[0].Stats.ToolFailures != 1 {
		t.Fatalf("metadata counted lifecycle frames or sibling audit: %+v", page.Turns)
	}
	if !reflect.DeepEqual(page.Turns[0].Stats, BuildCompact(entries, "answer", "", 1).Turns[0].Stats) {
		t.Fatalf("body-free metadata changed branch statistics: %+v", page.Turns[0].Stats)
	}
}

func TestTranscriptIndexRetainsToolOutcome(t *testing.T) {
	for _, failed := range []bool{false, true} {
		entry := Entry{Type: "message", ID: "result", Message: &types.Message{
			Role: "toolResult", ToolCallID: "call", ToolName: "read", IsError: failed,
			DurationMs: 7, Content: []types.Content{{Type: "text", Text: "tool output"}},
		}}
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		metadata, index, err := decodeMetadata(raw)
		if err != nil {
			t.Fatal(err)
		}
		direct := BuildIndex([]Entry{entry})[0]
		for _, got := range []IndexEntry{index, direct} {
			if got.Role != "toolResult" || got.ToolCallID != "call" || got.Name != "read" ||
				got.DurationMs != 7 || got.IsError != failed {
				t.Fatalf("index lost tool outcome: %+v", got)
			}
		}
		if metadata.Message == nil || metadata.Message.IsError != failed {
			t.Fatalf("metadata lost tool outcome: %+v", metadata)
		}
	}
}
