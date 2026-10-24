package loop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type overlapStreamer struct {
	started chan struct{}
	calls   int
	fail    bool
}

func (s *overlapStreamer) Stream(ctx context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.calls++
	if s.calls > 1 {
		return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "done"}}, StopReason: "stop"}, nil
	}
	call := types.Content{Type: "toolCall", ID: "call", Name: "read", Arguments: map[string]any{"path": "file"}}
	partial := types.Message{Role: "assistant", Content: []types.Content{call}, StopReason: "toolUse"}
	if err := emit(AssistantDelta{Type: "toolcall_complete", ToolCallID: "call", Partial: partial}); err != nil {
		return partial, err
	}
	select {
	case <-s.started:
	case <-ctx.Done():
		return partial, ctx.Err()
	}
	if s.fail {
		return partial, errors.New("disconnected after completed call")
	}
	return partial, nil
}

func TestStreamToolsOverlapAndFailureDoNotRepeat(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "failure", false: "success"}[fail], func(t *testing.T) {
			started := make(chan struct{})
			var executions atomic.Int32
			tool := dispatchTestTool{namedTool: namedTool{name: "read"}, execute: func(context.Context, map[string]any) toolapi.Result {
				if executions.Add(1) == 1 {
					close(started)
				}
				return toolapi.Result{Content: []types.Content{{Type: "text", Text: "file"}}}
			}}
			streamer := &overlapStreamer{started: started, fail: fail}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var ready, executionStart, resultCount int
			_, err := Run(ctx, "read", nil, Config{Tools: []toolapi.Tool{tool}, Streamer: streamer, Parallel: true, StreamToolExecution: true}, func(e Event) error {
				if e.Type == ToolCallReady {
					ready++
				}
				if e.Type == ToolExecutionStart {
					executionStart++
				}
				if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "toolResult" {
					resultCount++
				}
				return nil
			})
			if (err != nil) != fail {
				t.Fatalf("err = %v", err)
			}
			if executions.Load() != 1 || ready != 1 || executionStart != 1 || resultCount != 1 {
				t.Fatalf("executions=%d ready=%d starts=%d results=%d", executions.Load(), ready, executionStart, resultCount)
			}
			if fail && streamer.calls != 1 {
				t.Fatalf("failed stream replayed %d times", streamer.calls)
			}
		})
	}
}
