package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestContextEstimatesUseFullBodiesBeforePublicSlimming(t *testing.T) {
	body := strings.Repeat("中🙂", 10_000)
	message := &types.Message{Role: "assistant", Content: []types.Content{
		{Type: "text", Text: body},
		{Type: "thinking", Thinking: "thinking"},
		{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{"file_path": "a.txt"}},
		{Type: "image", Data: strings.Repeat("SECRET-BINARY", 10_000)},
		{Type: "thinking", ThinkingSignature: "SECRET-OPAQUE"},
	}}
	entries := []Entry{
		{Type: "message", ID: "u", Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "question"}}}},
		{Type: "request_header", ID: "r1", ParentID: "u", System: "system text", Tools: []ToolSchema{{Name: "read", Parameters: map[string]any{"type": "object"}}}},
		{Type: "request_header", ID: "r2", ParentID: "r1", System: "system text", Tools: []ToolSchema{{Name: "read", Parameters: map[string]any{"type": "object"}}}},
		{Type: "message", ID: "a", ParentID: "r2", Message: message},
		{Type: "compaction", ID: "local", ParentID: "a", Summary: body},
		{Type: "compaction", ID: "remote", ParentID: "local", Responses: &types.ResponsesContext{Items: []json.RawMessage{json.RawMessage(`{"encrypted_content":"SECRET-OPAQUE"}`)}}},
	}
	stored, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	index := BuildIndex(entries)
	slim := BuildTail(entries, "remote", 100, true).Entries
	full := LookupEntries(entries, []string{"r1", "r2", "a", "local", "remote"})
	for i := range index {
		if !reflect.DeepEqual(index[i].ContextEstimate, slim[i].ContextEstimate) {
			t.Fatalf("%s index/slim estimate differ: %#v / %#v", entries[i].ID, index[i].ContextEstimate, slim[i].ContextEstimate)
		}
	}
	if !slim[2].PromptUnchanged || slim[2].ContextEstimate.System == nil || *slim[2].ContextEstimate.System == 0 {
		t.Fatal("unchanged header lost full prompt estimate")
	}
	wantMessage := contextTokens(body) + contextTokens("thinking") + contextTokens("read\n{\n  \"file_path\": \"a.txt\"\n}")
	if got := *index[3].ContextEstimate.Message; got != wantMessage {
		t.Fatalf("message estimate %d, want %d", got, wantMessage)
	}
	if !slim[3].Truncated || len(slim[3].Message.Content[0].Text) == len(body) {
		t.Fatal("fixture did not slim the full message")
	}
	if *index[4].ContextEstimate.Summary != contextTokens("Previous conversation summary:\n"+body) {
		t.Fatal("summary estimate was priced from preview")
	}
	if index[5].ContextEstimate != nil || slim[5].ContextEstimate != nil || full[4].ContextEstimate != nil {
		t.Fatal("remote opaque payload must not get a plaintext estimate")
	}
	for i, entry := range full[:4] {
		if !reflect.DeepEqual(entry.ContextEstimate, index[i+1].ContextEstimate) {
			t.Fatal("exact lookup changed estimate")
		}
	}
	after, _ := json.Marshal(entries)
	if !bytes.Equal(stored, after) || bytes.Contains(after, []byte("contextEstimate")) {
		t.Fatal("public estimates mutated or persisted into source entries")
	}
}

func TestContextEstimatesKnownZeroAndUnknownPreview(t *testing.T) {
	for _, entry := range []Entry{
		{Type: "message", Message: &types.Message{Role: "user"}},
		{Type: "message", Message: &types.Message{Role: "user", Content: []types.Content{{Type: "image", Data: "opaque"}}}},
	} {
		estimate := estimateContext(entry)
		if estimate == nil || estimate.Message == nil || *estimate.Message != 0 {
			t.Fatal("known zero message must remain present")
		}
		raw, _ := json.Marshal(estimate)
		if string(raw) != `{"message":0}` {
			t.Fatalf("known zero wire estimate: %s", raw)
		}
	}
	header := estimateContext(Entry{Type: "request_header"})
	raw, _ := json.Marshal(header)
	if string(raw) != `{"system":0,"tools":0}` {
		t.Fatalf("empty prompt lost known zeros: %s", raw)
	}
	if estimateContext(Entry{Type: "message", Truncated: true, Message: &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "preview"}}}}) != nil {
		t.Fatal("preview cannot invent a complete estimate")
	}
}

func TestTranscriptContextEstimatesSurviveMetadataAndHydration(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("多字节 context ", 1000)
	entries := []Entry{
		{Type: "message", ID: "u", Message: &types.Message{Role: "user", Origin: "agent:fixture", Content: []types.Content{{Type: "text", Text: body}}}},
		{Type: "request_header", ID: "r", ParentID: "u", System: body, Tools: []ToolSchema{{Name: "read", Parameters: map[string]any{"type": "object"}}}},
		{Type: "message", ID: "a", ParentID: "r", Message: &types.Message{Role: "assistant", Content: []types.Content{
			{Type: "thinking", Thinking: body}, {Type: "toolCall", ID: "call", Name: "exec_command", Input: "raw custom input"},
			{Type: "toolCall", ID: "empty", Name: "read"}, {Type: "toolCall", ID: "json", Name: "read", Arguments: map[string]any{"file_path": "fixture"}},
		}}},
		{Type: "compaction", ID: "local", ParentID: "a", Summary: body},
		{Type: "compaction", ID: "remote", ParentID: "local", Responses: &types.ResponsesContext{Items: []json.RawMessage{json.RawMessage(`{"encrypted_content":"never-estimate-this"}`)}}},
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	if err := encoder.Encode(Header{Type: "session", ID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := BuildIndex(entries)
	for i, index := range transcript.Index() {
		if !reflect.DeepEqual(index.ContextEstimate, want[i].ContextEstimate) {
			t.Fatalf("%s metadata/full estimate mismatch: %#v / %#v", index.ID, index.ContextEstimate, want[i].ContextEstimate)
		}
	}
	if len(transcript.Entries()[0].Message.Text()) >= len(body) || transcript.Entries()[1].System != "" {
		t.Fatal("metadata retained full bodies")
	}
	hydrated, err := transcript.Lookup([]string{"u", "r", "a", "local", "remote"})
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range hydrated {
		if !reflect.DeepEqual(entry.ContextEstimate, want[i].ContextEstimate) {
			t.Fatalf("%s hydration changed estimate", entry.ID)
		}
	}
	if hydrated[4].Responses != nil || hydrated[4].ContextEstimate != nil {
		t.Fatal("remote checkpoint payload/estimate leaked")
	}
	after, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil || !bytes.Equal(after, raw.Bytes()) || bytes.Contains(after, []byte("contextEstimate")) {
		t.Fatal("derived estimates changed stored transcript")
	}
}
