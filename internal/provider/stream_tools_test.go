package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func sseDoer(body string) HTTPDoer {
	return roundTrip(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Header:     make(http.Header),
		}, nil
	})
}

func dataJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return "data: " + string(b) + "\n\n"
}

func mustTool(t *testing.T, m types.Message, name string) types.Content {
	t.Helper()
	calls := m.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: %+v", calls)
	}
	if calls[0].Name != name {
		t.Fatalf("name: %q", calls[0].Name)
	}
	return calls[0]
}

func TestLiveCompletionsAccumulatesFragmentedToolArgs(t *testing.T) {
	// Fragments that are not valid JSON alone — old upsertToolCall dropped these.
	sse := dataJSON(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "c1",
				"function": map[string]any{"name": "Read", "arguments": `{"file_path"`},
			}},
		}}},
	}) + dataJSON(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": `:"/tmp/a.txt"}`},
			}},
		}}},
	}) + dataJSON(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls"}},
	}) + "data: [DONE]\n"
	live := NewLive("completions", "https://api.deepseek.com", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "deepseek-chat"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m.StopReason != "toolUse" {
		t.Fatalf("stop: %s", m.StopReason)
	}
	c := mustTool(t, m, "Read")
	if c.ID != "c1" {
		t.Fatalf("id: %s", c.ID)
	}
	if c.Arguments["file_path"] != "/tmp/a.txt" {
		t.Fatalf("args: %+v", c.Arguments)
	}
}

func TestLiveAnthropicToolUseKeepsInputAndDeltas(t *testing.T) {
	sse := "event: content_block_start\n" + dataJSON(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use", "id": "tu1", "name": "Write",
			"input": map[string]any{"file_path": "/tmp/w.txt"},
		},
	}) + "event: message_delta\n" + dataJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
	})
	live := NewLive("anthropic", "https://api.anthropic.com", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "claude-sonnet-4-5"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c := mustTool(t, m, "Write")
	if c.Arguments["file_path"] != "/tmp/w.txt" {
		t.Fatalf("input dropped: %+v", c.Arguments)
	}

	sse2 := "event: content_block_start\n" + dataJSON(map[string]any{
		"type": "content_block_start",
		"content_block": map[string]any{
			"type": "tool_use", "id": "tu2", "name": "Edit", "input": map[string]any{},
		},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":"/tmp/e.txt"`},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `,"old_string":"a","new_string":"b"}`},
	}) + "event: message_delta\n" + dataJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
	})
	live2 := NewLive("anthropic", "https://api.anthropic.com", "k", sseDoer(sse2))
	m2, err := live2.Stream(context.Background(), loop.Request{Model: "claude-sonnet-4-5"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c2 := mustTool(t, m2, "Edit")
	if c2.Arguments["file_path"] != "/tmp/e.txt" || c2.Arguments["old_string"] != "a" {
		t.Fatalf("delta args: %+v", c2.Arguments)
	}
}

func TestLiveResponsesReconstructsFunctionCall(t *testing.T) {
	sse := "event: response.output_item.added\n" + dataJSON(map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"id": "fc_1", "type": "function_call", "name": "Bash", "call_id": "call_1", "arguments": "",
		},
	}) + "event: response.function_call_arguments.delta\n" + dataJSON(map[string]any{
		"type": "response.function_call_arguments.delta", "item_id": "fc_1", "delta": `{"command"`,
	}) + "event: response.function_call_arguments.delta\n" + dataJSON(map[string]any{
		"type": "response.function_call_arguments.delta", "item_id": "fc_1", "delta": `:"ls"}`,
	}) + "event: response.function_call_arguments.done\n" + dataJSON(map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "fc_1", "arguments": `{"command":"ls"}`,
	}) + "event: response.completed\n" + dataJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{"usage": map[string]any{"input_tokens": 4, "output_tokens": 2}},
	})
	live := NewLive("responses", "https://api.openai.com/v1", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "gpt-4o"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m.StopReason != "toolUse" {
		t.Fatalf("stop: %s", m.StopReason)
	}
	c := mustTool(t, m, "Bash")
	if c.ID != "call_1" {
		t.Fatalf("call id: %s", c.ID)
	}
	if c.Arguments["command"] != "ls" {
		t.Fatalf("args: %+v", c.Arguments)
	}
}

func TestLiveResponsesReconstructsCustomToolCall(t *testing.T) {
	sse := "event: response.output_item.added\n" + dataJSON(map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"id": "ct_1", "type": "custom_tool_call", "name": "apply_patch", "call_id": "call_1", "input": ""},
	}) + "event: response.custom_tool_call_input.delta\n" + dataJSON(map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "ct_1", "delta": "*** Begin ",
	}) + "event: response.custom_tool_call_input.delta\n" + dataJSON(map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "ct_1", "delta": "Patch",
	}) + "event: response.output_item.done\n" + dataJSON(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"id": "ct_1", "type": "custom_tool_call", "name": "apply_patch", "call_id": "call_1", "input": "*** Begin Patch"},
	}) + "event: response.completed\n" + dataJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{"usage": map[string]any{"input_tokens": 4, "output_tokens": 2}},
	})
	live := NewLive("responses", "https://api.openai.com/v1", "k", sseDoer(sse))
	var deltas []loop.AssistantDelta
	m, err := live.Stream(context.Background(), loop.Request{Model: "gpt-5.6-terra"}, func(delta loop.AssistantDelta) error { deltas = append(deltas, delta); return nil })
	if err != nil {
		t.Fatal(err)
	}
	c := mustTool(t, m, "apply_patch")
	if c.ToolType != "custom" || c.Input != "*** Begin Patch" || c.ID != "call_1" {
		t.Fatalf("custom call: %+v", c)
	}
	if len(deltas) != 2 || deltas[0].Type != "custom_tool_call_input_delta" || deltas[0].ToolCallID != "call_1" || deltas[0].ToolName != "apply_patch" {
		t.Fatalf("custom deltas: %+v", deltas)
	}
}
