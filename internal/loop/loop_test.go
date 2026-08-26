package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestRequestJSONUsesProviderFieldNames(t *testing.T) {
	raw, err := json.Marshal(Request{
		SessionID: "session-1",
		System:    "system",
		Messages:  []types.Message{{Role: "user"}},
		MaxTokens: 128000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sessionId", "system", "messages", "maxTokens"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("missing provider request field %q in %s", field, raw)
		}
	}
	for _, field := range []string{"SessionID", "System", "Messages", "MaxTokens"} {
		if _, ok := got[field]; ok {
			t.Fatalf("unexpected Go field name %q in %s", field, raw)
		}
	}
}

var (
	errCustomSpecMissing   = errors.New("custom spec missing")
	errCustomResultMissing = errors.New("custom result kind missing")
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

type rawTool struct{ got string }

func (t *rawTool) Name() string               { return "apply_patch" }
func (t *rawTool) Description() string        { return "patch" }
func (t *rawTool) Prompt() string             { return "" }
func (t *rawTool) Snippet() string            { return "patch" }
func (t *rawTool) Parameters() map[string]any { return nil }
func (t *rawTool) Execute(context.Context, map[string]any) ToolResult {
	return ToolResult{IsError: true}
}
func (t *rawTool) ExecuteRaw(_ context.Context, input string) ToolResult {
	t.got = input
	return ToolResult{Content: []types.Content{{Type: "text", Text: "patched"}}}
}
func (t *rawTool) ToolSpec() ToolSpec {
	return ToolSpec{Type: "custom", Name: t.Name(), Description: t.Description(), Format: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: PATCH"}}
}
func (t *rawTool) NewArgumentDiffConsumer() ToolArgumentDiffConsumer { return &rawArgumentConsumer{} }

type rawArgumentConsumer struct{ input string }

func (c *rawArgumentConsumer) Consume(delta string) (any, bool) {
	c.input += delta
	return map[string]any{"input": c.input}, true
}
func (c *rawArgumentConsumer) Finish() (any, bool) { return nil, false }

type customStreamer struct{ n int }

func (s *customStreamer) Stream(_ context.Context, req Request, _ func(AssistantDelta) error) (types.Message, error) {
	s.n++
	if s.n == 1 {
		if len(req.Tools) != 1 || req.Tools[0].Type != "custom" {
			return types.Message{}, fmt.Errorf("%w: %+v", errCustomSpecMissing, req.Tools)
		}
		return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{Type: "toolCall", ToolType: "custom", ID: "c1", Name: "apply_patch", Input: "PATCH"}}}, nil
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].ToolType != "custom" {
		return types.Message{}, fmt.Errorf("%w: %+v", errCustomResultMissing, req.Messages)
	}
	return types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: "done"}}}, nil
}

func TestRunDispatchesFreeformTool(t *testing.T) {
	tool := &rawTool{}
	_, err := Run(context.Background(), "patch", nil, Config{Streamer: &customStreamer{}, Tools: []Tool{tool}}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tool.got != "PATCH" {
		t.Fatalf("raw input = %q", tool.got)
	}
}

type streamingCustomStreamer struct{ customStreamer }

func (s *streamingCustomStreamer) Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
	if s.n == 0 {
		partial := types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ToolType: "custom", ID: "c1", Name: "apply_patch", Input: "PATCH"}}}
		if err := emit(AssistantDelta{Type: "custom_tool_call_input_delta", Delta: "PATCH", ToolCallID: "c1", ToolName: "apply_patch", Partial: partial}); err != nil {
			return types.Message{}, err
		}
	}
	return s.customStreamer.Stream(ctx, req, emit)
}

func TestRunEmitsStreamedToolArgumentPreview(t *testing.T) {
	tool := &rawTool{}
	var events []Event
	_, err := Run(context.Background(), "patch", nil, Config{Streamer: &streamingCustomStreamer{}, Tools: []Tool{tool}}, func(event Event) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == PatchApplyUpdated && event.ToolCallID == "c1" {
			return
		}
	}
	t.Fatalf("missing patch preview in %+v", events)
}

type captureMessages struct{ got []types.Message }

func (c *captureMessages) Stream(_ context.Context, req Request, _ func(AssistantDelta) error) (types.Message, error) {
	c.got = req.Messages
	return types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: "ok"}}}, nil
}

func TestRunStripsImagesForTextOnlyModel(t *testing.T) {
	stream := &captureMessages{}
	history := []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "old"}, {Type: "image", Data: "AAA", MIMEType: "image/png"}}}}
	_, err := Run(context.Background(), "next", history, Config{Streamer: stream, TextOnly: true}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range stream.got {
		for _, content := range message.Content {
			if content.Type == "image" {
				t.Fatalf("image leaked to text-only model: %+v", stream.got)
			}
		}
	}
}
func (oneTool) Execute(_ context.Context, _ map[string]any) ToolResult {
	return ToolResult{Content: []types.Content{{Type: "text", Text: "file-ok"}}, Details: map[string]any{"diff": "client-only"}}
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
	if details, ok := tr.Details.(map[string]any); !ok || details["diff"] != "client-only" {
		t.Fatalf("tool details: %#v", tr.Details)
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

type gatedEcho struct {
	started chan struct{}
	release chan struct{}
	n       int
	last    []types.Message
}

func (g *gatedEcho) Stream(_ context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
	g.n++
	if g.n == 1 {
		close(g.started)
		<-g.release
	}
	g.last = append([]types.Message(nil), req.Messages...)
	text := "echo:" + lastUser(req.Messages)
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: text}}, StopReason: "stop"}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: text, Partial: m})
	return m, nil
}

func TestRunDrainsInboxAfterCurrentStream(t *testing.T) {
	g := &gatedEcho{started: make(chan struct{}), release: make(chan struct{})}
	inbox := &Inbox{}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), "first", nil, Config{Streamer: g, Inbox: inbox}, func(Event) error { return nil })
		done <- err
	}()
	<-g.started
	if g.n != 1 {
		t.Fatalf("first stream should still be in flight, n=%d", g.n)
	}
	inbox.Push(types.Message{Content: []types.Content{{Type: "text", Text: "steer"}}})
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if g.n != 2 {
		t.Fatalf("expected a second stream after steer, n=%d", g.n)
	}
	if lastUser(g.last) != "steer" {
		t.Fatalf("second request last user = %q", lastUser(g.last))
	}
	foundFirst := false
	for _, m := range g.last {
		if m.Role == "user" && m.Text() == "first" {
			foundFirst = true
		}
	}
	if !foundFirst {
		t.Fatalf("original user missing from second request: %+v", g.last)
	}
}
