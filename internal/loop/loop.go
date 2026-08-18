package loop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ki/internal/types"
)

// EventType is a documented loop event.
type EventType string

var errAssistant = errors.New("assistant error")

const (
	// AgentStart begins an agent run.
	AgentStart EventType = "agent_start"
	// AgentEnd ends an agent run.
	AgentEnd EventType = "agent_end"
	// TurnStart begins a model turn.
	TurnStart EventType = "turn_start"
	// TurnEnd ends a model turn.
	TurnEnd EventType = "turn_end"
	// RequestHeader announces the request context.
	RequestHeader EventType = "request_header"
	// MessageStart begins a message.
	MessageStart EventType = "message_start"
	// MessageUpdate carries a message delta.
	MessageUpdate EventType = "message_update"
	// MessageEnd ends a message.
	MessageEnd EventType = "message_end"
	// ToolExecutionStart begins tool execution.
	ToolExecutionStart EventType = "tool_execution_start"
	// ToolExecutionUpdate carries a tool execution update.
	ToolExecutionUpdate EventType = "tool_execution_update"
	// ToolExecutionEnd ends tool execution.
	ToolExecutionEnd EventType = "tool_execution_end"
	// CompactionStart begins context compaction.
	CompactionStart EventType = "compaction_start"
	// CompactionEnd ends context compaction.
	CompactionEnd EventType = "compaction_end"
	// ContextUsage reports the current model-facing context pressure.
	ContextUsage EventType = "context_usage"
)

