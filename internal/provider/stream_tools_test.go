package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

func TestLiveCompletionsSupportsLegacyFunctionCall(t *testing.T) {
	sse := dataJSON(map[string]any{
		"id": "chatcmpl_legacy",
		"choices": []any{map[string]any{"delta": map[string]any{
			"function_call": map[string]any{"name": "Read"},
		}}},
	}) + dataJSON(map[string]any{
		"id": "chatcmpl_legacy",
		"choices": []any{map[string]any{"delta": map[string]any{
			"function_call": map[string]any{"arguments": `{"file_path":"/tmp/a"}`},
		}}},
	}) + dataJSON(map[string]any{
		"id":      "chatcmpl_legacy",
		"choices": []any{map[string]any{"finish_reason": "function_call"}},
	}) + "data: [DONE]\n"
	live := NewLive("completions", "https://api.example.test", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "legacy"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m.ResponseID != "chatcmpl_legacy" || m.StopReason != "toolUse" {
		t.Fatalf("legacy response: %+v", m)
	}
	c := mustTool(t, m, "Read")
	if c.Arguments["file_path"] != "/tmp/a" {
		t.Fatalf("legacy arguments: %+v", c.Arguments)
	}
}

func TestLiveCompletionsHandlesRefusalAndContentFilter(t *testing.T) {
	refusal := dataJSON(map[string]any{
		"id":      "chatcmpl_refusal",
		"choices": []any{map[string]any{"delta": map[string]any{"refusal": "I cannot help with that."}}},
	}) + dataJSON(map[string]any{
		"id":      "chatcmpl_refusal",
		"choices": []any{map[string]any{"finish_reason": "stop"}},
	}) + "data: [DONE]\n"
	live := NewLive("completions", "https://api.example.test", "k", sseDoer(refusal))
	m, err := live.Stream(context.Background(), loop.Request{Model: "refusal"}, func(loop.AssistantDelta) error { return nil })
	if err != nil || m.Text() != "I cannot help with that." || m.StopReason != "stop" {
		t.Fatalf("refusal: message=%+v err=%v", m, err)
	}

	filtered := dataJSON(map[string]any{
		"id":      "chatcmpl_filtered",
		"choices": []any{map[string]any{"finish_reason": "content_filter"}},
	}) + "data: [DONE]\n"
	live = NewLive("completions", "https://api.example.test", "k", sseDoer(filtered))
	m, err = live.Stream(context.Background(), loop.Request{Model: "filtered"}, func(loop.AssistantDelta) error { return nil })
	if err == nil || m.StopReason != "error" || !strings.Contains(m.ErrorMessage, "content filter") {
		t.Fatalf("content filter: message=%+v err=%v", m, err)
	}
}

func TestLiveAnthropicToolUseKeepsInputAndDeltas(t *testing.T) {
	sse := "event: content_block_start\n" + dataJSON(map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "tool_use", "id": "tu1", "name": "Write",
			"input": map[string]any{"file_path": "/tmp/w.txt"},
		},
	}) + "event: message_delta\n" + dataJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
	}) + "event: message_stop\n" + dataJSON(map[string]any{"type": "message_stop"})
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
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "tool_use", "id": "tu2", "name": "Edit", "input": map[string]any{},
		},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":"/tmp/e.txt"`},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `,"old_string":"a","new_string":"b"}`},
	}) + "event: message_delta\n" + dataJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
	}) + "event: message_stop\n" + dataJSON(map[string]any{"type": "message_stop"})
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

