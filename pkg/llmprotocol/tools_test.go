package llmprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func functionEnvelope(name, field string) ToolSpec {
	return ToolSpec{Type: "function", Name: name, Parameters: map[string]any{
		"type": "object", "required": []any{field},
		"properties": map[string]any{field: map[string]any{"type": "string"}},
	}}
}

func protocolBody(api string, req Request) map[string]any {
	switch api {
	case APIResponses:
		return ResponsesBody(req)
	case APIAnthropic:
		return AnthropicBody(req)
	default:
		return CompletionsBody(req)
	}
}

func TestToolReplayFunctionEnvelopeMatrix(t *testing.T) {
	for _, api := range []string{APICompletions, APIResponses, APIAnthropic} {
		for _, field := range []string{"input", "code"} {
			for _, custom := range []bool{false, true} {
				t.Run(api+"/"+field+map[bool]string{true: "/custom", false: "/function"}[custom], func(t *testing.T) {
					source := "text(\"你好\");\n// \"quoted\"\\path"
					call := Content{Type: "toolCall", ID: "call_1", Name: "orchestrate", ItemID: "item_1",
						Arguments: map[string]any{field: "stale"}, ArgumentsRaw: marshalArguments(map[string]any{field: source})}
					kind := ""
					if custom {
						call.ToolType, call.Input = "custom", source
						kind = "custom"
					}
					history := []Message{
						{Role: "assistant", Content: []Content{call}},
						{Role: "toolResult", ToolType: kind, ToolCallID: "call_1", ToolName: call.Name,
							Content: []Content{{Type: "text", Text: "done"}}},
					}
					before, _ := json.Marshal(history)
					body := protocolBody(api, Request{Tools: []ToolSpec{functionEnvelope(call.Name, field)}, Messages: history})
					var arguments map[string]any
					switch api {
					case APICompletions:
						tools := body["tools"].([]map[string]any)
						if tools[0]["type"] != "function" {
							t.Fatalf("tool declaration: %+v", tools)
						}
						messages := body["messages"].([]map[string]any)
						calls := messages[0]["tool_calls"].([]map[string]any)
						fn := calls[0]["function"].(map[string]any)
						_ = json.Unmarshal([]byte(fn["arguments"].(string)), &arguments)
						if calls[0]["id"] != call.ID || fn["name"] != call.Name ||
							messages[1]["tool_call_id"] != call.ID || messages[1]["content"] != "done" {
							t.Fatalf("call/output identity: %+v", messages)
						}
					case APIAnthropic:
						tools := body["tools"].([]map[string]any)
						if !reflect.DeepEqual(tools[0]["input_schema"], functionEnvelope(call.Name, field).Parameters) {
							t.Fatalf("tool declaration: %+v", tools)
						}
						messages := body["messages"].([]map[string]any)
						block := messages[0]["content"].([]map[string]any)[0]
						result := messages[1]["content"].([]map[string]any)[0]
						arguments = block["input"].(map[string]any)
						if block["id"] != call.ID || block["name"] != call.Name ||
							result["tool_use_id"] != call.ID || result["content"] != "done" {
							t.Fatalf("call/output identity: %+v", messages)
						}
					case APIResponses:
						tools := body["tools"].([]map[string]any)
						if tools[0]["type"] != "function" {
							t.Fatalf("tool declaration: %+v", tools)
						}
						items := body["input"].([]any)
						c := items[0].(map[string]any)
						result := items[1].(map[string]any)
						_ = json.Unmarshal([]byte(c["arguments"].(string)), &arguments)
						if c["type"] != "function_call" || c["call_id"] != call.ID || c["name"] != call.Name ||
							c["id"] != call.ItemID || result["type"] != "function_call_output" ||
							result["call_id"] != call.ID || result["output"] != "done" {
							t.Fatalf("call/output identity: %+v", items)
						}
					}
					if !reflect.DeepEqual(arguments, map[string]any{field: source}) {
						t.Fatalf("arguments: %+v", arguments)
					}
					after, _ := json.Marshal(history)
					if string(before) != string(after) {
						t.Fatal("caller history was mutated")
					}
				})
			}
		}
	}
}

