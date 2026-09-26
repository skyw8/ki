package llmprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// customCall is a Responses-style freeform tool call: identity in ID/Name and
// the opaque payload in Input, with no JSON arguments.
func customCall() Content {
	return Content{Type: "toolCall", ToolType: "custom", ID: "call_1", Name: "apply_patch", Input: "*** Begin Patch"}
}

func customHistory() []Message {
	return []Message{
		{Role: "assistant", Content: []Content{customCall()}},
		{Role: "toolResult", ToolType: "custom", ToolCallID: "call_1", ToolName: "apply_patch", Content: []Content{{Type: "text", Text: "ok"}}},
	}
}

func TestCompletionsReplayDowngradesCustomToolCall(t *testing.T) {
	history := customHistory()
	body := CompletionsBody(Request{Model: "deepseek-flash", Messages: history})
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("messages: %+v", msgs)
	}
	calls := msgs[0]["tool_calls"].([]map[string]any)
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "apply_patch" {
		t.Fatalf("function name: %+v", fn)
	}
	if fn["arguments"] != `{"input":"*** Begin Patch"}` {
		t.Fatalf("function arguments: %+v", fn)
	}
	if calls[0]["id"] != "call_1" {
		t.Fatalf("call id: %+v", calls[0])
	}
	if msgs[1]["role"] != "tool" || msgs[1]["tool_call_id"] != "call_1" {
		t.Fatalf("tool result: %+v", msgs[1])
	}
	// The caller's history must stay a custom call: only the encoder rewrites it.
	if history[0].Content[0].ToolType != "custom" || history[0].Content[0].Input != "*** Begin Patch" {
		t.Fatalf("history mutated: %+v", history[0].Content[0])
	}
}

func TestAnthropicReplayDowngradesCustomToolCall(t *testing.T) {
	body := AnthropicBody(Request{Model: "claude", Messages: customHistory()})
	msgs := body["messages"].([]map[string]any)
	blocks := msgs[0]["content"].([]map[string]any)
	if blocks[0]["type"] != "tool_use" || blocks[0]["name"] != "apply_patch" {
		t.Fatalf("tool_use block: %+v", blocks[0])
	}
	input := blocks[0]["input"].(map[string]any)
	if input["input"] != "*** Begin Patch" {
		t.Fatalf("tool_use input: %+v", input)
	}
}

func TestStructuredReplayDropsOrphanToolResult(t *testing.T) {
	body := CompletionsBody(Request{Model: "deepseek-flash", Messages: []Message{
		{Role: "user", Content: []Content{{Type: "text", Text: "hi"}}},
		{Role: "toolResult", ToolCallID: "ghost", Content: []Content{{Type: "text", Text: "orphan"}}},
	}})
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("orphan result leaked into request: %+v", msgs)
	}
}

func TestCustomToolCallRequestStreamsOnCompletions(t *testing.T) {
	sse := protocolJSON(map[string]any{
		"id": "chatcmpl_1", "choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}},
	}) + protocolJSON(map[string]any{
		"id": "chatcmpl_1", "choices": []any{map[string]any{"finish_reason": "stop"}},
	}) + "data: [DONE]\n"
	client := NewClient(APICompletions, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "deepseek-flash", Messages: customHistory()}, nil)
	if err != nil {
		t.Fatalf("custom call history must stream: %v", err)
	}
	if message.StopReason != "stop" {
		t.Fatalf("message: %+v", message)
	}
}

func TestCompletionsValidationFailureIsNonRetryable(t *testing.T) {
	called := false
	client := NewClient(APICompletions, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not reach the network")
	}))
	_, err := client.Stream(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: "assistant", Content: []Content{{Type: "toolCall", ID: "call_1"}}},
	}}, nil)
	var marker testNonRetryableError
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatalf("deterministic request failure must be non-retryable: %v", err)
	}
	if called {
		t.Fatal("request validation failure must not reach the network")
	}
}

func TestAnthropicValidationFailureIsNonRetryable(t *testing.T) {
	called := false
	client := NewClient(APIAnthropic, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not reach the network")
	}))
	_, err := client.Stream(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: "assistant", Content: []Content{{Type: "toolCall", ID: "call_1"}}},
	}}, nil)
	var marker testNonRetryableError
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatalf("deterministic request failure must be non-retryable: %v", err)
	}
	if called {
		t.Fatal("request validation failure must not reach the network")
	}
}

// TestCustomToolCallArgumentsAreJSON pins the exact wire shape the downgrade
// produces, since a non-object argument string is rejected by gateways.
func TestCustomToolCallArgumentsAreJSON(t *testing.T) {
	raw := customCallArguments(Content{Type: "toolCall", ToolType: "custom", Input: "line1\nline2"})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("arguments are not a JSON object: %q", raw)
	}
	if parsed["input"] != "line1\nline2" {
		t.Fatalf("arguments: %+v", parsed)
	}
}
