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

	"github.com/google/uuid"

	"ki/internal/telemetry"
	toolapi "ki/internal/tool"
	"ki/internal/tool/output"
	"ki/internal/types"
)

type EventType string

var errAssistant = errors.New("assistant error")

const (
	ProcessUpdated      EventType = "process_updated"
	AgentUpdated        EventType = "agent_updated"
	AgentStart          EventType = "agent_start"
	AgentEnd            EventType = "agent_end"
	TurnStart           EventType = "turn_start"
	TurnEnd             EventType = "turn_end"
	RequestHeader       EventType = "request_header"
	MessageStart        EventType = "message_start"
	MessageUpdate       EventType = "message_update"
	MessageEnd          EventType = "message_end"
	ToolExecutionStart  EventType = "tool_execution_start"
	ToolExecutionUpdate EventType = "tool_execution_update"
	ToolExecutionEnd    EventType = "tool_execution_end"
	PatchApplyUpdated   EventType = "patch_apply_updated"
	CompactionStart     EventType = "compaction_start"
	CompactionEnd       EventType = "compaction_end"
	ContextUsage        EventType = "context_usage"
	QueueChanged        EventType = "queue_changed"
	SteerAccepted       EventType = "steer_accepted"
	RunAborted          EventType = "run_aborted"
	ExtensionError      EventType = "extension_error"
	ExtensionNotice     EventType = "extension_notice"
	ExtensionUIPrompt   EventType = "extension_ui_prompt"
	AgentSettled        EventType = "agent_settled"
	RuntimeReady        EventType = "runtime_ready"
)

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