func TestResponsesNativeCustomToolRoundTrip(t *testing.T) {
	tool := ToolSpec{Type: "custom", Name: "apply_patch", Format: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH"}}
	body := ResponsesBody(Request{Tools: []ToolSpec{tool}, Messages: customHistory()})
	declaration := body["tools"].([]map[string]any)[0]
	items := body["input"].([]any)
	call, result := items[0].(map[string]any), items[1].(map[string]any)
	if declaration["type"] != "custom" || declaration["format"] != tool.Format ||
		call["type"] != "custom_tool_call" || call["input"] != customCall().Input ||
		result["type"] != "custom_tool_call_output" || result["call_id"] != call["call_id"] {
		t.Fatalf("custom round trip: %+v", body)
	}
}

func TestResponsesHistoricalFunctionRetainedWithCustomDeclaration(t *testing.T) {
	source := "text('kept as a function');"
	call := Content{Type: "toolCall", ID: "call_1", Name: "exec", ArgumentsRaw: marshalArguments(map[string]any{"code": source})}
	body := ResponsesBody(Request{
		Tools: []ToolSpec{{Type: "custom", Name: "exec", Format: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: SOURCE"}}},
		Messages: []Message{
			{Role: "assistant", Content: []Content{call}},
			{Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name, Content: []Content{{Type: "text", Text: "done"}}},
		},
	})
	items := body["input"].([]any)
	item, output := items[0].(map[string]any), items[1].(map[string]any)
	// Historical items describe what actually happened; a current custom
	// declaration does not require rewriting prior JSON calls into raw input.
	if item["type"] != "function_call" || item["arguments"] != call.ArgumentsRaw ||
		item["name"] != call.Name || item["call_id"] != call.ID ||
		output["type"] != "function_call_output" || output["call_id"] != call.ID {
		t.Fatalf("historical function changed: %+v", items)
	}
}

func TestResponsesOutputTypesFollowActualCalls(t *testing.T) {
	input := []any{
		map[string]any{"type": "function_call", "call_id": "fn", "name": "read", "arguments": "{}"},
		map[string]any{"type": "custom_tool_call", "call_id": "raw", "name": "apply_patch", "input": "patch"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "fn", "output": "read result"},
		map[string]any{"type": "function_call_output", "call_id": "raw", "output": "patch result"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "unpaired", "output": "orphan"},
	}
	before, _ := json.Marshal(input)
	output, err := adaptResponsesToolReplay(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, typ := range map[int]string{2: "function_call_output", 3: "custom_tool_call_output", 4: "custom_tool_call_output"} {
		item := output[index].(map[string]any)
		original := input[index].(map[string]any)
		if item["type"] != typ || item["call_id"] != original["call_id"] || item["output"] != original["output"] {
			t.Fatalf("output identity/payload changed: %+v", item)
		}
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("output repair mutated caller-owned items")
	}
}

func TestResponsesCanonicalCustomReplayUsesCurrentFunction(t *testing.T) {
	window := ResponsesWindow{
		ResponsesItem(`{"type":"compaction","encrypted_content":"opaque"}`),
		ResponsesItem(`{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"orchestrate","input":"","status":"completed"}`),
		ResponsesItem(`{"type":"custom_tool_call_output","call_id":"call_1","output":"done"}`),
	}
	before, _ := json.Marshal(window)
	body := ResponsesBody(Request{Tools: []ToolSpec{functionEnvelope("orchestrate", "code")}, ResponsesWindow: window})
	items := body["input"].([]any)
	if !reflect.DeepEqual(items[0], window[0]) {
		t.Fatal("opaque compaction item was changed")
	}
	call := items[1].(map[string]any)
	output := items[2].(map[string]any)
	if call["arguments"] != `{"code":""}` || call["type"] != "function_call" ||
		call["id"] != "item_1" || call["status"] != "completed" ||
		output["type"] != "function_call_output" || output["call_id"] != call["call_id"] {
		t.Fatalf("canonical custom conversion: %+v", items)
	}
	if _, exists := call["input"]; exists {
		t.Fatal("custom input field survived function conversion")
	}
	after, _ := json.Marshal(window)
	if string(before) != string(after) {
		t.Fatal("caller window was mutated")
	}
	// Server-side compaction retains canonical items on the assistant message
	// rather than the prefix; those items must follow the same adaptation.
	body = ResponsesBody(Request{Tools: []ToolSpec{functionEnvelope("orchestrate", "code")},
		Messages: []Message{{Role: "assistant", ResponsesItems: window}}})
	if !reflect.DeepEqual(body["input"], items) {
		t.Fatalf("canonical message differs from window replay: %+v", body["input"])
	}
}

func TestUnsupportedToolShapesFailBeforeNetwork(t *testing.T) {
	for _, api := range []string{APICompletions, APIResponses, APIAnthropic} {
		for _, typ := range []string{"custom", "unknown"} {
			if api == APIResponses && typ == "custom" {
				continue
			}
			t.Run(api+"/"+typ, func(t *testing.T) {
				client := NewClient(api, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
					t.Fatal("unsupported declaration reached the network")
					return nil, nil
				}))
				message, err := client.Stream(context.Background(), Request{Tools: []ToolSpec{{Name: "orchestrate", Type: typ}}}, nil)
				var marker testNonRetryableError
				if !errors.As(err, &marker) || !marker.NonRetryable() || message.StopReason != "error" {
					t.Fatalf("unsupported tool shape must be non-retryable: %+v, %v", message, err)
				}
			})
		}
	}
}

func TestAmbiguousCustomReplayEnvelopeFailsBeforeNetwork(t *testing.T) {
	for _, api := range []string{APICompletions, APIResponses, APIAnthropic} {
		t.Run(api, func(t *testing.T) {
			tool := functionEnvelope("apply_patch", "number")
			tool.Parameters["properties"].(map[string]any)["number"] = map[string]any{"type": "integer"}
			client := NewClient(api, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
				t.Fatal("ambiguous raw-to-function mapping reached the network")
				return nil, nil
			}))
			_, err := client.Stream(context.Background(), Request{Tools: []ToolSpec{tool}, Messages: customHistory()}, nil)
			var marker testNonRetryableError
			if !errors.As(err, &marker) || !marker.NonRetryable() {
				t.Fatalf("ambiguous replay must be non-retryable: %v", err)
			}
		})
	}
}

