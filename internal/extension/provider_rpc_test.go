package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

type providerStreamTestWriter func([]byte) (int, error)

func (write providerStreamTestWriter) Write(data []byte) (int, error) { return write(data) }

// Exercise the actual RPC pipe without starting a process: the sidecar event
// flag is JSON-decoded, just as it would be by readLoop in production.
func streamErrorTestClient(t *testing.T, terminal ProviderStreamEvent) (*rpcClient, *int) {
	t.Helper()
	c := &rpcClient{
		closed:          make(chan struct{}),
		pending:         map[string]chan rpcMsg{},
		providerStreams: map[string]*providerStreamPipe{},
	}
	calls := 0
	c.enc = json.NewEncoder(providerStreamTestWriter(func(data []byte) (int, error) {
		var request rpcMsg
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		if request.Method != "provider.stream.start" {
			return 0, fmt.Errorf("unexpected RPC %q", request.Method)
		}
		calls++
		var params struct {
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return 0, err
		}
		c.providerStreamMu.Lock()
		pipe := c.providerStreams[params.RequestID]
		c.providerStreamMu.Unlock()
		if pipe == nil {
			return 0, fmt.Errorf("missing stream %q", params.RequestID)
		}
		terminal.RequestID = params.RequestID
		for _, event := range []ProviderStreamEvent{
			{RequestID: params.RequestID, Type: "text_start"},
			{RequestID: params.RequestID, Type: "text_delta", Delta: "partial"},
			terminal,
		} {
			raw, err := json.Marshal(event)
			if err != nil {
				return 0, err
			}
			var decoded ProviderStreamEvent
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return 0, err
			}
			pipe.events <- decoded
		}
		c.pendingMu.Lock()
		pending := c.pending[fmt.Sprint(request.ID)]
		c.pendingMu.Unlock()
		if pending == nil {
			return 0, fmt.Errorf("missing RPC %v", request.ID)
		}
		pending <- rpcMsg{ID: request.ID, Result: json.RawMessage(`{"accepted":true}`)}
		return len(data), nil
	}))
	t.Cleanup(c.close)
	return c, &calls
}

func TestProviderStreamErrorRetryMarkerPreservesPartial(t *testing.T) {
	for _, marked := range []bool{false, true} {
		for _, finalSnapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("marked=%v/snapshot=%v", marked, finalSnapshot), func(t *testing.T) {
				event := ProviderStreamEvent{Type: "error", Error: "stream broke", NonRetryable: marked}
				if finalSnapshot {
					event.Message = &types.Message{Role: "assistant", StopReason: "error"}
				}
				c, calls := streamErrorTestClient(t, event)
				var deltas []loop.AssistantDelta
				message, err := c.streamProvider(t.Context(), ProviderStreamRequest{
					Model: provider.Model{Provider: "test-provider", ID: "m", API: "responses"},
				}, func(delta loop.AssistantDelta) error {
					deltas = append(deltas, delta)
					return nil
				})
				var marker interface{ NonRetryable() bool }
				gotMarked := errors.As(err, &marker) && marker.NonRetryable()
				if gotMarked != marked || !errors.Is(err, errProviderStreamFailed) ||
					!strings.Contains(err.Error(), "stream broke") {
					t.Fatalf("error=%v, marker=%v; want marked=%v", err, gotMarked, marked)
				}
				if message.Text() != "partial" || *calls != 1 || len(deltas) != 2 || deltas[1].Partial.Text() != "partial" {
					t.Fatalf("partial lost: message=%+v calls=%d deltas=%+v", message, *calls, deltas)
				}
				if finalSnapshot && message.StopReason != "error" {
					t.Fatalf("terminal message was not merged: %+v", message)
				}
				c.providerStreamMu.Lock()
				remaining := len(c.providerStreams)
				c.providerStreamMu.Unlock()
				if remaining != 0 {
					t.Fatalf("terminal error left %d streams registered", remaining)
				}
			})
		}
	}
}

func TestProviderStreamErrorMarkerControlsLoopRetries(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(fmt.Sprint(marked), func(t *testing.T) {
			c, calls := streamErrorTestClient(t, ProviderStreamEvent{
				Type: "error", Reason: "stream broke", NonRetryable: marked,
			})
			model := provider.Model{Provider: "test-provider", ID: "m", API: "responses"}
			pm := NewProviderManager("")
			pm.owners[model.Provider] = "test-extension"
			pm.clients["test-extension"] = c
			var failed *types.Message
			_, err := loop.Run(t.Context(), "hello", nil, loop.Config{
				Streamer:   pm.NewStreamer(model, provider.Credential{Type: provider.AuthNone}),
				MaxRetries: 2, BaseDelay: time.Nanosecond,
			}, func(event loop.Event) error {
				if event.Type == loop.MessageEnd && event.Message != nil && event.Message.Role == "assistant" {
					failed = event.Message
				}
				return nil
			})
			wantCalls := 3
			if marked {
				wantCalls = 1
			}
			if err == nil || *calls != wantCalls {
				t.Fatalf("loop error=%v calls=%d, want %d", err, *calls, wantCalls)
			}
			if failed == nil || !failed.IsError || failed.StopReason != "error" {
				t.Fatalf("loop lost failed assistant: %+v", failed)
			}
			// The loop returns the original accumulator for a marked failure;
			// its existing exhausted-transient-retries path returns only error.
			if marked && failed.Text() != "partial" {
				t.Fatalf("marked failure lost its partial output: %+v", failed)
			}
		})
	}
}

func TestProviderStreamRetryMarkerWireShape(t *testing.T) {
	for _, marked := range []bool{false, true} {
		raw, err := json.Marshal(ProviderStreamEvent{Type: "error", NonRetryable: marked})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"nonRetryable":true`) != marked ||
			strings.Contains(string(raw), `"nonRetryable":false`) {
			t.Fatalf("unexpected retry marker JSON: %s", raw)
		}
	}
}

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

func TestProviderCompactRPCErrorClassification(t *testing.T) {
	for _, code := range []int{-32700, -32600, -32601, -32602, -32040, -32800, 400, 401, 404, 413, 422} {
		if !deterministicProviderRPCError(code) {
			t.Fatalf("deterministic code %d was retryable", code)
		}
	}
	for _, code := range []int{-32603, -32000, 408, 409, 425, 429, 500, 503} {
		if deterministicProviderRPCError(code) {
			t.Fatalf("transient code %d was non-retryable", code)
		}
	}
}
