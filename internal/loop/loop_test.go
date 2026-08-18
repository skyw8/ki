package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ki/internal/types"
)

type echo struct{}

func (echo) Stream(_ context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
	m := types.Message{
		Role:       "assistant",
		Content:    []types.Content{{Type: "text", Text: "echo:" + lastUser(req.Messages)}},
		StopReason: "stop",
		Usage:      &types.Usage{Input: 3, Output: 2, TotalTokens: 5},
	}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: m.Text(), Partial: m})
	return m, nil
}

func lastUser(msgs []types.Message) string {
	for _, v := range slices.Backward(msgs) {
		if v.Role == "user" {
			return v.Text()
		}
	}
	return ""
}

type oneTool struct{}

func (oneTool) Name() string        { return "Read" }
func (oneTool) Description() string { return "r" }
func (oneTool) Prompt() string      { return "p" }
func (oneTool) Snippet() string     { return "s" }
func (oneTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (oneTool) Execute(_ context.Context, _ map[string]any) ToolResult {
	return ToolResult{Content: []types.Content{{Type: "text", Text: "file-ok"}}}
}

type scripted struct {
	n int
}

func (s *scripted) Stream(_ context.Context, _ Request, _ func(AssistantDelta) error) (types.Message, error) {
	s.n++
	if s.n == 1 {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "1", Name: "Read", Arguments: map[string]any{"file_path": "/a"},
			}},
			StopReason: "toolUse",
		}
		return m, nil
	}
	return types.Message{
		Role:       "assistant",
		Content:    []types.Content{{Type: "text", Text: "done"}},
		StopReason: "stop",
	}, nil
}

func TestRunEventOrderAndPersistPoints(t *testing.T) {
	var evs []Event
	_, err := Run(context.Background(), "hello", nil, Config{Streamer: echo{}}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(EventOrder(evs), ",")
	want := "agent_start,turn_start,message_start,message_end,request_header,message_start,message_update,message_end,turn_end,agent_end"
	if got != want {
		t.Fatalf("order\n got %s\nwant %s", got, want)
	}
	var user, asst *types.Message
	for _, e := range evs {
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "user" {
			user = e.Message
		}
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "assistant" {
			asst = e.Message
		}
	}
	if user == nil || user.Text() != "hello" {
		t.Fatalf("user: %+v", user)
	}
	if asst == nil || !strings.Contains(asst.Text(), "hello") {
		t.Fatalf("asst: %+v", asst)
	}
}

func TestRunToolThenSecondTurn(t *testing.T) {
	var evs []Event
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: &scripted{},
		Tools:    []Tool{oneTool{}},
		Parallel: true,
	}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	order := EventOrder(evs)
	has := func(t EventType) bool {
		return slices.Contains(order, string(t))
	}
	for _, need := range []EventType{ToolExecutionStart, ToolExecutionEnd, AgentEnd} {
		if !has(need) {
			t.Fatalf("missing %s in %v", need, order)
		}
	}
	var tr *types.Message
	for _, e := range evs {
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "toolResult" {
			tr = e.Message
		}
	}
	if tr == nil || tr.Text() != "file-ok" {
		t.Fatalf("tool result: %+v", tr)
	}
}

// overflowStreamer fails once with a context-overflow error, then succeeds.
type overflowStreamer struct {
	n int
}

func (s *overflowStreamer) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.n++
	if s.n == 1 {
		return types.Message{
			Role:         "assistant",
			StopReason:   "error",
			ErrorMessage: "prompt is too long: 999999 tokens > 200000 maximum",
		}, ErrContextOverflow
	}
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "retried ok"}}, StopReason: "stop"}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: m.Text(), Partial: m})
	return m, nil
}

func TestRunOverflowRecovery(t *testing.T) {
	var evs []Event
	hooked := false
	_, err := Run(context.Background(), "hello", nil, Config{
		Streamer: &overflowStreamer{},
		Hooks: Hooks{
			OnContextOverflow: func(_ context.Context) ([]types.Message, error) {
				hooked = true
				return []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "compacted"}}}}, nil
			},
		},
	}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hooked {
		t.Fatal("OnContextOverflow was not called")
	}
	order := EventOrder(evs)
	has := func(t EventType) bool {
		return slices.Contains(order, string(t))
	}
	for _, need := range []EventType{CompactionStart, CompactionEnd, AgentEnd} {
		if !has(need) {
			t.Fatalf("missing %s in %v", need, order)
		}
	}
	var asst *types.Message
	for _, e := range evs {
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "assistant" {
			asst = e.Message
		}
	}
	if asst == nil || asst.Text() != "retried ok" {
		t.Fatalf("retried assistant: %+v", asst)
	}
	// Compaction events must be ordered around the recovery, before the retry.
	iStart, iEnd := -1, -1
	for i, e := range evs {
		if e.Type == CompactionStart {
			iStart = i
		}
		if e.Type == CompactionEnd && e.OK {
			iEnd = i
		}
	}
	if iStart < 0 || iEnd < iStart {
		t.Fatalf("compaction events misplaced: %v", order)
	}
}

// alwaysOverflowStreamer always fails with overflow; the recovery must run at
// most once (pi _overflowRecoveryAttempted) and then give up.
type alwaysOverflowStreamer struct{}

func (alwaysOverflowStreamer) Stream(_ context.Context, _ Request, _ func(AssistantDelta) error) (types.Message, error) {
	return types.Message{
		Role:         "assistant",
		StopReason:   "error",
		ErrorMessage: "exceeds the context window of this model",
	}, ErrContextOverflow
}

