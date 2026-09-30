package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"ki/internal/telemetry"
	"ki/internal/tooloutput"
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
	// PatchApplyUpdated carries a non-executing preview parsed from streamed
	// apply_patch arguments.
	PatchApplyUpdated EventType = "patch_apply_updated"
	// CompactionStart begins context compaction.
	CompactionStart EventType = "compaction_start"
	// CompactionEnd ends context compaction.
	CompactionEnd EventType = "compaction_end"
	// ContextUsage reports the current model-facing context pressure.
	ContextUsage EventType = "context_usage"
	// QueueChanged reports that the session user-message FIFO changed.
	QueueChanged EventType = "queue_changed"
	// SteerAccepted reports a user message accepted into the live Inbox.
	// It is not a jsonl leaf; drain later emits message_start/end.
	SteerAccepted EventType = "steer_accepted"
	// RunAborted reports that abort cancelled the occupy context. AgentEnd
	// still follows when the loop actually returns.
	RunAborted EventType = "run_aborted"
	// ExtensionError reports a sidecar or lifecycle failure as a sideband.
	ExtensionError EventType = "extension_error"
	// ExtensionNotice is a non-error toast (info/warn) for the WebUI shell.
	ExtensionNotice EventType = "extension_notice"
	// ExtensionUIPrompt asks the WebUI to confirm or select (120s or abort = cancel).
	ExtensionUIPrompt EventType = "extension_ui_prompt"
	// AgentSettled reports occupy wrap-up finished and a new occupy may start.
	AgentSettled EventType = "agent_settled"
	// RuntimeReady reports session-open extension preparation finished.
	// Failure still counts as ready so the composer can unlock.
	RuntimeReady EventType = "runtime_ready"
)

// Lifecycle reports whether the event is delivered to extension lifecycle
// subscribers (the async-only list in docs/events.md).
//
// High-churn sidebands (tool progress, context usage, patch previews) and
// failures the host already handled (extension errors) are excluded. The list
// is an allow-list on purpose: a new event type stays invisible to extensions
// until it is added here, instead of leaking to them by default.
func (t EventType) Lifecycle() bool {
	switch t {
	case AgentStart, AgentEnd, TurnStart, TurnEnd,
		RequestHeader, MessageStart, MessageUpdate, MessageEnd,
		ToolExecutionStart, ToolExecutionEnd, CompactionStart, CompactionEnd,
		QueueChanged, SteerAccepted, RunAborted,
		ExtensionNotice, ExtensionUIPrompt, AgentSettled, RuntimeReady:
		return true
	}
	return false
}

