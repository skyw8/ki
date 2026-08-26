package extension

import (
	"testing"

	"ki/internal/types"
)

func TestProviderStreamAccumulatorRebuildsToolArguments(t *testing.T) {
	var message types.Message
	if !applyProviderStreamEvent(&message, ProviderStreamEvent{
		Type: "toolcall_start", ContentIndex: 0, ToolCallID: "call-1", ToolName: "run",
		ToolCall: &types.Content{Type: "toolCall", ToolType: "function"},
	}) {
		t.Fatal("toolcall_start was not applied")
	}
	if !applyProviderStreamEvent(&message, ProviderStreamEvent{Type: "toolcall_delta", ContentIndex: 0, Delta: `{"command":"`}) ||
		!applyProviderStreamEvent(&message, ProviderStreamEvent{Type: "toolcall_delta", ContentIndex: 0, Delta: `go test"}`}) {
		t.Fatal("toolcall deltas were not applied")
	}
	call := message.ToolCalls()
	if len(call) != 1 || call[0].ID != "call-1" || call[0].Name != "run" || call[0].Arguments["command"] != "go test" {
		t.Fatalf("accumulated call=%+v", call)
	}
	if call[0].ArgumentsRaw != `{"command":"go test"}` {
		t.Fatalf("raw arguments=%q", call[0].ArgumentsRaw)
	}

	mergeProviderMessage(&message, types.Message{
		Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "call-1", Name: "run", ToolType: "function", Arguments: map[string]any{"command": "go test"}}},
	})
	if got := message.Content[0].ArgumentsRaw; got != `{"command":"go test"}` {
		t.Fatalf("final message lost streamed raw arguments: %q", got)
	}
}

func TestProviderStreamAccumulatorRebuildsCustomInput(t *testing.T) {
	var message types.Message
	if !applyProviderStreamEvent(&message, ProviderStreamEvent{Type: "toolcall_start", ContentIndex: 0, ToolCallID: "call-1", ToolName: "apply_patch", ToolCall: &types.Content{Type: "toolCall", ToolType: "custom"}}) {
		t.Fatal("custom start was not applied")
	}
	if !applyProviderStreamEvent(&message, ProviderStreamEvent{Type: "custom_tool_call_input_delta", ContentIndex: 0, Delta: "*** Begin Patch\n"}) ||
		!applyProviderStreamEvent(&message, ProviderStreamEvent{Type: "custom_tool_call_input_delta", ContentIndex: 0, Delta: "*** End Patch"}) {
		t.Fatal("custom input deltas were not applied")
	}
	if got := message.Content[0].Input; got != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("custom input=%q", got)
	}
}
