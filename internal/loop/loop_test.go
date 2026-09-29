package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"ki/internal/tooloutput"
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

type echo struct{}

type cancelPartial struct{ cancel context.CancelFunc }

func (s cancelPartial) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "Already visible 中🙂"}}}
	if err := emit(AssistantDelta{Type: "text_delta", Partial: m}); err != nil {
		return m, err
	}
	s.cancel()
	return types.Message{}, context.Canceled
}

func TestCanceledStreamKeepsVisiblePartialInFinalEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var final types.Message
	var start *types.Message
	message, err := streamWithRetry(ctx, Config{Streamer: cancelPartial{cancel: cancel}}, Request{}, func(ev Event) error {
		if ev.Type == MessageEnd {
			final = *ev.Message
		}
		if ev.Type == MessageStart {
			start = ev.Message
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || message.Text() != "Already visible 中🙂" || final.Text() != message.Text() || final.StopReason != "aborted" {
		t.Fatalf("message=%+v final=%+v err=%v", message, final, err)
	}
	if start == nil || start.Text() != "" {
		t.Fatal("published start message mutated")
	}
}

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
func (t *rawTool) NewArgumentDiffConsumer() ToolArgumentDiffConsumer {
	return &rawArgumentConsumer{}
}

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
			return types.Message{}, fmt.Errorf("custom spec missing: %+v", req.Tools)
		}
		return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{Type: "toolCall", ToolType: "custom", ID: "c1", Name: "apply_patch", Input: "PATCH"}}}, nil
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].ToolType != "custom" {
		return types.Message{}, fmt.Errorf("custom result kind missing: %+v", req.Messages)
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
	_, err := Run(context.Background(), "patch", nil, Config{Streamer: &streamingCustomStreamer{}, Tools: []Tool{tool}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
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

type testNonRetryableStreamError struct{}

func (testNonRetryableStreamError) Error() string      { return "malformed provider stream" }
func (testNonRetryableStreamError) NonRetryable() bool { return true }

type malformedStream struct{ calls int }

func (s *malformedStream) Stream(_ context.Context, _ Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.calls++
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "pong"}}}
	_ = emit(AssistantDelta{Type: "text_delta", Delta: "pong", Partial: m})
	return m, testNonRetryableStreamError{}
}

func TestRunReportsNonRetryableStreamFailureWithoutRetry(t *testing.T) {
	stream := &malformedStream{}
	var events []Event
	_, err := Run(context.Background(), "ping", nil, Config{Streamer: stream}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err == nil || stream.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, stream.calls)
	}
	var failed *types.Message
	for _, event := range events {
		if event.Type == MessageEnd && event.Message != nil && event.Message.Role == "assistant" {
			failed = event.Message
		}
	}
	if failed == nil || !failed.IsError || failed.StopReason != "error" || failed.ErrorMessage != "malformed provider stream" {
		t.Fatalf("failed assistant: %+v", failed)
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

type delayedTool struct {
	oneTool
	delay time.Duration
}

func (t delayedTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
	}
	return t.oneTool.Execute(ctx, args)
}

