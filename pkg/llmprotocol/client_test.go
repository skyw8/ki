package llmprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testNonRetryableError interface {
	NonRetryable() bool
}

type testDoer func(*http.Request) (*http.Response, error)

func (f testDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func protocolSSE(body string) HTTPDoer {
	return testDoer(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Header:     make(http.Header),
		}, nil
	})
}

func protocolJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return "data: " + string(b) + "\n\n"
}

func TestClientStreamsChatCompletionsToolArguments(t *testing.T) {
	sse := protocolJSON(map[string]any{
		"id": "chatcmpl_1", "choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "function": map[string]any{"name": "Read", "arguments": `{"path"`}}}}}},
	}) + protocolJSON(map[string]any{
		"id": "chatcmpl_1", "choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `:"/tmp/a"}`}}}}}},
	}) + protocolJSON(map[string]any{
		"id": "chatcmpl_1", "choices": []any{map[string]any{"finish_reason": "tool_calls"}},
	}) + "data: [DONE]\n"
	client := NewClient(APICompletions, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.ResponseID != "chatcmpl_1" || message.StopReason != "toolUse" {
		t.Fatalf("message: %+v", message)
	}
	calls := message.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments["path"] != "/tmp/a" {
		t.Fatalf("tool calls: %+v", calls)
	}
}

func TestClientStreamsAnthropicIndexedBlocks(t *testing.T) {
	sse := "event: message_start\n" + protocolJSON(map[string]any{
		"type": "message_start", "message": map[string]any{"id": "msg_1"},
	}) + "event: content_block_start\n" + protocolJSON(map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}) + "event: content_block_delta\n" + protocolJSON(map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "hello"},
	}) + "event: content_block_stop\n" + protocolJSON(map[string]any{
		"type": "content_block_stop", "index": 0,
	}) + "event: message_delta\n" + protocolJSON(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
	}) + "event: message_stop\n" + protocolJSON(map[string]any{"type": "message_stop"})
	client := NewClient(APIAnthropic, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.ResponseID != "msg_1" || message.Text() != "hello" || message.StopReason != "stop" {
		t.Fatalf("message: %+v", message)
	}
}

func TestClientStreamsResponsesFunctionCall(t *testing.T) {
	sse := "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "item": map[string]any{
			"id": "fc_1", "type": "function_call", "name": "Bash", "call_id": "call_1", "arguments": "",
		},
	}) + "event: response.function_call_arguments.delta\n" + protocolJSON(map[string]any{
		"type": "response.function_call_arguments.delta", "item_id": "fc_1", "call_id": "call_1", "name": "Bash", "delta": `{"command":"ls"}`,
	}) + "event: response.function_call_arguments.done\n" + protocolJSON(map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "fc_1", "call_id": "call_1", "name": "Bash", "arguments": `{"command":"ls"}`,
	}) + "event: response.completed\n" + protocolJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed"},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.ResponseID != "resp_1" || message.StopReason != "toolUse" {
		t.Fatalf("message: %+v", message)
	}
	if calls := message.ToolCalls(); len(calls) != 1 || calls[0].Name != "Bash" || calls[0].Arguments["command"] != "ls" {
		t.Fatalf("tool calls: %+v", calls)
	}
}

func TestClientStreamsResponsesMessageWithoutItemID(t *testing.T) {
	// Some OpenAI-compatible gateways omit the message item's id but keep the
	// output index. Text remains usable and can acquire the id from a later
	// delta without weakening tool/reasoning item validation.
	sse := "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "message", "role": "assistant"},
	}) + "event: response.output_text.delta\n" + protocolJSON(map[string]any{
		"type": "response.output_text.delta", "output_index": 0, "delta": "pong",
	}) + "event: response.completed\n" + protocolJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "pong"}},
			}},
		},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.ResponseID != "resp_1" || message.Text() != "pong" || message.StopReason != "stop" {
		t.Fatalf("message: %+v", message)
	}
	if len(message.Content) != 1 || message.Content[0].ItemID != "" {
		t.Fatalf("missing-id message was not kept as one text block: %+v", message.Content)
	}
}

func TestClientBindsResponsesTextIDAfterMissingOutputID(t *testing.T) {
	sse := "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "message", "role": "assistant"},
	}) + "event: response.output_text.delta\n" + protocolJSON(map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_1", "delta": "hello",
	}) + "event: response.completed\n" + protocolJSON(map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed"},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 1 || message.Content[0].ItemID != "msg_1" || message.Text() != "hello" {
		t.Fatalf("text item was not rebound: %+v", message.Content)
	}
	if !strings.Contains(message.Content[0].TextSignature, `"id":"msg_1"`) {
		t.Fatalf("missing replay id in signature: %q", message.Content[0].TextSignature)
	}
}

func TestClientSendsSSEAcceptHeader(t *testing.T) {
	var accept string
	client := NewClient(APICompletions, "https://example.test", "key", testDoer(func(r *http.Request) (*http.Response, error) {
		accept = r.Header.Get("Accept")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: [DONE]\n")), Header: make(http.Header)}, nil
	}))
	if _, err := client.Stream(context.Background(), Request{Model: "model"}, nil); err != nil {
		t.Fatal(err)
	}
	if accept != "text/event-stream" {
		t.Fatalf("Accept: %q", accept)
	}
}

func TestClientMarksDeterministicHTTPFailureNonRetryable(t *testing.T) {
	client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid tools"}}`)),
			Header:     make(http.Header),
		}, nil
	}))
	_, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	var marker testNonRetryableError
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatalf("400 error must be non-retryable: %v", err)
	}
}

func TestClientKeepsTransientHTTPFailureRetryable(t *testing.T) {
	client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`)),
			Header:     make(http.Header),
		}, nil
	}))
	_, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	var marker testNonRetryableError
	if err == nil || errors.As(err, &marker) {
		t.Fatalf("429 error must remain retryable: %v", err)
	}
}

func TestClientRetriesGatewayBadResponseStatus(t *testing.T) {
	client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"openai_error","type":"bad_response_status_code","code":"bad_response_status_code"}}`,
			)),
			Header: make(http.Header),
		}, nil
	}))
	_, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	var marker testNonRetryableError
	if err == nil || errors.As(err, &marker) {
		t.Fatalf("transient gateway 400 must remain retryable: %v", err)
	}
}

func TestResponsesStreamWithoutTerminalEventIsNonRetryable(t *testing.T) {
	sse := "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "message", "role": "assistant"},
	}) + "event: response.output_text.delta\n" + protocolJSON(map[string]any{
		"type": "response.output_text.delta", "output_index": 0, "delta": "partial",
	}) + "data: [DONE]\n"
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	_, err := client.Stream(context.Background(), Request{Model: "model"}, nil)
	var marker testNonRetryableError
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatalf("malformed terminal sequence must be non-retryable: %v", err)
	}
}
