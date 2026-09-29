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

func TestCompactResponsesPreservesCanonicalWindowAndUsage(t *testing.T) {
	prefix := ResponsesWindow{
		ResponsesItem(`{"type":"compaction","id":"cmp_old","encrypted_content":"old","unknown":{"keep":true}}`),
		ResponsesItem(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"retained"}]}`),
	}
	var gotPath, gotAccept, gotAuthorization string
	var gotBody []byte
	client := NewClient(APIResponses, "https://example.test/v1", "secret", testDoer(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotAuthorization = r.Header.Get("Authorization")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"id":"cmp_response_1",
				"output":[
					{"type":"message","role":"assistant","content":[{"type":"output_text","text":"retained"}],"future_field":{"x":1}},
					{"type":"compaction","id":"cmp_new","encrypted_content":"cipher","future_array":[1,{"z":true}],"future_integer":900719925474099312345}
				],
				"usage":{
					"input_tokens":120,
					"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":3},
					"output_tokens":7,
					"total_tokens":127
				}
			}`)),
		}, nil
	}))

	result, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{
		Model:        "gpt-test",
		Instructions: "system instructions",
		Window:       prefix,
		Messages: []Message{{
			Role:    "user",
			Content: []Content{{Type: "text", Text: "continue"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path: %q", gotPath)
	}
	if gotAccept != "application/json" || gotAuthorization != "Bearer secret" {
		t.Fatalf("headers: Accept=%q Authorization=%q", gotAccept, gotAuthorization)
	}
	var request struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Input        []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(gotBody, &request); err != nil {
		t.Fatalf("request JSON: %v\n%s", err, gotBody)
	}
	if request.Model != "gpt-test" || request.Instructions != "system instructions" || len(request.Input) != 3 {
		t.Fatalf("request: %+v", request)
	}
	var requestFields map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &requestFields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stream", "store", "include", "tools"} {
		if _, ok := requestFields[forbidden]; ok {
			t.Fatalf("standalone compact request unexpectedly contains %q: %s", forbidden, gotBody)
		}
	}
	assertJSONEqual(t, request.Input[0], prefix[0])
	assertJSONEqual(t, request.Input[1], prefix[1])
	var user map[string]any
	if err := json.Unmarshal(request.Input[2], &user); err != nil {
		t.Fatal(err)
	}
	if user["type"] != "message" || user["role"] != "user" {
		t.Fatalf("converted suffix: %+v", user)
	}

	if result.ID != "cmp_response_1" || len(result.Output) != 2 {
		t.Fatalf("result: %+v", result)
	}
	if result.Output[0].Type() != "message" || result.Output[1].Type() != "compaction" {
		t.Fatalf("output order/types: %q, %q", result.Output[0].Type(), result.Output[1].Type())
	}
	var second map[string]any
	if err := json.Unmarshal(result.Output[1].RawJSON(), &second); err != nil {
		t.Fatal(err)
	}
	if second["encrypted_content"] != "cipher" || second["future_array"] == nil {
		t.Fatalf("opaque item fields lost: %+v", second)
	}
	if !bytes.Contains(result.Output[1].RawJSON(), []byte(`"future_integer":900719925474099312345`)) {
		t.Fatalf("opaque numeric JSON changed: %s", result.Output[1])
	}
	if result.Usage == nil ||
		result.Usage.Input != 120 ||
		result.Usage.Output != 7 ||
		result.Usage.CacheRead != 20 ||
		result.Usage.CacheWrite != 3 ||
		result.Usage.TotalTokens != 127 {
		t.Fatalf("usage: %+v", result.Usage)
	}
}

func TestResponsesBodyPrependsWindowAndEnablesServerCompaction(t *testing.T) {
	window := ResponsesWindow{
		ResponsesItem(`{"type":"compaction","id":"cmp_1","encrypted_content":"cipher","unknown":"preserved"}`),
		ResponsesItem(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"tail"}]}`),
	}
	body := ResponsesBody(Request{
		Model:                     "gpt-test",
		ResponsesWindow:           window,
		ResponsesCompactThreshold: 200000,
		Messages: []Message{{
			Role:    "user",
			Content: []Content{{Type: "text", Text: "next"}},
		}},
	})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input             []json.RawMessage `json:"input"`
		ContextManagement []struct {
			Type             string `json:"type"`
			CompactThreshold int    `json:"compact_threshold"`
		} `json:"context_management"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Input) != 3 {
		t.Fatalf("input length: %d", len(wire.Input))
	}
	assertJSONEqual(t, wire.Input[0], window[0])
	assertJSONEqual(t, wire.Input[1], window[1])
	if len(wire.ContextManagement) != 1 ||
		wire.ContextManagement[0].Type != "compaction" ||
		wire.ContextManagement[0].CompactThreshold != 200000 {
		t.Fatalf("context_management: %+v", wire.ContextManagement)
	}
}