type Event struct {
	Process               any               `json:"process,omitempty"`
	Agent                 any               `json:"agent,omitempty"`
	Type                  EventType         `json:"type"`
	EntryID               string            `json:"entryId,omitempty"`
	LifecycleEntryID      string            `json:"lifecycleEntryId,omitempty"`
	ParentID              *string           `json:"parentId,omitempty"`
	Timestamp             int64             `json:"timestamp,omitzero"`
	DurationMs            int64             `json:"durationMs"`
	Message               *types.Message    `json:"message,omitempty"`
	MessagePatch          *MessagePatch     `json:"messagePatch,omitempty"`
	MessageStream         int64             `json:"messageStream,omitempty"`
	Messages              []types.Message   `json:"messages,omitempty"`
	ToolResults           []types.Message   `json:"toolResults,omitempty"`
	AssistantMessageEvent *AssistantDelta   `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string            `json:"toolCallId,omitempty"`
	ToolName              string            `json:"toolName,omitempty"`
	RequestedToolName     string            `json:"requestedToolName,omitempty"`
	Args                  map[string]any    `json:"args,omitempty"`
	PartialResult         any               `json:"partialResult,omitempty"`
	Result                any               `json:"result,omitempty"`
	IsError               bool              `json:"isError,omitzero"`
	System                string            `json:"system,omitempty"`
	Tools                 []toolapi.Spec    `json:"tools,omitempty"`
	Reason                string            `json:"reason,omitempty"`
	CancelSource          string            `json:"cancelSource,omitempty"`
	OK                    bool              `json:"ok,omitzero"`
	WillRetry             bool              `json:"willRetry,omitzero"`
	Strategy              string            `json:"strategy,omitempty"`
	Status                string            `json:"status,omitempty"`
	FromExtension         bool              `json:"fromExtension,omitzero"`
	FirstKeptEntryID      string            `json:"firstKeptEntryId,omitempty"`
	TokensBefore          int               `json:"tokensBefore,omitzero"`
	Usage                 *types.Usage      `json:"usage,omitempty"`
	CacheMiss             *types.CacheMiss  `json:"cacheMiss,omitempty"`
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
	ParentCallID          string            `json:"parentCallId,omitempty"`
	CellID                string            `json:"cellId,omitempty"`
	Seq                   int64             `json:"seq,omitempty"`
	PromptUnchanged       bool              `json:"promptUnchanged,omitempty"`
	Blank                 bool              `json:"-"`
	BufferedAt            time.Time         `json:"-"`
	BufferedBytes         int               `json:"-"`
}

type AssistantDelta struct {
	Type       string        `json:"type"`
	Delta      string        `json:"delta,omitempty"`
	ToolCallID string        `json:"toolCallId,omitempty"`
	ToolName   string        `json:"toolName,omitempty"`
	Partial    types.Message `json:"partial"`
}

type Streamer interface {
	Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error)
}

type Request struct {
	SessionID                 string                `json:"sessionId"`
	CacheKey                  string                `json:"cacheKey,omitempty"`
	TurnID                    string                `json:"turnId,omitempty"`
	System                    string                `json:"system"`
	Messages                  []types.Message       `json:"messages"`
	Tools                     []toolapi.Spec        `json:"tools"`
	Provider                  string                `json:"provider"`
	Model                     string                `json:"model"`
	API                       string                `json:"api"`
	ProviderBinding           types.ProviderBinding `json:"-"`
	MaxTokens                 int                   `json:"maxTokens,omitzero"`
	ThinkingEffort            string                `json:"thinkingEffort,omitempty"`
	ThinkingFormat            string                `json:"thinkingFormat,omitempty"`
	MaxTokensField            string                `json:"maxTokensField,omitempty"`
	SupportsReasoningEffort   bool                  `json:"supportsReasoningEffort,omitzero"`
	ForceAdaptiveThinking     bool                  `json:"forceAdaptiveThinking,omitzero"`
	ThinkingLevelMap          map[string]*string    `json:"thinkingLevelMap,omitempty"`
	ResponsesContext          []json.RawMessage     `json:"responsesContext,omitempty"`
	ResponsesCompactThreshold int                   `json:"responsesCompactThreshold,omitzero"`
	ContextTransformed        bool                  `json:"-"`
}

type Hooks struct {
	BeforeRun          func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error)
	TransformContext   func(ctx context.Context, msgs []types.Message) ([]types.Message, error)
	BeforeTool         func(ctx context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error)
	AfterTool          func(ctx context.Context, name string, args map[string]any, res toolapi.Result) (toolapi.Result, error)
	ShouldCompact      func() bool
	OnContextThreshold func(ctx context.Context) (CompactionResult, error)
	OnContextOverflow  func(ctx context.Context, failed Request) (CompactionResult, error)
}

type CompactionResult struct {
	Compacted        bool
	Status           string
	Context          types.ModelContext
	EntryID          string
	Strategy         string
	FromExtension    bool
	FirstKeptEntryID string
	TokensBefore     int
	Usage            *types.Usage
}

var ErrContextOverflow = errors.New("context overflow")

type Inbox struct {
	mu       sync.Mutex
	pending  []types.Message
	activity chan struct{}
}

func (i *Inbox) Push(m types.Message) {
	if i == nil {
		return
	}
	m.Role = "user"
	i.mu.Lock()
	i.pending = append(i.pending, m)
	if i.activity != nil {
		close(i.activity)
	}
	i.activity = make(chan struct{})
	i.mu.Unlock()
}

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

func (i *Inbox) Has() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.pending) > 0
}

type Config struct {
	RunID                     string
	AgentID                   string
	Generation                uint64
	Streamer                  Streamer
	SessionID                 string
	CacheKey                  string
	TurnID                    string
	Tools                     []toolapi.Tool
	ModelTools                func([]types.Message) []toolapi.Tool
	OutputStore               *output.Store
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
	ResponsesCompactFallback  bool
	TextOnly                  bool
	System                    string
	Inbox                     *Inbox
	CommitUserMessage         func(types.Message, func() error) (bool, error)
}

func Run(ctx context.Context, prompt string, history []types.Message, cfg Config, emit func(Event) error) ([]types.Message, error) {
	return RunMessage(ctx, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: prompt}}}, history, cfg, emit)
}

func RunMessage(ctx context.Context, user types.Message, history []types.Message, cfg Config, emit func(Event) error) ([]types.Message, error) {
	if cfg.TurnID == "" {
		cfg.TurnID = uuid.NewString()
	}
	registry, err := toolapi.NewRegistry(cfg.Tools)
	if err != nil {
		return nil, err
	}
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
	turnStartedAt := time.Now()
	accepted, err := commitUserMessage(cfg.CommitUserMessage, user, func() error {
		if err := emit(Event{Type: AgentStart}); err != nil {
			return err
		}
		if err := emit(Event{Type: TurnStart, Timestamp: turnStartedAt.UnixMilli()}); err != nil {
			return err
		}
		return emitUserMessage(emit, user)
	})
	if err != nil || !accepted {
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

	responsesContext := slices.Clone(cfg.ResponsesContext)

	firstTurn := true
	overflowRecovered := false
	serverCompactionFallback := false
	thresholdCompactionStopped := false
	thresholdCompactionFailures := 0
	const maxThresholdCompactionFailures = 3
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
		newMsgs, history, err = drainInbox(cfg.Inbox, cfg.CommitUserMessage, emit, newMsgs, history)
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
			msgs = stripImages(msgs)
		}
		msgs = stripExternal(msgs)

		specs, err := modelToolSpecs(cfg, registry, msgs)
		if err != nil {
			return newMsgs, err
		}
		if err := emit(Event{Type: RequestHeader, System: system, Tools: slices.Clone(specs), Provider: cfg.Provider, Model: cfg.Model}); err != nil {
			return newMsgs, err
		}

		request := Request{
			SessionID:                 cfg.SessionID,
			CacheKey:                  cfg.CacheKey,
			TurnID:                    cfg.TurnID,
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
				serverCompactionFallback = true
				cfg.ResponsesCompactThreshold = 0
				continue
			}
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
			responsesContext = make([]json.RawMessage, len(asst.ResponsesItems))
			for i, item := range asst.ResponsesItems {
				responsesContext[i] = slices.Clone(item)
			}
			history = nil
		} else {
			history = append(history, cleanAssistant)
		}

		calls := cleanAssistant.ToolCalls()
		var results []types.Message
		terminate := false
		if len(calls) > 0 {
			if asst.StopReason == "length" {
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
			Type:        TurnEnd,
			Timestamp:   turnEndedAt.UnixMilli(),
			DurationMs:  turnEndedAt.Sub(turnStartedAt).Milliseconds(),
			Message:     &cleanAssistant,
			ToolResults: results,
		}); err != nil {
			return newMsgs, err
		}
		if asst.StopReason == "error" || asst.StopReason == "aborted" || terminate {
			break
		}
		if len(calls) == 0 && !cfg.Inbox.HasWork() {
			break
		}
		if !thresholdCompactionStopped && cfg.Hooks.ShouldCompact != nil && cfg.Hooks.OnContextThreshold != nil && cfg.Hooks.ShouldCompact() {
			if err := emit(Event{Type: CompactionStart, Reason: "threshold"}); err != nil {
				return newMsgs, fmt.Errorf("emit threshold compaction start: %w", err)
			}
			outcome, compactErr := cfg.Hooks.OnContextThreshold(ctx)
			if compactErr != nil {
				thresholdCompactionFailures++
				thresholdCompactionStopped = thresholdCompactionFailures >= maxThresholdCompactionFailures
				if emitErr := emit(Event{Type: CompactionEnd, Reason: "threshold", Status: "failed"}); emitErr != nil {
					return newMsgs, errors.Join(compactErr, fmt.Errorf("emit threshold compaction end: %w", emitErr))
				}
				if errors.Is(compactErr, context.Canceled) || errors.Is(compactErr, context.DeadlineExceeded) {
					return newMsgs, compactErr
				}
				continue
			}
			status := outcome.Status
			if status == "" {
				status = "empty"
			}
			if outcome.Compacted {
				thresholdCompactionFailures = 0
				status = "committed"
				history = outcome.Context.Messages
				responsesContext = nil
				if outcome.Context.Responses != nil {
					responsesContext = slices.Clone(outcome.Context.Responses.Items)
				}
				if cfg.Telemetry != nil {
					cfg.Telemetry.Reset("compaction")
				}
			} else {
				thresholdCompactionStopped = true
			}
			if err := emit(Event{
				Type: CompactionEnd, Reason: "threshold", OK: true, Status: status,
				EntryID: outcome.EntryID, Strategy: outcome.Strategy,
				FromExtension: outcome.FromExtension, FirstKeptEntryID: outcome.FirstKeptEntryID,
				TokensBefore: outcome.TokensBefore, Usage: outcome.Usage,
			}); err != nil {
				return newMsgs, fmt.Errorf("emit threshold compaction end: %w", err)
			}
		}
	}
	if err := emit(Event{Type: AgentEnd, Messages: newMsgs}); err != nil {
		return newMsgs, err
	}
	return newMsgs, nil
}

func modelToolSpecs(cfg Config, registry *toolapi.Registry, history []types.Message) ([]toolapi.Spec, error) {
	tools := cfg.Tools
	if cfg.ModelTools != nil {
		tools = cfg.ModelTools(slices.Clone(history))
	}
	var specs []toolapi.Spec
	seen := map[string]bool{}
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("model tools contains nil tool")
		}
		name, err := toolapi.Canonical(t.Name())
		if err != nil {
			return nil, fmt.Errorf("model tool: %w", err)
		}
		if _, allowed := registry.Lookup(name); !allowed {
			return nil, fmt.Errorf("model tool %q is not in the execution registry", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		specs = append(specs, toolapi.SpecFor(t))
	}
	return specs, nil
}

func stripExternal(msgs []types.Message) []types.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]types.Message, len(msgs))
	for i, msg := range msgs {
		msg.External = nil
		msg.ClientRequestID = ""
		msg.Completion = nil
		msg.ContextOnly = false
		out[i] = msg
	}
	return out
}

func commitUserMessage(commit func(types.Message, func() error) (bool, error), user types.Message, persist func() error) (bool, error) {
	if commit != nil {
		return commit(user, persist)
	}
	err := persist()
	return err == nil, err
}

func emitUserMessage(emit func(Event) error, user types.Message) error {
	if err := emit(Event{Type: MessageStart, Message: &user}); err != nil {
		return err
	}
	return emit(Event{Type: MessageEnd, Message: &user})
}

func drainInbox(inbox *Inbox, commit func(types.Message, func() error) (bool, error), emit func(Event) error, newMsgs, history []types.Message) ([]types.Message, []types.Message, error) {
	pending := inbox.Take()
	for index, user := range pending {
		user.Role = "user"
		if user.Timestamp == 0 {
			user.Timestamp = time.Now().UnixMilli()
		}
		accepted, err := commitUserMessage(commit, user, func() error { return emitUserMessage(emit, user) })
		if err != nil {
			inbox.mu.Lock()
			inbox.pending = append(pending[index:], inbox.pending...)
			inbox.mu.Unlock()
			return newMsgs, history, err
		}
		if !accepted {
			continue
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
		argumentConsumers := map[string]toolapi.ArgumentDiffConsumer{}
		argumentConsumerNames := map[string]string{}
		partial := types.Message{Role: "assistant", Provider: cfg.Provider, Model: cfg.Model, Timestamp: time.Now().UnixMilli()}
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
						if !toolapi.Equal(d.ToolName, tool.Name()) {
							continue
						}
						if provider, ok := tool.(toolapi.ArgumentDiffProvider); ok {
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
				if len(asst.Content) == 0 {
					asst = latest
				}
				asst.Role = "assistant"
				asst.StopReason = "aborted"
				asst.ErrorMessage = ctx.Err().Error()
				_ = emit(Event{Type: MessageEnd, Message: &asst})
				return asst, fmt.Errorf("stream assistant response: %w", err)
			}
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

func executeTools(ctx context.Context, cfg Config, calls []types.Content, emit func(Event) error) ([]types.Message, bool) {
	dispatcher, err := NewToolDispatcher(cfg, emit)
	if err != nil {
		cfg.Tools = nil
		dispatcher, _ = NewToolDispatcher(cfg, emit)
	}
	return dispatcher.executeBatch(ctx, calls)
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
			Tools  []toolapi.Spec
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
				"toolapi.Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", c.Name)}},
			IsError:    true,
			DurationMs: dur,
			Timestamp:  finishedAt.UnixMilli(),
		}
		_ = emit(Event{Type: ToolExecutionEnd, Timestamp: finishedAt.UnixMilli(), DurationMs: dur, ToolCallID: c.ID, ToolName: c.Name, IsError: true})
		out = append(out, msg)
	}
	return out
}

func EventOrder(evs []Event) []string {
	var s []string
	for _, e := range evs {
		s = append(s, string(e.Type))
	}
	return s
}