// Event is a loop event (pi field names).
type Event struct {
	Type    EventType `json:"type"`
	EntryID string    `json:"entryId,omitempty"`
	// ParentID is the persisted message edge, including an explicit empty root.
	// A sparse transcript leaf is not a safe parent for a replayed message.
	ParentID *string `json:"parentId,omitempty"`
	// Timestamp is Unix milliseconds. Tool execution start/end events use it
	// as the authoritative start/completion wall-clock time.
	Timestamp int64 `json:"timestamp,omitzero"`
	// DurationMs is the elapsed time for one tool call, including execution and
	// the optional AfterTool hook. It is not omitempty: a sub-millisecond call
	// rounds to 0 and clients must still see the field (otherwise fast tools
	// like Read show no timing at all).
	DurationMs            int64             `json:"durationMs"`
	Message               *types.Message    `json:"message,omitempty"`
	MessagePatch          *MessagePatch     `json:"messagePatch,omitempty"`
	MessageStream         int64             `json:"messageStream,omitempty"`
	Messages              []types.Message   `json:"messages,omitempty"`
	ToolResults           []types.Message   `json:"toolResults,omitempty"`
	AssistantMessageEvent *AssistantDelta   `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string            `json:"toolCallId,omitempty"`
	ToolName              string            `json:"toolName,omitempty"`
	Args                  map[string]any    `json:"args,omitempty"`
	PartialResult         any               `json:"partialResult,omitempty"`
	Result                any               `json:"result,omitempty"`
	IsError               bool              `json:"isError,omitzero"`
	System                string            `json:"system,omitempty"`
	Tools                 []ToolSpec        `json:"tools,omitempty"`
	Reason                string            `json:"reason,omitempty"`
	OK                    bool              `json:"ok,omitzero"`
	WillRetry             bool              `json:"willRetry,omitzero"`
	Strategy              string            `json:"strategy,omitempty"`
	Status                string            `json:"status,omitempty"`
	FromExtension         bool              `json:"fromExtension,omitzero"`
	FirstKeptEntryID      string            `json:"firstKeptEntryId,omitempty"`
	TokensBefore          int               `json:"tokensBefore,omitzero"`
	Usage                 *types.Usage      `json:"usage,omitempty"`
	Provider              string            `json:"provider,omitempty"`
	Model                 string            `json:"model,omitempty"`
	CatalogVersion        int               `json:"catalogVersion,omitzero"`
	UsedTokens            int               `json:"usedTokens,omitzero"`
	ContextWindow         int               `json:"contextWindow,omitzero"`
	Estimated             bool              `json:"estimated,omitzero"`
	Server                string            `json:"server,omitempty"`
	MessageText           string            `json:"messageText,omitempty"`
	ReloadRequired        bool              `json:"reloadRequired,omitzero"`
	Options               []string          `json:"options,omitempty"`
	RunID                 string            `json:"runId,omitempty"`
	External              map[string]string `json:"external,omitempty"`
	// Seq is the per-run sequence number the server stamps when it buffers the
	// event for SSE replay. It travels with the event so a client can resume
	// with the last one it saw (the SSE id line, or ?since=) instead of asking
	// for the whole run again.
	Seq int64 `json:"seq,omitempty"`
	// PromptUnchanged marks a replayed request_header whose system prompt and
	// tool schemas repeat the run's first one: the payload is left out and the
	// reader reuses what it already has. Same shape as the persisted entry
	// (session.Entry.PromptUnchanged), so both paths feed one client rule.
	PromptUnchanged bool `json:"promptUnchanged,omitempty"`
	// Blank marks a buffered event whose payload the server dropped from the
	// replay log because a newer partial supersedes it. Readers skip it; it is
	// never sent and never reaches an extension.
	Blank bool `json:"-"`
	// Process-local replay accounting; never persisted or put on the wire.
	BufferedAt    time.Time `json:"-"`
	BufferedBytes int       `json:"-"`
}

// AssistantDelta is a streaming increment (pi assistantMessageEvent).
type AssistantDelta struct {
	Type       string        `json:"type"`
	Delta      string        `json:"delta,omitempty"`
	ToolCallID string        `json:"toolCallId,omitempty"`
	ToolName   string        `json:"toolName,omitempty"`
	Partial    types.Message `json:"partial"`
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

// ProgressTool is implemented by tools that can report incremental output
// while Execute is still running. The callback is intentionally untyped at the
// loop boundary: tool-specific progress is persisted as JSON and presented to
// the model/UI as a partial result.
type ProgressTool interface {
	Tool
	ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) ToolResult
}

// ToolSpecProvider optionally replaces the default JSON function schema.
// It is used by grammar-backed Responses custom tools such as apply_patch.
type ToolSpecProvider interface {
	ToolSpec() ToolSpec
}

// FreeformTool executes the raw input of a custom tool call.
type FreeformTool interface {
	ExecuteRaw(ctx context.Context, input string) ToolResult
}

// ToolArgumentDiffConsumer incrementally parses a freeform tool call while
// the provider is still producing its arguments. Results are client previews;
// they never authorize or execute the tool.
type ToolArgumentDiffConsumer interface {
	Consume(delta string) (any, bool)
	Finish() (any, bool)
}

// ToolArgumentDiffProvider creates isolated state for one streamed tool call.
type ToolArgumentDiffProvider interface {
	NewArgumentDiffConsumer() ToolArgumentDiffConsumer
}

// ToolResult is one tool execution outcome.
type ToolResult struct {
	Content []types.Content
	IsError bool
	Details any
	// Diagnostic is private harness telemetry. Provider adapters receive only
	// the model-facing content and IsError fields.
	Diagnostic telemetry.ToolDiagnostic `json:"-"`
	// Terminate hints the agent to stop after this tool batch when every
	// finalized result in the batch sets it (pi result.terminate).
	Terminate bool
}

// Streamer produces an assistant message (and stream deltas).
type Streamer interface {
	Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error)
}

// Request is one provider call. It crosses the provider extension NDJSON
// boundary, so its JSON names must be explicit: encoding/json otherwise emits
// Go field names (for example, Messages), which sidecars do not consume.
type Request struct {
	// SessionID is optional provider cache affinity metadata. Core providers
	// ignore it; provider extensions may use it for session-scoped protocol state.
	SessionID               string                `json:"sessionId"`
	System                  string                `json:"system"`
	Messages                []types.Message       `json:"messages"`
	Tools                   []ToolSpec            `json:"tools"`
	Provider                string                `json:"provider"`
	Model                   string                `json:"model"`
	API                     string                `json:"api"`
	ProviderBinding         types.ProviderBinding `json:"-"`
	MaxTokens               int                   `json:"maxTokens,omitzero"`
	ThinkingEffort          string                `json:"thinkingEffort,omitempty"`
	ThinkingFormat          string                `json:"thinkingFormat,omitempty"`
	MaxTokensField          string                `json:"maxTokensField,omitempty"`
	SupportsReasoningEffort bool                  `json:"supportsReasoningEffort,omitzero"`
	ForceAdaptiveThinking   bool                  `json:"forceAdaptiveThinking,omitzero"`
	ThinkingLevelMap        map[string]*string    `json:"thinkingLevelMap,omitempty"`
	// ResponsesContext is a provider-owned canonical input prefix. Other wire
	// protocols ignore it.
	ResponsesContext []json.RawMessage `json:"responsesContext,omitempty"`
	// ResponsesCompactThreshold enables Responses context_management.
	ResponsesCompactThreshold int `json:"responsesCompactThreshold,omitzero"`
	// ContextTransformed is process-local proof that TransformContext already
	// ran for Messages. Standalone compaction uses it to avoid applying the
	// security-sensitive hook twice.
	ContextTransformed bool `json:"-"`
}

// ToolSpec is the schema sent to the provider.
type ToolSpec struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *ToolFormat    `json:"format,omitempty"`
}

// ToolFormat describes the grammar accepted by a Responses custom tool.
type ToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

func specForTool(t Tool) ToolSpec {
	if p, ok := t.(ToolSpecProvider); ok {
		return p.ToolSpec()
	}
	return ToolSpec{Type: "function", Name: t.Name(), Description: t.Description() + "\n\n" + t.Prompt(), Parameters: t.Parameters()}
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
	OnContextOverflow func(ctx context.Context, failed Request) (CompactionResult, error)
}

// CompactionResult carries a rebuilt context plus redacted checkpoint metadata
// from an overflow hook back to the loop's terminal compaction event.
type CompactionResult struct {
	Context          types.ModelContext
	EntryID          string
	Strategy         string
	FromExtension    bool
	FirstKeptEntryID string
	TokensBefore     int
	Usage            *types.Usage
}

// ErrContextOverflow marks a request failure caused by context overflow.
// streamWithRetry does not retry it (a resend of the same oversized prompt
// cannot succeed); Run recovers via Hooks.OnContextOverflow instead.
var ErrContextOverflow = errors.New("context overflow")

// Inbox holds user messages injected into a live Run (steer). It is
// process-local and is not part of a resources snapshot.
type Inbox struct {
	mu      sync.Mutex
	pending []types.Message
}

// Push appends a user message to be consumed on the next model request.
func (i *Inbox) Push(m types.Message) {
	if i == nil {
		return
	}
	m.Role = "user"
	i.mu.Lock()
	i.pending = append(i.pending, m)
	i.mu.Unlock()
}

// Take returns and clears pending steer messages.
func (i *Inbox) Take() []types.Message {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	out := i.pending
	i.pending = nil
	return out
}

// Has reports whether any steer message is waiting.
func (i *Inbox) Has() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.pending) > 0
}

// Config is loop runtime options.
type Config struct {
	Streamer                  Streamer
	SessionID                 string
	Tools                     []Tool
	OutputStore               *tooloutput.Store
	Telemetry                 *telemetry.Run
	Hooks                     Hooks
	MaxRetries                int
	BaseDelay                 time.Duration
	Parallel                  bool
	Provider                  string
	Model                     string
	API                       string
	ProviderBinding           types.ProviderBinding
	MaxTokens                 int
	ThinkingEffort            string
	ThinkingFormat            string
	MaxTokensField            string
	SupportsReasoningEffort   bool
	ForceAdaptiveThinking     bool
	ThinkingLevelMap          map[string]*string
	ResponsesContext          []json.RawMessage
	ResponsesCompactThreshold int
	// ResponsesCompactFallback retries one failed server-side request without
	// context_management. Auto mode enables it; strict remote mode does not.
	ResponsesCompactFallback bool
	TextOnly                 bool
	System                   string
	// Inbox receives same-run user messages. Drain happens after the current
	// stream/tools turn, before the next streamWithRetry. A nil Inbox is idle.
	Inbox *Inbox
}

// Run executes one user prompt against the current messages.
func Run(ctx context.Context, prompt string, history []types.Message, cfg Config, emit func(Event) error) ([]types.Message, error) {
	return RunMessage(ctx, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: prompt}}}, history, cfg, emit)
}

// RunMessage executes one structured user message against the current history.
func RunMessage(ctx context.Context, user types.Message, history []types.Message, cfg Config, emit func(Event) error) ([]types.Message, error) {
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
	user.Role = "user"
	if user.Timestamp == 0 {
		user.Timestamp = time.Now().UnixMilli()
	}
	if err := emit(Event{Type: AgentStart}); err != nil {
		return nil, err
	}
	// The turn's wall clock starts here and is stamped onto turn_start; turn_end
	// reports the span so consumers can time a turn from the server clock.
	turnStartedAt := time.Now()
	if err := emit(Event{Type: TurnStart, Timestamp: turnStartedAt.UnixMilli()}); err != nil {
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
		specs = append(specs, specForTool(t))
	}
	responsesContext := slices.Clone(cfg.ResponsesContext)

	firstTurn := true
	overflowRecovered := false // compact-and-retry runs at most once per Run (pi _overflowRecoveryAttempted)
	serverCompactionFallback := false
	for {
		if ctx.Err() != nil {
			_ = emit(Event{Type: AgentEnd, Messages: newMsgs})
			return newMsgs, ctx.Err()
		}
		if !firstTurn {
			turnStartedAt = time.Now()
			if err := emit(Event{Type: TurnStart, Timestamp: turnStartedAt.UnixMilli()}); err != nil {
				return newMsgs, err
			}
		}
		firstTurn = false

		var err error
		newMsgs, history, err = drainInbox(cfg.Inbox, emit, newMsgs, history)
		if err != nil {
			return newMsgs, err
		}

		msgs := history
		if cfg.Hooks.TransformContext != nil {
			m, err := cfg.Hooks.TransformContext(ctx, msgs)
			if err != nil {
				return newMsgs, err
			}
			msgs = m
		}
		if cfg.TextOnly {
			// Tool schemas cannot constrain old session entries or extension results;
			// strip here at the final model-facing boundary as Codex does.
			msgs = stripImages(msgs)
		}
		msgs = stripExternal(msgs)

		if err := emit(Event{Type: RequestHeader, System: system, Tools: slices.Clone(specs), Provider: cfg.Provider, Model: cfg.Model}); err != nil {
			return newMsgs, err
		}

		request := Request{
			SessionID:                 cfg.SessionID,
			System:                    system,
			Messages:                  msgs,
			Tools:                     specs,
			Provider:                  cfg.Provider,
			Model:                     cfg.Model,
			API:                       cfg.API,
			ProviderBinding:           cfg.ProviderBinding,
			MaxTokens:                 cfg.MaxTokens,
			ThinkingEffort:            cfg.ThinkingEffort,
			ThinkingFormat:            cfg.ThinkingFormat,
			MaxTokensField:            cfg.MaxTokensField,
			SupportsReasoningEffort:   cfg.SupportsReasoningEffort,
			ForceAdaptiveThinking:     cfg.ForceAdaptiveThinking,
			ThinkingLevelMap:          cfg.ThinkingLevelMap,
			ResponsesContext:          responsesContext,
			ResponsesCompactThreshold: cfg.ResponsesCompactThreshold,
			ContextTransformed:        cfg.Hooks.TransformContext != nil,
		}
		asst, err := streamWithRetry(ctx, cfg, request, emit)
		if err != nil {
			if request.ResponsesCompactThreshold > 0 &&
				cfg.ResponsesCompactFallback &&
				!serverCompactionFallback &&
				!errors.Is(err, ErrContextOverflow) {
				// A stale gateway capability or extension implementation must
				// not make auto mode unusable. Retry once without the optional
				// field; a real overflow still takes the standalone/local path.
				serverCompactionFallback = true
				cfg.ResponsesCompactThreshold = 0
				continue
			}
			// Context overflow: compact once (server-side, via hook) and retry
			// with the new context inside the same Run. The failed assistant
			// message stays in session history but is dropped by provider
			// replayable on the retry (skipAssistant skips stopReason error).
			if errors.Is(err, ErrContextOverflow) && !overflowRecovered && cfg.Hooks.OnContextOverflow != nil {
				overflowRecovered = true
				if emitErr := emit(Event{Type: CompactionStart, Reason: "overflow", WillRetry: true}); emitErr != nil {
					return newMsgs, fmt.Errorf("emit overflow compaction start: %w", emitErr)
				}
				outcome, herr := cfg.Hooks.OnContextOverflow(ctx, request)
				if herr == nil {
					if cfg.Telemetry != nil {
						cfg.Telemetry.Reset("compaction")
					}
					history = outcome.Context.Messages
					responsesContext = nil
					if outcome.Context.Responses != nil {
						responsesContext = slices.Clone(outcome.Context.Responses.Items)
					}
					if emitErr := emit(Event{
						Type: CompactionEnd, Reason: "overflow", OK: true,
						WillRetry: true, Status: "committed", EntryID: outcome.EntryID,
						Strategy: outcome.Strategy, FromExtension: outcome.FromExtension,
						FirstKeptEntryID: outcome.FirstKeptEntryID,
						TokensBefore:     outcome.TokensBefore, Usage: outcome.Usage,
					}); emitErr != nil {
						return newMsgs, fmt.Errorf("emit overflow compaction end: %w", emitErr)
					}
					continue
				}
				if emitErr := emit(Event{Type: CompactionEnd, Reason: "overflow", WillRetry: true, Status: "failed"}); emitErr != nil {
					return newMsgs, errors.Join(herr, fmt.Errorf("emit overflow compaction end: %w", emitErr))
				}
			}
			_ = emit(Event{Type: AgentEnd, Messages: newMsgs})
			return newMsgs, err
		}
		cleanAssistant := asst
		cleanAssistant.ResponsesItems = nil
		newMsgs = append(newMsgs, cleanAssistant)
		if len(asst.ResponsesItems) > 0 {
			if cfg.Telemetry != nil {
				cfg.Telemetry.Reset("server_compaction")
			}
			// Promote provider compaction immediately. Keeping it only on the
			// transient assistant works for an in-process adapter but is lost
			// when the next tool round crosses the extension JSON-RPC boundary
			// (ResponsesItems is intentionally json:"-").
			responsesContext = make([]json.RawMessage, len(asst.ResponsesItems))
			for i, item := range asst.ResponsesItems {
				responsesContext[i] = slices.Clone(item)
			}
			// ResponsesItems is the complete terminal output from the latest
			// compaction item onward, including this assistant. Re-adding the
			// portable assistant would duplicate its message/tool calls.
			history = nil
		} else {
			history = append(history, cleanAssistant)
		}

		calls := cleanAssistant.ToolCalls()
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
		turnEndedAt := time.Now()
		if err := emit(Event{
			Type:       TurnEnd,
			Timestamp:  turnEndedAt.UnixMilli(),
			DurationMs: turnEndedAt.Sub(turnStartedAt).Milliseconds(),
			// Provider-owned checkpoint items are useful only to the live loop
			// state. Keeping them on replayable TurnEnd events pins multi-MiB
			// encrypted windows for every completed idle run.
			Message:     &cleanAssistant,
			ToolResults: results,
		}); err != nil {
			return newMsgs, err
		}
		if asst.StopReason == "error" || asst.StopReason == "aborted" || terminate {
			break
		}
		if len(calls) == 0 && !cfg.Inbox.Has() {
			break
		}
	}
	if err := emit(Event{Type: AgentEnd, Messages: newMsgs}); err != nil {
		return newMsgs, err
	}
	return newMsgs, nil
}

func stripExternal(msgs []types.Message) []types.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]types.Message, len(msgs))
	for i, msg := range msgs {
		msg.External = nil
		out[i] = msg
	}
	return out
}

func drainInbox(inbox *Inbox, emit func(Event) error, newMsgs, history []types.Message) ([]types.Message, []types.Message, error) {
	for _, user := range inbox.Take() {
		user.Role = "user"
		if user.Timestamp == 0 {
			user.Timestamp = time.Now().UnixMilli()
		}
		u := user
		if err := emit(Event{Type: MessageStart, Message: &u}); err != nil {
			return newMsgs, history, err
		}
		if err := emit(Event{Type: MessageEnd, Message: &u}); err != nil {
			return newMsgs, history, err
		}
		newMsgs = append(newMsgs, user)
		history = append(history, user)
	}
	return newMsgs, history, nil
}

func stripImages(msgs []types.Message) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].Content = make([]types.Content, 0, len(m.Content))
		for _, c := range m.Content {
			if c.Type != "image" {
				out[i].Content = append(out[i].Content, c)
			}
		}
	}
	return out
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
		var lastDelta time.Time
		deltas := 0
		argumentConsumers := map[string]ToolArgumentDiffConsumer{}
		argumentConsumerNames := map[string]string{}
		partial := types.Message{Role: "assistant", Provider: cfg.Provider, Model: cfg.Model, Timestamp: time.Now().UnixMilli()}
		// The start message escapes to replay readers; keep the local accumulator
		// separate so retaining a canceled partial never mutates a published event.
		latest := partial
		if err := emit(Event{Type: MessageStart, Message: &partial}); err != nil {
			return partial, err
		}
		asst, err := cfg.Streamer.Stream(ctx, req, func(d AssistantDelta) error {
			arrived := time.Now()
			if firstDelta.IsZero() {
				firstDelta = arrived
			}
			m := d.Partial
			latest = m
			if err := emit(Event{Type: MessageUpdate, Message: &m, AssistantMessageEvent: &d}); err != nil {
				return err
			}
			if d.Type == "custom_tool_call_input_delta" && d.ToolCallID != "" {
				consumer := argumentConsumers[d.ToolCallID]
				if consumer == nil {
					for _, tool := range cfg.Tools {
						if tool.Name() != d.ToolName {
							continue
						}
						if provider, ok := tool.(ToolArgumentDiffProvider); ok {
							consumer = provider.NewArgumentDiffConsumer()
							argumentConsumers[d.ToolCallID] = consumer
							argumentConsumerNames[d.ToolCallID] = d.ToolName
						}
						break
					}
				}
				if consumer != nil {
					if value, ok := consumer.Consume(d.Delta); ok {
						return emit(Event{Type: PatchApplyUpdated, ToolCallID: d.ToolCallID, ToolName: d.ToolName, PartialResult: value})
					}
				}
			}
			deltas++
			if slog.Default().Enabled(ctx, slog.LevelDebug) {
				emitTime := time.Since(arrived)
				gap := time.Duration(0)
				if !lastDelta.IsZero() {
					gap = arrived.Sub(lastDelta)
				}
				if deltas == 1 || deltas%128 == 0 || gap >= time.Second || emitTime >= 100*time.Millisecond {
					slog.DebugContext(ctx, "provider delta", "provider", cfg.Provider, "model", cfg.Model, "attempt", attempt, "delta_count", deltas, "gap_us", gap.Microseconds(), "emit_us", emitTime.Microseconds(), "delta_bytes", len(d.Delta))
				}
			}
			lastDelta = arrived
			return nil
		})
		if cfg.Telemetry != nil {
			observation := modelTelemetry(req, asst, err, time.Since(started).Milliseconds())
			observation.Attempt = attempt
			cfg.Telemetry.RecordModelRequest(observation)
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				// Cancellation ends generation, not the text already shown. Some
				// streamers return no accumulator on error, so retain the last delta.
				if len(asst.Content) == 0 {
					asst = latest
				}
				asst.Role = "assistant"
				asst.StopReason = "aborted"
				asst.ErrorMessage = ctx.Err().Error()
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
			if nonRetryableStreamError(err) {
				if asst.Role == "" {
					asst.Role = "assistant"
				}
				if asst.StopReason == "" {
					asst.StopReason = "error"
				}
				if asst.ErrorMessage == "" {
					asst.ErrorMessage = err.Error()
				}
				asst.IsError = true
				if asst.Provider == "" {
					asst.Provider = cfg.Provider
				}
				if asst.Model == "" {
					asst.Model = cfg.Model
				}
				// End the failed attempt explicitly so clients can discard the
				// partial stream and show the actual protocol error.
				_ = emit(Event{Type: MessageEnd, Message: &asst})
				return asst, fmt.Errorf("stream assistant response: %w", err)
			}
			continue
		}
		for _, call := range asst.ToolCalls() {
			consumer := argumentConsumers[call.ID]
			if consumer == nil {
				continue
			}
			if value, ok := consumer.Finish(); ok {
				if err := emit(Event{Type: PatchApplyUpdated, ToolCallID: call.ID, ToolName: argumentConsumerNames[call.ID], PartialResult: value}); err != nil {
					return asst, err
				}
			}
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
		if asst.StopReason == "error" {
			asst.IsError = true
			if asst.ErrorMessage == "" {
				asst.ErrorMessage = "provider response failed"
			}
		}
		if req.ResponsesCompactThreshold <= 0 {
			// Why: ResponsesItems is a private inline-compaction channel, not
			// ordinary provider output. Discard it at the loop boundary unless
			// this exact request enabled context_management; emitter-only
			// gating is too late because the next tool round also uses it.
			asst.ResponsesItems = nil
		}
		serverCompacted := len(asst.ResponsesItems) > 0
		if serverCompacted {
			if err := emit(Event{Type: CompactionStart, Reason: "server", Strategy: "openai-inline"}); err != nil {
				return asst, err
			}
		}
		if err := emit(Event{Type: MessageEnd, Message: &asst}); err != nil {
			if serverCompacted {
				_ = emit(Event{Type: CompactionEnd, Reason: "server", Strategy: "openai-inline", Status: "failed"})
			}
			return asst, err
		}
		if serverCompacted {
			if err := emit(Event{Type: CompactionEnd, Reason: "server", OK: true, Strategy: "openai-inline", Status: "committed"}); err != nil {
				return asst, err
			}
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
		last = types.Message{
			Role: "assistant", Provider: cfg.Provider, Model: cfg.Model,
			StopReason: "error", ErrorMessage: fmt.Sprint(lastErr), IsError: true,
		}
		_ = emit(Event{Type: MessageStart, Message: &last})
		_ = emit(Event{Type: MessageEnd, Message: &last})
	}
	return last, lastErr
}

type nonRetryableStreamFailure interface {
	NonRetryable() bool
}

func nonRetryableStreamError(err error) bool {
	var failure nonRetryableStreamFailure
	return errors.As(err, &failure) && failure.NonRetryable()
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
		call       types.Content
		args       map[string]any
		tool       Tool
		immediate  *types.Message // set → skip execute
		diagnostic telemetry.ToolDiagnostic
		terminate  bool
	}
	preps := make([]prep, len(calls))
	for i, c := range calls {
		args := c.Arguments
		if c.ToolType == "custom" {
			args = map[string]any{"input": c.Input}
		}
		if args == nil {
			args = map[string]any{}
		}
		p := prep{call: c, args: args}
		t, ok := byName[c.Name]
		if !ok {
			m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, ToolType: c.ToolType, Content: []types.Content{{Type: "text", Text: "unknown tool " + c.Name}}, IsError: true}
			p.immediate = &m
			p.diagnostic = telemetry.ToolDiagnostic{Status: "rejected", Kind: "unknown_tool", FaultDomain: "model_input"}
			preps[i] = p
			continue
		}
		if v, ok := t.(ToolValidator); ok && c.ToolType != "custom" {
			if err := v.Validate(args); err != nil {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, ToolType: c.ToolType, Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
				p.immediate = &m
				p.diagnostic = telemetry.ToolDiagnostic{Status: "rejected", Kind: "invalid_arguments", FaultDomain: "model_input"}
				preps[i] = p
				continue
			}
		}
		if cfg.Hooks.BeforeTool != nil {
			a, b, r, term, err := cfg.Hooks.BeforeTool(ctx, c.Name, args)
			if err != nil {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, ToolType: c.ToolType, Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
				p.immediate = &m
				p.diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "extension_failed", FaultDomain: "extension"}
				p.terminate = term
				preps[i] = p
				continue
			}
			args, p.terminate = a, term
			p.args = args
			if b {
				m := types.Message{Role: "toolResult", ToolCallID: c.ID, ToolName: c.Name, ToolType: c.ToolType, Content: []types.Content{{Type: "text", Text: r}}, IsError: true}
				p.immediate = &m
				p.diagnostic = telemetry.ToolDiagnostic{Status: "rejected", Kind: "extension_rejected", FaultDomain: "extension"}
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
		startedAt := time.Now()
		_ = emit(Event{
			Type:       ToolExecutionStart,
			Timestamp:  startedAt.UnixMilli(),
			ToolCallID: p.call.ID,
			ToolName:   p.call.Name,
			Args:       p.args,
		})
		if p.immediate != nil {
			finishedAt := time.Now()
			dur := finishedAt.Sub(startedAt).Milliseconds()
			msg := *p.immediate
			msg.DurationMs = dur
			msg.Timestamp = finishedAt.UnixMilli()
			out[i] = msg
			if cfg.Telemetry != nil {
				cfg.Telemetry.RecordTool(p.call.Name, p.call.ID, dur, true, p.diagnostic, nil)
			}
			_ = emit(Event{
				Type:       ToolExecutionEnd,
				Timestamp:  finishedAt.UnixMilli(),
				DurationMs: dur,
				ToolCallID: p.call.ID,
				ToolName:   p.call.Name,
				Args:       p.args,
				IsError:    true,
			})
			return
		}
		var res ToolResult
		if p.call.ToolType == "custom" {
			raw, _ := p.args["input"].(string)
			if freeform, ok := p.tool.(FreeformTool); ok {
				res = freeform.ExecuteRaw(ctx, raw)
			} else {
				res = ToolResult{
					Content: []types.Content{{Type: "text", Text: "tool does not accept freeform input"}}, IsError: true,
					Diagnostic: telemetry.ToolDiagnostic{Status: "rejected", Kind: "invalid_arguments", FaultDomain: "model_input"},
				}
			}
		} else if progress, ok := p.tool.(ProgressTool); ok {
			progressEmit := func(value any) {
				_ = emit(Event{
					Type:          ToolExecutionUpdate,
					ToolCallID:    p.call.ID,
					ToolName:      p.call.Name,
					Args:          p.args,
					PartialResult: value,
				})
			}
			res = progress.ExecuteWithProgress(ctx, p.args, progressEmit)
		} else {
			res = p.tool.Execute(ctx, p.args)
		}
		if cfg.Hooks.AfterTool != nil {
			if nr, err := cfg.Hooks.AfterTool(ctx, p.call.Name, p.args, res); err == nil {
				res = nr
			}
		}
		if cfg.OutputStore != nil {
			// The bounded result is what reaches the model, the jsonl, and the
			// SSE stream; the complete text stays in the session spill file.
			content, ref := cfg.OutputStore.Normalize(cfg.SessionID, p.call.Name, p.args, res.Content, res.Details)
			res.Content = content
			res.Details = tooloutput.MergeDetails(res.Details, ref)
		}
		finishedAt := time.Now()
		dur := finishedAt.Sub(startedAt).Milliseconds()
		msg := types.Message{
			Role:       "toolResult",
			ToolCallID: p.call.ID,
			ToolName:   p.call.Name,
			ToolType:   p.call.ToolType,
			Content:    res.Content,
			Details:    res.Details,
			IsError:    res.IsError,
			DurationMs: dur,
			Timestamp:  finishedAt.UnixMilli(),
		}
		out[i] = msg
		p.terminate = p.terminate || res.Terminate
		preps[i] = p
		if cfg.Telemetry != nil {
			cfg.Telemetry.RecordTool(p.call.Name, p.call.ID, dur, res.IsError, res.Diagnostic, toolTelemetryAttrs(res.Details))
		}
		_ = emit(Event{
			Type:       ToolExecutionEnd,
			Timestamp:  finishedAt.UnixMilli(),
			DurationMs: dur,
			ToolCallID: p.call.ID,
			ToolName:   p.call.Name,
			Args:       p.args,
			Result:     res,
			IsError:    res.IsError,
		})
	}
	if cfg.Parallel {
		var wg sync.WaitGroup
		for i := range calls {
			wg.Go(func() {
				run(i)
			})
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

func modelTelemetry(request Request, response types.Message, err error, durationMS int64) telemetry.ModelRequest {
	chunks := make([]string, 0, len(request.ResponsesContext)+len(request.Messages))
	for _, item := range request.ResponsesContext {
		chunks = append(chunks, telemetry.Hash(json.RawMessage(item)))
	}
	for _, message := range request.Messages {
		chunks = append(chunks, telemetry.Hash(message))
	}
	binding := struct {
		Provider, API, BaseURL, Model, Compaction string
	}{
		request.ProviderBinding.Provider,
		request.ProviderBinding.API,
		request.ProviderBinding.BaseURL,
		request.ProviderBinding.Model,
		request.ProviderBinding.Compaction,
	}
	shape := struct {
		Provider, Model, API, ThinkingEffort, ThinkingFormat, MaxTokensField string
		MaxTokens, CompactThreshold                                          int
		Binding                                                              any
	}{
		request.Provider, request.Model, request.API, request.ThinkingEffort,
		request.ThinkingFormat, request.MaxTokensField, request.MaxTokens,
		request.ResponsesCompactThreshold, binding,
	}
	errorKind := ""
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		errorKind = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		errorKind = "timeout"
	case errors.Is(err, ErrContextOverflow):
		errorKind = "context_overflow"
	default:
		errorKind = "provider_error"
	}
	return telemetry.ModelRequest{
		Provider: request.Provider, Model: request.Model, API: request.API,
		StaticHash: telemetry.Hash(struct {
			System string
			Tools  []ToolSpec
		}{request.System, request.Tools}),
		ShapeHash: telemetry.Hash(shape), BindingHash: telemetry.Hash(request.ProviderBinding), HistoryChunks: chunks,
		Usage: response.Usage, DurationMS: durationMS, Failed: err != nil, ErrorKind: errorKind,
	}
}

func toolTelemetryAttrs(details any) map[string]any {
	if details == nil {
		return nil
	}
	data, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		return nil
	}
	out := map[string]any{}
	if exit, ok := values["exit_code"].(float64); ok {
		out["process.exit.code"] = int(exit)
	}
	if status, ok := values["status"].(string); ok {
		out["ki.tool.task_status"] = status
	}
	if truncation, ok := values["truncation"].(map[string]any); ok {
		if truncated, ok := truncation["truncated"].(bool); ok {
			out["ki.tool.output_truncated"] = truncated
		}
		if total, ok := truncation["total_bytes"].(float64); ok {
			out["ki.tool.output_bytes"] = int64(total)
		}
	}
	return out
}

// rejectToolCalls turns every tool call into an error result without executing
// it. Used when an assistant message was truncated by the output token limit:
// streamed arguments may be incomplete, so none are safe to run (pi
// failToolCallsFromTruncatedMessage). The model sees the errors and re-issues.
func rejectToolCalls(calls []types.Content, emit func(Event) error) []types.Message {
	out := make([]types.Message, 0, len(calls))
	for _, c := range calls {
		args := c.Arguments
		if c.ToolType == "custom" {
			args = map[string]any{"input": c.Input}
		}
		startedAt := time.Now()
		_ = emit(Event{Type: ToolExecutionStart, Timestamp: startedAt.UnixMilli(), ToolCallID: c.ID, ToolName: c.Name, Args: args})
		finishedAt := time.Now()
		dur := finishedAt.Sub(startedAt).Milliseconds()
		msg := types.Message{
			Role:       "toolResult",
			ToolCallID: c.ID,
			ToolName:   c.Name,
			ToolType:   c.ToolType,
			Content: []types.Content{{Type: "text", Text: fmt.Sprintf(
				"Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", c.Name)}},
			IsError:    true,
			DurationMs: dur,
			Timestamp:  finishedAt.UnixMilli(),
		}
		_ = emit(Event{Type: ToolExecutionEnd, Timestamp: finishedAt.UnixMilli(), DurationMs: dur, ToolCallID: c.ID, ToolName: c.Name, IsError: true})
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