func TestResponsesStreamCapturesAndReplaysServerCompaction(t *testing.T) {
	compaction := map[string]any{
		"type": "compaction", "id": "cmp_1", "encrypted_content": "cipher",
		"future": map[string]any{"preserved": true},
	}
	sse := "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "compaction", "id": "cmp_1"},
	}) + "event: response.output_item.done\n" + protocolJSON(map[string]any{
		"type": "response.output_item.done", "output_index": 0, "item": compaction,
	}) + "event: response.output_item.added\n" + protocolJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 1,
		"item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant"},
	}) + "event: response.output_text.delta\n" + protocolJSON(map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 1, "delta": "done",
	}) + "event: response.completed\n" + protocolJSON(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{
				compaction,
				map[string]any{
					"type": "message", "id": "msg_1", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": "done"}},
				},
			},
		},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{
		Model: "gpt-test", ResponsesCompactThreshold: 1000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.Text() != "done" || len(message.ResponsesItems) != 2 {
		t.Fatalf("message: %+v", message)
	}
	if message.ResponsesItems[0].Type() != "compaction" {
		t.Fatalf("captured item: %s", message.ResponsesItems[0])
	}
	var opaque map[string]any
	if err := json.Unmarshal(message.ResponsesItems[0], &opaque); err != nil {
		t.Fatal(err)
	}
	if opaque["encrypted_content"] != "cipher" || opaque["future"] == nil {
		t.Fatalf("captured fields: %+v", opaque)
	}

	replay := ResponsesBody(Request{
		Model: "gpt-test",
		ResponsesWindow: ResponsesWindow{ResponsesItem(
			`{"type":"compaction","encrypted_content":"superseded"}`)},
		Messages: []Message{
			{Role: "user", Content: []Content{{Type: "text", Text: "old history"}}},
			message,
			{Role: "user", Content: []Content{{Type: "text", Text: "continue"}}},
		},
	})
	raw, err := json.Marshal(replay["input"])
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0]["type"] != "compaction" || items[1]["type"] != "message" || items[2]["role"] != "user" {
		t.Fatalf("replayed items: %+v", items)
	}
}

func TestResponsesStreamCapturesCompactionFromTerminalResponseOnly(t *testing.T) {
	sse := "event: response.completed\n" + protocolJSON(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{
				"type": "compaction", "encrypted_content": "terminal-only", "future": "kept",
			}},
		},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "gpt-test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ResponsesItems) != 1 || message.ResponsesItems[0].Type() != "compaction" {
		t.Fatalf("terminal compaction was not captured: %+v", message.ResponsesItems)
	}
	if !bytes.Contains(message.ResponsesItems[0], []byte(`"future":"kept"`)) {
		t.Fatalf("terminal compaction fields lost: %s", message.ResponsesItems[0])
	}
}

func TestResponsesStreamDoesNotPromoteIncompleteCompaction(t *testing.T) {
	sse := "event: response.incomplete\n" + protocolJSON(map[string]any{
		"type": "response.incomplete",
		"response": map[string]any{
			"id": "resp_1", "status": "incomplete",
			"output": []any{map[string]any{
				"type": "compaction", "encrypted_content": "not-canonical",
			}},
		},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "gpt-test"}, nil)
	if err == nil {
		t.Fatal("incomplete response should fail")
	}
	if len(message.ResponsesItems) != 0 {
		t.Fatalf("incomplete response replaced context: %+v", message.ResponsesItems)
	}
}

func TestResponsesDoneDoesNotPromoteFlatIncompleteCompaction(t *testing.T) {
	sse := "event: response.done\n" + protocolJSON(map[string]any{
		"type":   "response.done",
		"status": "incomplete",
		"incomplete_details": map[string]any{
			"reason": "max_output_tokens",
		},
		"output": []any{map[string]any{
			"type": "compaction", "encrypted_content": "not-canonical",
		}},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "gpt-test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != "length" {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if len(message.ResponsesItems) != 0 {
		t.Fatalf("flat incomplete response replaced context: %+v", message.ResponsesItems)
	}
}

func TestResponsesStreamDoesNotPromoteCompactionBeforeTerminal(t *testing.T) {
	sse := "event: response.output_item.done\n" + protocolJSON(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "compaction", "encrypted_content": "early"},
	})
	client := NewClient(APIResponses, "https://example.test", "key", protocolSSE(sse))
	message, err := client.Stream(context.Background(), Request{Model: "gpt-test"}, nil)
	if err == nil {
		t.Fatal("early EOF should fail")
	}
	if len(message.ResponsesItems) != 0 {
		t.Fatalf("pre-terminal item replaced context: %+v", message.ResponsesItems)
	}
}