func TestRunToolTimingIsReportedOnExecutionAndResultEvents(t *testing.T) {
	const delay = 20 * time.Millisecond
	var start, end Event
	var result *types.Message
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: &scripted{},
		Tools:    []Tool{delayedTool{delay: delay}},
	}, func(event Event) error {
		switch event.Type {
		case ToolExecutionStart:
			start = event
		case ToolExecutionEnd:
			end = event
		case MessageEnd:
			if event.Message != nil && event.Message.Role == "toolResult" {
				msg := *event.Message
				result = &msg
			}
		case AgentStart, AgentEnd, TurnStart, TurnEnd, RequestHeader, MessageStart, MessageUpdate,
			ToolExecutionUpdate, PatchApplyUpdated, CompactionStart, CompactionEnd, ContextUsage,
			QueueChanged, SteerAccepted, RunAborted, ExtensionError, ExtensionNotice,
			ExtensionUIPrompt, AgentSettled, RuntimeReady:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if start.Timestamp == 0 || end.Timestamp == 0 || end.Timestamp < start.Timestamp {
		t.Fatalf("tool event timestamps: start=%+v end=%+v", start, end)
	}
	if end.DurationMs < int64(delay/time.Millisecond) {
		t.Fatalf("tool event duration = %dms, want at least %dms", end.DurationMs, delay/time.Millisecond)
	}
	if result == nil {
		t.Fatal("missing tool result")
	}
	if result.DurationMs != end.DurationMs || result.Timestamp != end.Timestamp {
		t.Fatalf("tool result timing = %+v, event timing = %+v", result, end)
	}
}

// A turn's wall clock is stamped onto turn_start (timestamp) and turn_end
// (timestamp + durationMs) so a client can run a live per-turn counter from the
// server clock. The tool delay inside the first turn must be covered by its
// duration, which is what makes turn timing more than the sum of the model's
// own latencies.
func TestTurnTimingIsReportedOnTurnEvents(t *testing.T) {
	const delay = 20 * time.Millisecond
	var starts, ends []Event
	_, err := Run(context.Background(), "read it", nil, Config{
		Streamer: &scripted{},
		Tools:    []Tool{delayedTool{delay: delay}},
	}, func(event Event) error {
		switch event.Type {
		case TurnStart:
			starts = append(starts, event)
		case TurnEnd:
			ends = append(ends, event)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) == 0 || len(starts) != len(ends) {
		t.Fatalf("turn events: starts=%d ends=%d", len(starts), len(ends))
	}
	if starts[0].Timestamp == 0 {
		t.Fatalf("turn_start timestamp not set: %+v", starts[0])
	}
	if ends[0].Timestamp < starts[0].Timestamp {
		t.Fatalf("turn timestamps out of order: start=%+v end=%+v", starts[0], ends[0])
	}
	if ends[0].DurationMs < int64(delay/time.Millisecond) {
		t.Fatalf("turn duration = %dms, want at least %dms", ends[0].DurationMs, delay/time.Millisecond)
	}
	if len(starts) > 1 && starts[1].Timestamp < ends[0].Timestamp {
		t.Fatalf("second turn starts before the first ended: %+v %+v", starts[1], ends[0])
	}
}

// A tool that finishes in under a millisecond reports DurationMs 0. The field
// must still be serialized, otherwise fast tools (Read) show no timing in the
// WebUI because `omitempty` dropped the zero value.
func TestZeroToolDurationIsSerialized(t *testing.T) {
	raw, err := json.Marshal(Event{Type: ToolExecutionEnd, ToolCallID: "c1", ToolName: "Read"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["durationMs"]; !ok {
		t.Fatalf("tool_execution_end dropped zero durationMs: %s", raw)
	}
	msg, err := json.Marshal(types.Message{Role: "toolResult", ToolName: "Read", ToolCallID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["durationMs"]; !ok {
		t.Fatalf("toolResult dropped zero durationMs: %s", msg)
	}
}

// overflowStreamer fails once with a context-overflow error, then succeeds.
type overflowStreamer struct {
	n        int
	requests []Request
}

func (s *overflowStreamer) Stream(_ context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.n++
	s.requests = append(s.requests, req)
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
	streamer := &overflowStreamer{}
	initial := []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"old"}`)}
	replacement := []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"new"}`)}
	_, err := Run(context.Background(), "hello", nil, Config{
		Streamer:         streamer,
		ResponsesContext: initial,
		Hooks: Hooks{
			OnContextOverflow: func(_ context.Context, failed Request) (CompactionResult, error) {
				hooked = true
				if len(failed.ResponsesContext) != 1 || string(failed.ResponsesContext[0]) != string(initial[0]) {
					t.Fatalf("hook did not receive failed provider context: %+v", failed.ResponsesContext)
				}
				return CompactionResult{Context: types.ModelContext{
					Responses: &types.ResponsesContext{Items: replacement},
					Messages:  []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "compacted"}}}},
				}}, nil
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
	if len(streamer.requests) != 2 || len(streamer.requests[1].ResponsesContext) != 1 ||
		string(streamer.requests[1].ResponsesContext[0]) != string(replacement[0]) {
		t.Fatalf("retry did not use replacement provider context: %+v", streamer.requests)
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
			OnContextOverflow: func(_ context.Context, _ Request) (CompactionResult, error) {
				hooks++
				return CompactionResult{Context: types.ModelContext{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "c"}}}}}}, nil
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

type serverCompactionStreamer struct{}

func (serverCompactionStreamer) Stream(_ context.Context, _ Request, _ func(AssistantDelta) error) (types.Message, error) {
	return types.Message{
		Role:    "assistant",
		Content: []types.Content{{Type: "text", Text: "compacted reply"}},
		ResponsesItems: []json.RawMessage{
			json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`),
			json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"compacted reply"}]}`),
		},
		StopReason: "stop",
	}, nil
}

func TestRunEmitsServerCompactionLifecycle(t *testing.T) {
	var events []Event
	if _, err := Run(context.Background(), "hello", nil, Config{
		Streamer:                  serverCompactionStreamer{},
		ResponsesCompactThreshold: 1000,
	}, func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start, assistantEnd, end := -1, -1, -1
	for i, event := range events {
		if event.Type == TurnEnd && event.Message != nil && len(event.Message.ResponsesItems) != 0 {
			t.Fatal("turn_end retained provider-owned checkpoint")
		}
		switch {
		case event.Type == CompactionStart && event.Reason == "server":
			start = i
		case event.Type == MessageEnd && event.Message != nil && event.Message.Role == "assistant":
			assistantEnd = i
		case event.Type == CompactionEnd && event.Reason == "server" && event.OK:
			end = i
		}
	}
	if start < 0 || assistantEnd <= start || end <= assistantEnd {
		t.Fatalf("server compaction order: %+v", events)
	}
}

type serverCompactionToolStreamer struct {
	requests []Request
}

func (s *serverCompactionToolStreamer) Stream(_ context.Context, req Request, _ func(AssistantDelta) error) (types.Message, error) {
	s.requests = append(s.requests, req)
	if len(s.requests) == 1 {
		return types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "call_1", ItemID: "fc_1", Name: "Read", Arguments: map[string]any{},
			}},
			ResponsesItems: []json.RawMessage{
				json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`),
				json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{}"}`),
			},
			StopReason: "toolUse",
		}, nil
	}
	return types.Message{
		Role: "assistant", Content: []types.Content{{Type: "text", Text: "done"}}, StopReason: "stop",
	}, nil
}