func TestRunOverflowRecoveryRunsOnce(t *testing.T) {
	hooks := 0
	_, err := Run(context.Background(), "hello", nil, Config{
		Streamer: alwaysOverflowStreamer{},
		Hooks: Hooks{
			OnContextOverflow: func(_ context.Context) ([]types.Message, error) {
				hooks++
				return []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "c"}}}}, nil
			},
		},
	}, func(_ Event) error { return nil })
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("err = %v, want ErrContextOverflow", err)
	}
	if hooks != 1 {
		t.Fatalf("OnContextOverflow called %d times, want 1", hooks)
	}
}

// lengthToolStreamer returns a truncated (length) message with a tool call on
// the first attempt, then a normal reply (model re-issuing after rejection).
type lengthToolStreamer struct {
	n int
}

func (s *lengthToolStreamer) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.n++
	if s.n == 1 {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "1", Name: "Read", Arguments: map[string]any{"file_path": "/a"},
			}},
			StopReason: "length",
		}
		_ = emit(AssistantDelta{Type: "toolcall_delta", Partial: m})
		return m, nil
	}
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "done after retry"}}, StopReason: "stop"}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: m.Text(), Partial: m})
	return m, nil
}

func TestRunLengthRejectsToolCalls(t *testing.T) {
	var evs []Event
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: &lengthToolStreamer{},
		Tools:    []Tool{oneTool{}},
	}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var tr *types.Message
	for _, e := range evs {
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "toolResult" {
			tr = e.Message
		}
	}
	if tr == nil {
		t.Fatal("missing rejected toolResult")
	}
	if !tr.IsError || !strings.Contains(tr.Text(), "not executed") {
		t.Fatalf("toolResult should be a rejection: %+v", tr)
	}
}

// validatingTool rejects calls without a file_path via the optional
// ToolValidator (P0), and records whether Execute ever ran.
type validatingTool struct {
	executed bool
}

func (t *validatingTool) Name() string        { return "Read" }
func (t *validatingTool) Description() string { return "r" }
func (t *validatingTool) Prompt() string      { return "p" }
func (t *validatingTool) Snippet() string     { return "s" }
func (t *validatingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "required": []any{"file_path"}}
}
func (t *validatingTool) Validate(args map[string]any) error {
	if msg := SchemaErrors(t.Parameters(), t.Name(), args); msg != "" {
		return fmt.Errorf("%w: %s", errAssistant, msg)
	}
	return nil
}
func (t *validatingTool) Execute(_ context.Context, _ map[string]any) ToolResult {
	t.executed = true
	return ToolResult{Content: []types.Content{{Type: "text", Text: "ran"}}}
}

// badArgsStreamer returns one tool call with an empty argument map, so the
// optional ToolValidator must reject it before execution.
type badArgsStreamer struct {
	n int
}

func (s *badArgsStreamer) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.n++
	if s.n == 1 {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "1", Name: "Read", Arguments: map[string]any{},
			}},
			StopReason: "toolUse",
		}
		_ = emit(AssistantDelta{Type: "toolcall_delta", Partial: m})
		return m, nil
	}
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "ok after fix"}}, StopReason: "stop"}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: m.Text(), Partial: m})
	return m, nil
}

func TestRunValidateBlocksToolBeforeExecute(t *testing.T) {
	tool := &validatingTool{}
	var evs []Event
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: &badArgsStreamer{},
		Tools:    []Tool{tool},
		Parallel: true,
	}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool.executed {
		t.Fatal("tool executed despite failing validation")
	}
	var tr *types.Message
	for _, e := range evs {
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "toolResult" {
			tr = e.Message
		}
	}
	if tr == nil || !tr.IsError || !strings.Contains(tr.Text(), "file_path: required field") {
		t.Fatalf("toolResult should carry the validation error: %+v", tr)
	}
}

// terminateToolStreamer returns one tool call; the model would normally be
// called again after the result, but the batch terminate signal must stop it.
type terminateToolStreamer struct {
	n int
}

func (s *terminateToolStreamer) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.n++
	m := types.Message{
		Role: "assistant",
		Content: []types.Content{{
			Type: "toolCall", ID: "1", Name: "Read", Arguments: map[string]any{"file_path": "/a"},
		}},
		StopReason: "toolUse",
	}
	_ = emit(AssistantDelta{Type: "toolcall_delta", Partial: m})
	return m, nil
}

func TestRunBeforeToolTerminateStopsLoop(t *testing.T) {
	st := &terminateToolStreamer{}
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: st,
		Tools:    []Tool{oneTool{}},
		Hooks: Hooks{
			BeforeTool: func(_ context.Context, _ string, args map[string]any) (map[string]any, bool, string, bool, error) {
				return args, false, "", true, nil // terminate after this batch
			},
		},
	}, func(_ Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Batch terminate must stop the loop after one request; the model must not
	// be called again.
	if st.n != 1 {
		t.Fatalf("Stream called %d times, want 1 (terminate should stop the loop)", st.n)
	}
}

func TestRequestHeaderCarriesSystemAndTools(t *testing.T) {
	var evs []Event
	_, err := Run(context.Background(), "hello", nil, Config{
		Streamer: echo{},
		System:   "you are ki",
		Tools:    []Tool{oneTool{}},
	}, func(e Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var hdr *Event
	for i := range evs {
		if evs[i].Type == RequestHeader {
			hdr = &evs[i]
			break
		}
	}
	if hdr == nil {
		t.Fatal("missing request_header")
	}
	if hdr.System != "you are ki" {
		t.Fatalf("system: %q", hdr.System)
	}
	if len(hdr.Tools) != 1 || hdr.Tools[0].Name != "Read" || hdr.Tools[0].Description == "" {
		t.Fatalf("tools: %+v", hdr.Tools)
	}
}