func TestServerCompactionKeepsRawSuffixFromLastItem(t *testing.T) {
	last := `{"type":"compaction","id":"cmp_2","encrypted_content":"new","future":{"n":9007199254740993}}`
	message := `{"type":"message","id":"msg_2","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"status","annotations":[{"future":true}]}]}`
	unknown := `{"type":"future_output","payload":{"kept":true}}`
	data := strings.TrimPrefix(strings.TrimSpace(protocolJSON(map[string]any{
		"type": "response.completed",
		"response": json.RawMessage(`{"status":"completed","output":[` +
			`{"type":"compaction","encrypted_content":"old"},` +
			`{"type":"message","role":"assistant","content":[]},` +
			last + `,` + message + `,` + unknown + `]}`),
	})), "data: ")
	window := rawResponsesCompactionWindow([]byte(data), "response.completed")
	if len(window) != 3 {
		t.Fatalf("window = %d: %s", len(window), data)
	}
	if string(window[0]) != last || string(window[1]) != message || string(window[2]) != unknown {
		t.Fatalf("raw suffix changed:\n%s\n%s\n%s", window[0], window[1], window[2])
	}
}

func TestCompactResponsesHTTPAndContextErrors(t *testing.T) {
	t.Run("deterministic HTTP error", func(t *testing.T) {
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid input"}}`)),
			}, nil
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{Model: "gpt-test"})
		var marker testNonRetryableError
		if !errors.As(err, &marker) || !marker.NonRetryable() {
			t.Fatalf("400 must be non-retryable: %v", err)
		}
	})

	t.Run("transient HTTP error", func(t *testing.T) {
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`)),
			}, nil
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{Model: "gpt-test"})
		var marker testNonRetryableError
		if err == nil || errors.As(err, &marker) {
			t.Fatalf("429 must remain retryable: %v", err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		}))
		_, err := client.CompactResponses(ctx, ResponsesCompactRequest{Model: "gpt-test"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("context cancellation lost: %v", err)
		}
	})

	t.Run("empty canonical output", func(t *testing.T) {
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"output":[]}`)),
			}, nil
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{Model: "gpt-test"})
		var marker testNonRetryableError
		if !errors.As(err, &marker) || !marker.NonRetryable() {
			t.Fatalf("empty output must be deterministic: %v", err)
		}
	})

	t.Run("output without compaction item", func(t *testing.T) {
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"output":[{"type":"message","role":"assistant","content":[]}]}`,
				)),
			}, nil
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{Model: "gpt-test"})
		var marker testNonRetryableError
		if !errors.As(err, &marker) || !marker.NonRetryable() {
			t.Fatalf("missing compaction item must be deterministic: %v", err)
		}
	})
}

func TestCompactResponsesRejectsMalformedCanonicalData(t *testing.T) {
	t.Run("invalid request item", func(t *testing.T) {
		called := false
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("must not reach network")
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{
			Model:  "gpt-test",
			Window: ResponsesWindow{ResponsesItem(`["not","an","object"]`)},
		})
		var marker testNonRetryableError
		if !errors.As(err, &marker) || !marker.NonRetryable() || called {
			t.Fatalf("invalid input: err=%v called=%v", err, called)
		}
	})

	t.Run("invalid response item", func(t *testing.T) {
		client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"output":[42]}`)),
			}, nil
		}))
		_, err := client.CompactResponses(context.Background(), ResponsesCompactRequest{Model: "gpt-test"})
		var marker testNonRetryableError
		if !errors.As(err, &marker) || !marker.NonRetryable() {
			t.Fatalf("invalid response must be non-retryable: %v", err)
		}
	})
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	decoder := json.NewDecoder(bytes.NewReader(got))
	decoder.UseNumber()
	if err := decoder.Decode(&gotValue); err != nil {
		t.Fatalf("decode got JSON: %v: %s", err, got)
	}
	decoder = json.NewDecoder(bytes.NewReader(want))
	decoder.UseNumber()
	if err := decoder.Decode(&wantValue); err != nil {
		t.Fatalf("decode want JSON: %v: %s", err, want)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("JSON mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}