func TestResponsesCanonicalReplayAdaptationFailureIsNonRetryable(t *testing.T) {
	for _, invalidInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "schema", true: "input"}[invalidInput], func(t *testing.T) {
			tool := functionEnvelope("orchestrate", "code")
			item := ResponsesItem(`{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"orchestrate","input":"raw"}`)
			if invalidInput {
				item = ResponsesItem(`{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"orchestrate","input":123}`)
			} else {
				tool.Parameters["required"] = []any{"code", "other"}
			}
			window := ResponsesWindow{
				ResponsesItem(`{"type":"compaction","encrypted_content":"opaque"}`),
				item,
				ResponsesItem(`{"type":"custom_tool_call_output","call_id":"call_1","output":"done"}`),
			}
			before, _ := json.Marshal(window)
			client := NewClient(APIResponses, "https://example.test", "key", testDoer(func(*http.Request) (*http.Response, error) {
				t.Fatal("unrepresentable canonical replay reached the network")
				return nil, nil
			}))
			message, err := client.Stream(context.Background(), Request{Tools: []ToolSpec{tool}, ResponsesWindow: window}, nil)
			var marker testNonRetryableError
			if !errors.As(err, &marker) || !marker.NonRetryable() || message.StopReason != "error" {
				t.Fatalf("canonical adaptation failure must be non-retryable: %+v, %v", message, err)
			}
			after, _ := json.Marshal(window)
			if string(before) != string(after) {
				t.Fatal("failed adaptation changed canonical state")
			}
		})
	}
}

func TestCustomReplayPreservesEmptyInput(t *testing.T) {
	for _, api := range []string{APICompletions, APIAnthropic} {
		t.Run(api, func(t *testing.T) {
			history := customHistory()
			history[0].Content[0].Input = ""
			replayed := structuredToolReplay(history, nil)
			if replayed[0].Content[0].ArgumentsRaw != `{"input":""}` {
				t.Fatalf("empty raw input was lost: %+v", replayed)
			}
		})
	}
}