func TestLiveAnthropicIndexesBlocksAndPreservesThinkingState(t *testing.T) {
	sse := "event: message_start\n" + dataJSON(map[string]any{
		"type": "message_start", "message": map[string]any{"id": "msg_anthropic_1"},
	}) + "event: content_block_start\n" + dataJSON(map[string]any{
		"type": "content_block_start", "index": 1,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type": "content_block_delta", "index": 1,
		"delta": map[string]any{"type": "thinking_delta", "thinking": "private"},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type": "content_block_delta", "index": 1,
		"delta": map[string]any{"type": "signature_delta", "signature": "sig"},
	}) + "event: content_block_stop\n" + dataJSON(map[string]any{
		"type": "content_block_stop", "index": 1,
	}) + "event: content_block_start\n" + dataJSON(map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}) + "event: content_block_delta\n" + dataJSON(map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "answer"},
	}) + "event: content_block_stop\n" + dataJSON(map[string]any{
		"type": "content_block_stop", "index": 0,
	}) + "event: message_delta\n" + dataJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
	}) + "event: message_stop\n" + dataJSON(map[string]any{"type": "message_stop"})
	live := NewLive("anthropic", "https://api.anthropic.com", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "claude-sonnet-4-5"}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m.ResponseID != "msg_anthropic_1" || m.Text() != "answer" || m.StopReason != "stop" {
		t.Fatalf("Anthropic response: %+v", m)
	}
	if len(m.Content) != 2 || m.Content[0].Type != "text" || m.Content[1].Type != "thinking" {
		t.Fatalf("indexed content order: %+v", m.Content)
	}
	if m.Content[1].Thinking != "private" || m.Content[1].ThinkingSignature != "sig" {
		t.Fatalf("thinking state: %+v", m.Content[1])
	}
}

func TestLiveAnthropicReportsSSEError(t *testing.T) {
	sse := "event: error\n" + dataJSON(map[string]any{
		"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "overloaded"},
	})
	live := NewLive("anthropic", "https://api.anthropic.com", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "claude-sonnet-4-5"}, func(loop.AssistantDelta) error { return nil })
	if err == nil || m.StopReason != "error" || m.ErrorMessage != "overloaded" {
		t.Fatalf("Anthropic SSE error: message=%+v err=%v", m, err)
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

func TestLiveResponsesRequiresTerminalEvent(t *testing.T) {
	live := NewLive("responses", "https://api.openai.com/v1", "k", sseDoer(dataJSON(map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_1", "delta": "partial",
	})))
	_, err := live.Stream(context.Background(), loop.Request{Model: "gpt-5.6"}, func(loop.AssistantDelta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "terminal response event") {
		t.Fatalf("missing terminal event error: %v", err)
	}
}

func TestLiveResponsesReportsFailedEvent(t *testing.T) {
	sse := dataJSON(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": "resp_failed", "status": "failed",
			"error": map[string]any{"message": "model unavailable"},
		},
	})
	live := NewLive("responses", "https://api.openai.com/v1", "k", sseDoer(sse))
	m, err := live.Stream(context.Background(), loop.Request{Model: "gpt-5.6"}, func(loop.AssistantDelta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("failed response error: %v", err)
	}
	if m.StopReason != "error" || m.ErrorMessage != "model unavailable" || m.ResponseID != "resp_failed" {
		t.Fatalf("failed response message: %+v", m)
	}
}

func TestLiveResponsesReplaysOutputAndContentPartEvents(t *testing.T) {
	sse := "event: response.created\n" + dataJSON(map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp_1"},
	}) + "event: response.output_item.added\n" + dataJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant"},
	}) + "event: response.content_part.added\n" + dataJSON(map[string]any{
		"type": "response.content_part.added", "item_id": "msg_1", "output_index": 0,
		"part": map[string]any{"type": "output_text", "text": "hello"},
	}) + "event: response.completed\n" + dataJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed"},
	})
	live := NewLive("responses", "https://api.openai.com/v1", "k", sseDoer(sse))
	var deltas []loop.AssistantDelta
	m, err := live.Stream(context.Background(), loop.Request{Model: "gpt-5.6"}, func(delta loop.AssistantDelta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Text() != "hello" || m.ResponseID != "resp_1" || len(deltas) != 1 || deltas[0].Delta != "hello" {
		t.Fatalf("response: %+v deltas: %+v", m, deltas)
	}
	if m.Content[0].ItemID != "msg_1" || m.Content[0].TextSignature == "" {
		t.Fatalf("message replay metadata: %+v", m.Content[0])
	}
}