func TestRunPromotesServerCompactionBeforeToolRound(t *testing.T) {
	streamer := &serverCompactionToolStreamer{}
	history := []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "old"}}}}
	if _, err := Run(context.Background(), "new", history, Config{
		Streamer:                  streamer,
		Tools:                     []Tool{oneTool{}},
		ResponsesCompactThreshold: 1000,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests) != 2 {
		t.Fatalf("requests = %d", len(streamer.requests))
	}
	next := streamer.requests[1]
	if len(next.ResponsesContext) != 2 {
		t.Fatalf("canonical context not promoted: %+v", next.ResponsesContext)
	}
	if len(next.Messages) != 1 || next.Messages[0].Role != "toolResult" {
		t.Fatalf("old history or duplicate assistant survived compaction: %+v", next.Messages)
	}
}

func TestRunDiscardsUnadvertisedInlineCheckpoint(t *testing.T) {
	streamer := &serverCompactionToolStreamer{}
	history := []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "old"}}}}
	var events []Event
	if _, err := Run(context.Background(), "new", history, Config{
		Streamer: streamer,
		Tools:    []Tool{oneTool{}},
	}, func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests) != 2 {
		t.Fatalf("requests = %d", len(streamer.requests))
	}
	next := streamer.requests[1]
	if len(next.ResponsesContext) != 0 {
		t.Fatalf("unadvertised checkpoint reached next request: %+v", next.ResponsesContext)
	}
	if len(next.Messages) < 4 {
		t.Fatalf("portable history was cleared: %+v", next.Messages)
	}
	for _, event := range events {
		if event.Type == CompactionStart || event.Type == CompactionEnd {
			t.Fatalf("unadvertised checkpoint emitted compaction event: %+v", event)
		}
	}
}