// Event is a loop event (pi field names).
type Event struct {
	Type                  EventType       `json:"type"`
	Message               *types.Message  `json:"message,omitempty"`
	Messages              []types.Message `json:"messages,omitempty"`
	ToolResults           []types.Message `json:"toolResults,omitempty"`
	AssistantMessageEvent *AssistantDelta `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string          `json:"toolCallId,omitempty"`
	ToolName              string          `json:"toolName,omitempty"`
	Args                  map[string]any  `json:"args,omitempty"`
	PartialResult         any             `json:"partialResult,omitempty"`
	Result                any             `json:"result,omitempty"`
	IsError               bool            `json:"isError,omitempty"`
	System                string          `json:"system,omitempty"`
	Tools                 []ToolSpec      `json:"tools,omitempty"`
	Reason                string          `json:"reason,omitempty"`
	OK                    bool            `json:"ok,omitempty"`
	Provider              string          `json:"provider,omitempty"`
	Model                 string          `json:"model,omitempty"`
	CatalogVersion        int             `json:"catalogVersion,omitempty"`
	UsedTokens            int             `json:"usedTokens,omitempty"`
	ContextWindow         int             `json:"contextWindow,omitempty"`
	Estimated             bool            `json:"estimated,omitempty"`
}

// AssistantDelta is a streaming increment (pi assistantMessageEvent).
type AssistantDelta struct {
	Type    string        `json:"type"`
	Delta   string        `json:"delta,omitempty"`
	Partial types.Message `json:"partial"`
}

// Tool is something the model can call.
type Tool interface {
	Name() string
	Description() string
	Prompt() string
	Snippet() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) ToolResult
}

// ToolResult is one tool execution outcome.
type ToolResult struct {
	Content []types.Content
	IsError bool
	Details any
	// Terminate hints the agent to stop after this tool batch when every
	// finalized result in the batch sets it (pi result.terminate).
	Terminate bool
}

// Streamer produces an assistant message (and stream deltas).
type Streamer interface {
	Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error)
}

// Request is one provider call.
type Request struct {
	System                  string
	Messages                []types.Message
	Tools                   []ToolSpec
	Provider                string
	Model                   string
	API                     string
	MaxTokens               int
	ThinkingEffort          string
	ThinkingFormat          string
	MaxTokensField          string
	SupportsReasoningEffort bool
	ForceAdaptiveThinking   bool
	ThinkingLevelMap        map[string]*string
}

// ToolSpec is the schema sent to the provider.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Hooks are awaited interception points.
type Hooks struct {
	BeforeRun        func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error)
	TransformContext func(ctx context.Context, msgs []types.Message) ([]types.Message, error)
	// BeforeTool may rewrite args, block the call (with reason), and signal
	// terminate (stop after this batch when every call in the batch terminates).
	BeforeTool func(ctx context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error)
	AfterTool  func(ctx context.Context, name string, args map[string]any, res ToolResult) (ToolResult, error)
	// OnContextOverflow compacts and returns the new context when a request
	// failed with a context-overflow error. Runs at most once per Run (the
	// compact-and-retry guard), inside the same Run so events are not replayed.
	OnContextOverflow func(ctx context.Context) ([]types.Message, error)
}

// ErrContextOverflow marks a request failure caused by context overflow.
// streamWithRetry does not retry it (a resend of the same oversized prompt
// cannot succeed); Run recovers via Hooks.OnContextOverflow instead.
var ErrContextOverflow = errors.New("context overflow")

// Config is loop runtime options.
type Config struct {
	Streamer                Streamer
	Tools                   []Tool
	Hooks                   Hooks
	MaxRetries              int
	BaseDelay               time.Duration
	Parallel                bool
	Provider                string
	Model                   string
	API                     string
	MaxTokens               int
	ThinkingEffort          string
	ThinkingFormat          string
	MaxTokensField          string
	SupportsReasoningEffort bool
	ForceAdaptiveThinking   bool
	ThinkingLevelMap        map[string]*string
	System                  string
}

// Run executes one user prompt against the current messages.
func Run(ctx context.Context, prompt string, history []types.Message, cfg Config, emit func(Event) error) ([]types.Message, error) {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 5
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 2 * time.Second
	}
	if emit == nil {
		emit = func(Event) error { return nil }
	}
	newMsgs := []types.Message{}
	user := types.Message{
		Role:      "user",
		Content:   []types.Content{{Type: "text", Text: prompt}},
		Timestamp: time.Now().UnixMilli(),
	}
	if err := emit(Event{Type: AgentStart}); err != nil {
		return nil, err
	}
	if err := emit(Event{Type: TurnStart}); err != nil {
		return nil, err
	}
	if err := emit(Event{Type: MessageStart, Message: &user}); err != nil {
		return nil, err
	}
	if err := emit(Event{Type: MessageEnd, Message: &user}); err != nil {
		return nil, err
	}
	newMsgs = append(newMsgs, user)
	history = append(append([]types.Message{}, history...), user)

	system := cfg.System
	if cfg.Hooks.BeforeRun != nil {
		s, msgs, err := cfg.Hooks.BeforeRun(ctx, system, history)
		if err != nil {
			return newMsgs, err
		}
		system = s
		history = msgs
	}

	var specs []ToolSpec
	for _, t := range cfg.Tools {
		specs = append(specs, ToolSpec{Name: t.Name(), Description: t.Description() + "\n\n" + t.Prompt(), Parameters: t.Parameters()})
	}

	firstTurn := true
	overflowRecovered := false // compact-and-retry runs at most once per Run (pi _overflowRecoveryAttempted)
	for {
		if ctx.Err() != nil {
			_ = emit(Event{Type: AgentEnd, Messages: newMsgs})
			return newMsgs, ctx.Err()
		}
		if !firstTurn {
			if err := emit(Event{Type: TurnStart}); err != nil {
				return newMsgs, err
			}
		}
		firstTurn = false

		msgs := history
		if cfg.Hooks.TransformContext != nil {
			m, err := cfg.Hooks.TransformContext(ctx, msgs)
			if err != nil {
				return newMsgs, err
			}
			msgs = m
		}

		if err := emit(Event{Type: RequestHeader, System: system, Tools: append([]ToolSpec(nil), specs...), Provider: cfg.Provider, Model: cfg.Model}); err != nil {
			return newMsgs, err
		}

		asst, err := streamWithRetry(ctx, cfg, Request{
			System:                  system,
			Messages:                msgs,
			Tools:                   specs,
			Provider:                cfg.Provider,
			Model:                   cfg.Model,
			API:                     cfg.API,
			MaxTokens:               cfg.MaxTokens,
			ThinkingEffort:          cfg.ThinkingEffort,
			ThinkingFormat:          cfg.ThinkingFormat,
			MaxTokensField:          cfg.MaxTokensField,
			SupportsReasoningEffort: cfg.SupportsReasoningEffort,
			ForceAdaptiveThinking:   cfg.ForceAdaptiveThinking,
			ThinkingLevelMap:        cfg.ThinkingLevelMap,
		}, emit)
		if err != nil {
			// Context overflow: compact once (server-side, via hook) and retry
			// with the new context inside the same Run. The failed assistant
			// message stays in session history but is dropped by provider
			// replayable on the retry (skipAssistant skips stopReason error).
			if errors.Is(err, ErrContextOverflow) && !overflowRecovered && cfg.Hooks.OnContextOverflow != nil {
				overflowRecovered = true
				_ = emit(Event{Type: CompactionStart, Reason: "overflow"})
				newHistory, herr := cfg.Hooks.OnContextOverflow(ctx)
				if herr == nil {
					history = newHistory
					_ = emit(Event{Type: CompactionEnd, Reason: "overflow", OK: true})
					continue
				}
				_ = emit(Event{Type: CompactionEnd, Reason: "overflow", OK: false})
			}
			_ = emit(Event{Type: AgentEnd, Messages: newMsgs})
			return newMsgs, err
		}
		newMsgs = append(newMsgs, asst)
		history = append(history, asst)

		calls := asst.ToolCalls()
		var results []types.Message
		terminate := false
		if len(calls) > 0 {
			if asst.StopReason == "length" {
				// Output hit the token limit: streamed tool-call arguments may be
				// truncated. Refuse to run them so the model re-issues (pi
				// failToolCallsFromTruncatedMessage).
				results = rejectToolCalls(calls, emit)
			} else {
				results, terminate = executeTools(ctx, cfg, calls, emit)
			}
			for _, r := range results {
				rr := r
				if err := emit(Event{Type: MessageStart, Message: &rr}); err != nil {
					return newMsgs, err
				}
				if err := emit(Event{Type: MessageEnd, Message: &rr}); err != nil {
					return newMsgs, err
				}
				newMsgs = append(newMsgs, rr)
				history = append(history, rr)
			}
		}
		if err := emit(Event{Type: TurnEnd, Message: &asst, ToolResults: results}); err != nil {
			return newMsgs, err
		}
		if len(calls) == 0 || asst.StopReason == "error" || asst.StopReason == "aborted" || terminate {
			break
		}
	}
	if err := emit(Event{Type: AgentEnd, Messages: newMsgs}); err != nil {
		return newMsgs, err
	}
	return newMsgs, nil
}

func streamWithRetry(ctx context.Context, cfg Config, req Request, emit func(Event) error) (types.Message, error) {
	var last types.Message
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			d := cfg.BaseDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return last, ctx.Err()
			case <-time.After(d):
			}
		}
		started := time.Now()
		var firstDelta time.Time
		partial := types.Message{Role: "assistant", Provider: cfg.Provider, Model: cfg.Model, Timestamp: time.Now().UnixMilli()}
		if err := emit(Event{Type: MessageStart, Message: &partial}); err != nil {
			return partial, err
		}
		asst, err := cfg.Streamer.Stream(ctx, req, func(d AssistantDelta) error {
			if firstDelta.IsZero() {
				firstDelta = time.Now()
			}
			m := d.Partial
			return emit(Event{Type: MessageUpdate, Message: &m, AssistantMessageEvent: &d})
		})
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				asst = types.Message{Role: "assistant", StopReason: "aborted", ErrorMessage: ctx.Err().Error()}
				_ = emit(Event{Type: MessageEnd, Message: &asst})
				return asst, fmt.Errorf("stream assistant response: %w", err)
			}
			// Context overflow (provider 4xx carries the error message on asst):
			// a backoff retry of the same oversized prompt cannot succeed. Emit
			// the end marker and let Run recover via Hooks.OnContextOverflow.
			if asst.StopReason == "error" && IsContextOverflow(asst) {
				last = asst
				_ = emit(Event{Type: MessageEnd, Message: &asst})
				return last, ErrContextOverflow
			}
			continue
		}
		asst.LatencyMs = time.Since(started).Milliseconds()
		if !firstDelta.IsZero() {
			asst.TTFTMs = firstDelta.Sub(started).Milliseconds()
		}
		if asst.Timestamp == 0 {
			asst.Timestamp = time.Now().UnixMilli()
		}
		if asst.Provider == "" {
			asst.Provider = cfg.Provider
		}
		if asst.Model == "" {
			asst.Model = cfg.Model
		}
		if err := emit(Event{Type: MessageEnd, Message: &asst}); err != nil {
			return asst, err
		}
		if asst.StopReason == "error" {
			last = asst
			lastErr = fmt.Errorf("%w: %s", errAssistant, asst.ErrorMessage)
			// An overflow resend of the same oversized prompt cannot succeed;
			// let Run recover via Hooks.OnContextOverflow instead of burning
			// MaxRetries on backoff.
			if IsContextOverflow(asst) {
				return last, ErrContextOverflow
			}
			continue
		}
		return asst, nil
	}
	if last.Role == "" {
		last = types.Message{Role: "assistant", StopReason: "error", ErrorMessage: fmt.Sprint(lastErr)}
		_ = emit(Event{Type: MessageStart, Message: &last})
		_ = emit(Event{Type: MessageEnd, Message: &last})
	}
	return last, lastErr
}

// ToolValidator is the optional pre-execution schema check (P0, pi
// validateToolArguments). Tools may implement it to reject malformed
// arguments before any execution starts.
type ToolValidator interface {
	Validate(args map[string]any) error
}

// executeTools runs a batch of tool calls in two phases (pi prepare/execute):
//
//  1. prepare (synchronous): resolve the tool, schema-validate via the
//     optional ToolValidator, and run the BeforeTool hook. Failures become
//     immediate error results — nothing is executed for them.
//  2. execute: run the prepared calls (parallel or sequential).
//
// The second return value is the batch terminate signal: true when every call
// in the batch terminated (BeforeTool terminate or ToolResult.Terminate), so
// the main loop can stop instead of requesting the model again (pi
// shouldTerminateToolBatch).
func executeTools(ctx context.Context, cfg Config, calls []types.Content, emit func(Event) error) ([]types.Message, bool) {
	byName := map[string]Tool{}
	for _, t := range cfg.Tools {
		byName[t.Name()] = t
	}
	out := make([]types.Message, len(calls))

	// Phase 1: prepare (synchronous, no side effects).
	type prep struct {
		call      types.Content
		args      map[string]any
		tool      Tool
		immediate *types.Message // set → skip execute
		terminate bool
	}
	preps := make([]prep, len(calls))
	for i, c := range calls {
		_ = emit(Event{Type: ToolExecutionStart, ToolCallID: c.ID, ToolName: c.Name, Args: c.Arguments})
		p := prep{call: c}
		args := c.Arguments
		if args == nil {
			args = map[string]any{}
		}
		t, ok := byName[c.Name]
		if !ok {
			m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, Content: []types.Content{{Type: "text", Text: "unknown tool " + c.Name}}, IsError: true}
			p.immediate = &m
			preps[i] = p
			continue
		}
		if v, ok := t.(ToolValidator); ok {
			if err := v.Validate(args); err != nil {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
				p.immediate = &m
				preps[i] = p
				continue
			}
		}
		if cfg.Hooks.BeforeTool != nil {
			a, b, r, term, err := cfg.Hooks.BeforeTool(ctx, c.Name, args)
			if err != nil {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
				p.immediate = &m
				p.terminate = term
				preps[i] = p
				continue
			}
			args, p.terminate = a, term
			if b {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, Content: []types.Content{{Type: "text", Text: r}}, IsError: true}
				p.immediate = &m
				preps[i] = p
				continue
			}
		}
		p.args, p.tool = args, t
		preps[i] = p
	}

	// Phase 2: execute.
	run := func(i int) {
		p := preps[i]
		if p.immediate != nil {
			out[i] = *p.immediate
			_ = emit(Event{Type: ToolExecutionEnd, ToolCallID: p.call.ID, ToolName: p.call.Name, IsError: true})
			return
		}
		start := time.Now()
		res := p.tool.Execute(ctx, p.args)
		if cfg.Hooks.AfterTool != nil {
			if nr, err := cfg.Hooks.AfterTool(ctx, p.call.Name, p.args, res); err == nil {
				res = nr
			}
		}
		dur := time.Since(start).Milliseconds()
		msg := types.Message{
			Role:       "toolResult",
			ToolCallID: p.call.ID,
			ToolName:   p.call.Name,
			Content:    res.Content,
			IsError:    res.IsError,
			DurationMs: dur,
			Timestamp:  time.Now().UnixMilli(),
		}
		out[i] = msg
		p.terminate = p.terminate || res.Terminate
		preps[i] = p
		_ = emit(Event{Type: ToolExecutionEnd, ToolCallID: p.call.ID, ToolName: p.call.Name, Args: p.args, Result: res, IsError: res.IsError})
	}
	if cfg.Parallel {
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				run(i)
			}(i)
		}
		wg.Wait()
	} else {
		for i := range calls {
			run(i)
		}
	}

	// Batch terminate: every call terminated (pi shouldTerminateToolBatch).
	terminate := len(calls) > 0
	for _, p := range preps {
		if !p.terminate {
			terminate = false
			break
		}
	}
	return out, terminate
}

// rejectToolCalls turns every tool call into an error result without executing
// it. Used when an assistant message was truncated by the output token limit:
// streamed arguments may be incomplete, so none are safe to run (pi
// failToolCallsFromTruncatedMessage). The model sees the errors and re-issues.
func rejectToolCalls(calls []types.Content, emit func(Event) error) []types.Message {
	out := make([]types.Message, 0, len(calls))
	for _, c := range calls {
		msg := types.Message{
			Role:       "toolResult",
			ToolCallID: c.ID,
			ToolName:   c.Name,
			Content: []types.Content{{Type: "text", Text: fmt.Sprintf(
				"Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", c.Name)}},
			IsError:   true,
			Timestamp: time.Now().UnixMilli(),
		}
		_ = emit(Event{Type: ToolExecutionStart, ToolCallID: c.ID, ToolName: c.Name, Args: c.Arguments})
		_ = emit(Event{Type: ToolExecutionEnd, ToolCallID: c.ID, ToolName: c.Name, IsError: true})
		out = append(out, msg)
	}
	return out
}

// EventOrder is the sequence of types in events, for tests.
func EventOrder(evs []Event) []string {
	var s []string
	for _, e := range evs {
		s = append(s, string(e.Type))
	}
	return s
}
