package loop

import (
	"context"
	"strings"
	"testing"

	"ki/internal/types"
)

type echo struct{}

func (echo) Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
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
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Text()
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
func (oneTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	return ToolResult{Content: []types.Content{{Type: "text", Text: "file-ok"}}}
}

type scripted struct {
	n int
}

func (s *scripted) Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
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
		for _, s := range order {
			if s == string(t) {
				return true
			}
		}
		return false
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