type contextManagementFallbackStreamer struct {
	requests []Request
}

func (s *contextManagementFallbackStreamer) Stream(_ context.Context, req Request, _ func(AssistantDelta) error) (types.Message, error) {
	s.requests = append(s.requests, req)
	if req.ResponsesCompactThreshold > 0 {
		return types.Message{Role: "assistant", StopReason: "error", ErrorMessage: "unknown field context_management"},
			testNonRetryableStreamError{}
	}
	return types.Message{
		Role: "assistant", Content: []types.Content{{Type: "text", Text: "fallback ok"}}, StopReason: "stop",
	}, nil
}

func TestRunAutoRetriesWithoutRejectedContextManagement(t *testing.T) {
	streamer := &contextManagementFallbackStreamer{}
	if _, err := Run(context.Background(), "hello", nil, Config{
		Streamer:                  streamer,
		ResponsesCompactThreshold: 1000,
		ResponsesCompactFallback:  true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests) != 2 ||
		streamer.requests[0].ResponsesCompactThreshold != 1000 ||
		streamer.requests[1].ResponsesCompactThreshold != 0 {
		t.Fatalf("fallback requests: %+v", streamer.requests)
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
	var end Event
	for _, e := range evs {
		if e.Type == ToolExecutionEnd {
			end = e
		}
		if e.Type == MessageEnd && e.Message != nil && e.Message.Role == "toolResult" {
			tr = e.Message
		}
	}
	if tr == nil || !tr.IsError || !strings.Contains(tr.Text(), "file_path: required field") {
		t.Fatalf("toolResult should carry the validation error: %+v", tr)
	}
	if end.Timestamp == 0 || tr.Timestamp != end.Timestamp || tr.DurationMs != end.DurationMs {
		t.Fatalf("validation timing: result=%+v end=%+v", tr, end)
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
	g.last = slices.Clone(req.Messages)
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

type bigTool struct {
	oneTool
	text string
}

func (t bigTool) Execute(context.Context, map[string]any) ToolResult {
	return ToolResult{
		Content: []types.Content{{Type: "text", Text: t.text}},
		Details: map[string]any{"matches": 3},
	}
}

// The loop is the single spool boundary: every tool result that exceeds the
// preview budget is bounded there, its complete text is written to the session
// store, and the tool's own details stay flat next to the reference.
func TestRunSpillsOversizedToolResult(t *testing.T) {
	store, err := tooloutput.NewWithConfig(tooloutput.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	full := strings.Repeat("payload ", 4000)
	var result *types.Message
	_, err = Run(context.Background(), "read it", nil, Config{
		Streamer:    &scripted{},
		Tools:       []Tool{bigTool{text: full}},
		SessionID:   "session-1",
		OutputStore: store,
	}, func(event Event) error {
		if event.Type == MessageEnd && event.Message != nil && event.Message.Role == "toolResult" {
			msg := *event.Message
			result = &msg
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("missing tool result")
	}
	if len(result.Text()) > tooloutput.DefaultPreviewBytes+1024 {
		t.Fatalf("model-facing result is %d bytes", len(result.Text()))
	}
	if !strings.Contains(result.Text(), "Stored:") {
		t.Fatalf("result does not point at the spill file: %q", result.Text())
	}
	// Details reach the jsonl/WebUI as JSON, so assert on the marshalled shape.
	rawDetails, err := json.Marshal(result.Details)
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]any
	if err := json.Unmarshal(rawDetails, &details); err != nil {
		t.Fatal(err)
	}
	if details["matches"] != float64(3) {
		t.Fatalf("tool details were nested or lost: %#v", details)
	}
	output, ok := details["output"].(map[string]any)
	if !ok {
		t.Fatalf("missing output reference: %#v", details)
	}
	path, _ := output["path"].(string)
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != full {
		t.Fatalf("spill file is not the complete output (%d of %d bytes)", len(raw), len(full))
	}
}
