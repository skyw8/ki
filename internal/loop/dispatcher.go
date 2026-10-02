package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"ki/internal/telemetry"
	toolapi "ki/internal/tool"
	"ki/internal/tool/discovery"
	"ki/internal/tool/output"
	"ki/internal/types"
)

// NestedToolCall is a host-attributed tool request, not a provider message.
// The parent supplies ParentCallID/CellID from its trusted execution binding.
// Input selects freeform execution; otherwise Arguments is a JSON function input.
type NestedToolCall struct {
	ParentCallID string
	CellID       string
	Name         string
	Arguments    map[string]any
	Input        *string
}

// ToolDispatcher owns the fixed tool capabilities and policy of one run.
// It returns full post-hook intermediate results to nested callers, while only
// separately bounded audit events reach persistence and SSE.
type ToolDispatcher struct {
	cfg      Config
	registry *toolapi.Registry
	emit     func(Event) error
	nextCall atomic.Uint64
}

// NewToolDispatcher snapshots the allowed tools. A worker cannot expand this
// registry, choose execution identities, or bypass the normal interception path.
func NewToolDispatcher(cfg Config, emit func(Event) error) (*ToolDispatcher, error) {
	cfg.Tools = slices.Clone(cfg.Tools)
	registry, err := toolapi.NewRegistry(cfg.Tools)
	if err != nil {
		return nil, err
	}
	if emit == nil {
		emit = func(Event) error { return nil }
	}
	return &ToolDispatcher{cfg: cfg, registry: registry, emit: emit}, nil
}

// DispatchNested executes a single nested request without creating a transcript
// toolResult. Only the enclosing provider-issued call has such a message.
// Terminate combines BeforeTool and the tool result; the enclosing runtime must
// stop accepting work and propagate it to its own outer result.
func (d *ToolDispatcher) DispatchNested(ctx context.Context, call NestedToolCall) (toolapi.Result, error) {
	if call.ParentCallID == "" || call.CellID == "" {
		return toolapi.Result{}, errors.New("nested tool call requires a parent call and cell")
	}
	content := types.Content{
		Type: "toolCall", ID: fmt.Sprintf("%s:nested-%d", call.ParentCallID, d.nextCall.Add(1)),
		Name: call.Name, Arguments: call.Arguments,
	}
	if call.Input != nil {
		content.ToolType, content.Input = "custom", *call.Input
	}
	origin := nestedOrigin{parentCallID: call.ParentCallID, cellID: call.CellID}
	prepared := d.prepare(ctx, content, true)
	outcome := d.execute(ctx, prepared, origin)
	return outcome.full, outcome.err
}

// NotifyNested publishes attributed progress rather than an unmatched extra
// provider toolResult. The enclosing runtime supplies only its bound identities.
func (d *ToolDispatcher) NotifyNested(parentCallID, cellID string, value any) error {
	if parentCallID == "" || cellID == "" {
		return errors.New("nested notification requires a parent call and cell")
	}
	return d.emitEvent(Event{
		Type: ToolExecutionUpdate, Timestamp: time.Now().UnixMilli(),
		ToolCallID: parentCallID, ParentCallID: parentCallID, CellID: cellID,
		ToolName: "exec", RequestedToolName: "exec", PartialResult: value,
	}, true)
}

type nestedOrigin struct{ parentCallID, cellID string }

func (o nestedOrigin) nested() bool { return o.parentCallID != "" }

type preparedTool struct {
	call      types.Content
	args      map[string]any
	tool      toolapi.Tool
	immediate *toolapi.Result
	terminate bool
}

type dispatchedTool struct {
	full      toolapi.Result
	message   types.Message
	terminate bool
	err       error
}

func rejectedTool(text, status, kind, domain string) *toolapi.Result {
	return &toolapi.Result{
		Content: []types.Content{{Type: "text", Text: text}}, IsError: true,
		Diagnostic: telemetry.ToolDiagnostic{Status: status, Kind: kind, FaultDomain: domain},
	}
}

func (d *ToolDispatcher) prepare(ctx context.Context, call types.Content, nested bool) preparedTool {
	args := call.Arguments
	if call.ToolType == "custom" {
		args = map[string]any{"input": call.Input}
	}
	if args == nil {
		args = map[string]any{}
	}
	p := preparedTool{call: call, args: args}
	t, ok := d.registry.Lookup(call.Name)
	if !ok {
		p.immediate = rejectedTool("unknown tool "+call.Name, "rejected", "unknown_tool", "model_input")
		return p
	}
	// Keep the resolved tool even on rejection: aliases must never change the
	// canonical policy/audit name when validation fails.
	p.tool = t
	canonical := toolapi.MustCanonical(t.Name())
	if nested && (canonical == "exec" || canonical == "wait") {
		p.immediate = rejectedTool(canonical+" cannot be called from code mode", "rejected", "recursive_tool", "model_input")
		return p
	}
	if nested && ctx.Err() != nil {
		p.immediate = rejectedTool(ctx.Err().Error(), "cancelled", "cancelled", "cancellation")
		return p
	}
	if v, ok := t.(toolapi.Validator); ok && call.ToolType != "custom" {
		if err := v.Validate(args); err != nil {
			p.immediate = rejectedTool(err.Error(), "rejected", "invalid_arguments", "model_input")
			return p
		}
	}
	if d.cfg.Hooks.BeforeTool != nil {
		a, blocked, reason, terminate, err := d.cfg.Hooks.BeforeTool(ctx, canonical, args)
		p.terminate = terminate
		if err != nil {
			p.immediate = rejectedTool(err.Error(), "failed", "extension_failed", "extension")
			return p
		}
		p.args = a
		if blocked {
			p.immediate = rejectedTool(reason, "rejected", "extension_rejected", "extension")
		}
	}
	return p
}

func (d *ToolDispatcher) execute(ctx context.Context, p preparedTool, origin nestedOrigin) dispatchedTool {
	nested := origin.nested()
	canonical := p.call.Name
	if p.tool != nil {
		canonical = toolapi.MustCanonical(p.tool.Name())
	}
	startedAt := time.Now()
	event := func(typ EventType) Event {
		return Event{
			Type: typ, ToolCallID: p.call.ID, ToolName: canonical, RequestedToolName: p.call.Name,
			ParentCallID: origin.parentCallID, CellID: origin.cellID, Args: p.args,
		}
	}
	start := event(ToolExecutionStart)
	start.Timestamp = startedAt.UnixMilli()
	if err := d.emitEvent(start, nested); err != nil {
		// Never execute effects whose start cannot be durably attributed.
		return dispatchedTool{err: err}
	}
	var res toolapi.Result
	var executionErr error
	if p.immediate != nil {
		res = *p.immediate
	} else {
		executionCtx := toolapi.WithExecutionIdentity(ctx, toolapi.ExecutionIdentity{
			RunID: d.cfg.RunID, AgentID: d.cfg.AgentID, Generation: d.cfg.Generation, CallID: p.call.ID,
		})
		if nested {
			var cancel context.CancelFunc
			executionCtx, cancel = context.WithCancel(executionCtx)
			defer cancel()
			// ProgressTool cannot return emitter errors. Retain the first and
			// cancel execution so a failed audit stream does not silently continue.
			var progressMu sync.Mutex
			var progressErr error
			res = executePreparedTool(executionCtx, p, func(value any) {
				progress := event(ToolExecutionUpdate)
				progress.Timestamp, progress.PartialResult = time.Now().UnixMilli(), value
				if err := d.emitEvent(progress, true); err != nil {
					progressMu.Lock()
					if progressErr == nil {
						progressErr = err
					}
					progressMu.Unlock()
					cancel()
				}
			})
			progressMu.Lock()
			executionErr = progressErr
			progressMu.Unlock()
		} else {
			res = executePreparedTool(executionCtx, p, func(value any) {
				progress := event(ToolExecutionUpdate)
				progress.PartialResult = value
				_ = d.emitEvent(progress, false)
			})
		}
		if d.cfg.Hooks.AfterTool != nil {
			if nr, err := d.cfg.Hooks.AfterTool(ctx, canonical, p.args, res); err == nil {
				res = nr
			} else if nested || canonical == discovery.Name {
				// Failed output policy cannot expose original results to JS or
				// publish discovery identities that would load their schemas.
				terminate := res.Terminate
				res = *rejectedTool(err.Error(), "failed", "extension_failed", "extension")
				res.Terminate = terminate
				executionErr = errors.Join(executionErr, err)
			}
		}
	}
	res.Terminate = p.terminate || res.Terminate
	full := res
	published := res
	if p.immediate == nil || nested {
		if d.cfg.OutputStore != nil {
			content, ref := d.cfg.OutputStore.Normalize(d.cfg.SessionID, canonical, p.args, res.Content, res.Details)
			published.Content = content
			published.Details = output.MergeDetails(res.Details, ref)
		}
	}
	finishedAt := time.Now()
	dur := finishedAt.Sub(startedAt).Milliseconds()
	d.recordTelemetry(canonical, p, published, dur, origin)
	end := event(ToolExecutionEnd)
	end.Timestamp, end.DurationMs, end.IsError = finishedAt.UnixMilli(), dur, published.IsError
	if p.immediate == nil || nested {
		end.Result = published
	}
	emitErr := d.emitEvent(end, nested)
	return dispatchedTool{
		full: full, terminate: res.Terminate, err: errors.Join(executionErr, emitErr),
		message: types.Message{
			Role: "toolResult", ToolCallID: p.call.ID, ToolName: p.call.Name, ToolType: p.call.ToolType,
			Content: published.Content, Details: published.Details, IsError: published.IsError,
			DurationMs: dur, Timestamp: finishedAt.UnixMilli(),
		},
	}
}

func executePreparedTool(ctx context.Context, p preparedTool, progressEmit func(any)) toolapi.Result {
	if p.call.ToolType == "custom" {
		raw, _ := p.args["input"].(string)
		if freeform, ok := p.tool.(toolapi.FreeformTool); ok {
			return freeform.ExecuteRaw(ctx, raw)
		}
		return *rejectedTool("tool does not accept freeform input", "rejected", "invalid_arguments", "model_input")
	}
	if progress, ok := p.tool.(toolapi.ProgressTool); ok {
		return progress.ExecuteWithProgress(ctx, p.args, progressEmit)
	}
	return p.tool.Execute(ctx, p.args)
}

func (d *ToolDispatcher) recordTelemetry(canonical string, p preparedTool, res toolapi.Result, dur int64, origin nestedOrigin) {
	if d.cfg.Telemetry == nil {
		return
	}
	attrs := toolTelemetryAttrs(res.Details)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["ki.agent.id"], attrs["ki.agent.generation"] = d.cfg.AgentID, d.cfg.Generation
	attrs["ki.tool.requested_name"] = p.call.Name
	if origin.nested() {
		attrs["ki.tool.parent_call_id"], attrs["ki.code.cell_id"] = origin.parentCallID, origin.cellID
	}
	if canonical == "wait_agent" || canonical == "send_message" || canonical == "followup_task" {
		if raw, err := json.Marshal(res.Details); err == nil {
			var receipt map[string]any
			if json.Unmarshal(raw, &receipt) == nil {
				for _, key := range []string{"wake_reason", "timed_out", "status", "task_name", "agent_id"} {
					if value, ok := receipt[key]; ok {
						attrs["ki.coordination."+key] = value
					}
				}
			}
		}
	}
	d.cfg.Telemetry.RecordTool(canonical, p.call.ID, dur, res.IsError, res.Diagnostic, attrs)
}

func (d *ToolDispatcher) emitEvent(ev Event, strict bool) error {
	if strict {
		var err error
		ev, err = BoundNestedToolEvent(ev)
		if err != nil {
			return err
		}
		return d.emit(ev)
	}
	// Ordinary execution retains its historical best-effort event behavior.
	_ = d.emit(ev)
	return nil
}

const nestedAuditFieldBytes = 16 << 10
const nestedAuditResultBytes = 64 << 10

// BoundNestedToolEvent snapshots and bounds nested payloads, including Details
// and non-text content. These are audit limits, not intermediate JS limits.
// A serialized oversized value is replaced by an explicit preview, never
// silently presented as the complete original value.
func BoundNestedToolEvent(ev Event) (Event, error) {
	if ev.ParentCallID == "" {
		return ev, nil
	}
	if ev.Args != nil {
		value, err := boundAuditValue(ev.Args, nestedAuditFieldBytes)
		if err != nil {
			return ev, fmt.Errorf("encode nested tool arguments: %w", err)
		}
		ev.Args = value.(map[string]any)
	}
	if result, ok := ev.Result.(toolapi.Result); ok && result.Details != nil {
		// Preserve the bounded content envelope even when a tool produces a
		// large diff/detail payload; text-only spool does not bound Details.
		details, err := boundAuditValue(result.Details, nestedAuditFieldBytes)
		if err != nil {
			return ev, fmt.Errorf("encode nested tool details: %w", err)
		}
		result.Details = details
		ev.Result = result
	}
	for _, field := range []struct {
		value *any
		limit int
	}{
		{&ev.PartialResult, nestedAuditFieldBytes},
		{&ev.Result, nestedAuditResultBytes},
	} {
		if *field.value == nil {
			continue
		}
		value, err := boundAuditValue(*field.value, field.limit)
		if err != nil {
			return ev, fmt.Errorf("encode nested tool audit: %w", err)
		}
		*field.value = value
	}
	return ev, nil
}

func boundAuditValue(value any, limit int) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		preview := string(raw[:limit/4])
		for !utf8.ValidString(preview) {
			preview = preview[:len(preview)-1]
		}
		return map[string]any{"truncated": true, "bytes": len(raw), "preview": preview}, nil
	}
	var copy any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Audit snapshots must not round integer arguments/details through float64.
	decoder.UseNumber()
	if err := decoder.Decode(&copy); err != nil {
		return nil, err
	}
	// encoding/json snapshots remove shared mutable maps/slices before an
	// asynchronous subscriber can observe subsequent tool or hook mutations.
	return copy, nil
}

func (d *ToolDispatcher) executeBatch(ctx context.Context, calls []types.Content) ([]types.Message, bool) {
	preps := make([]preparedTool, len(calls))
	for i, call := range calls {
		preps[i] = d.prepare(ctx, call, false)
	}
	out := make([]types.Message, len(calls))
	terminated := make([]bool, len(calls))
	run := func(i int) {
		res := d.execute(ctx, preps[i], nestedOrigin{})
		out[i], terminated[i] = res.message, res.terminate
	}
	if d.cfg.Parallel {
		var wg sync.WaitGroup
		for i := range calls {
			wg.Go(func() { run(i) })
		}
		wg.Wait()
	} else {
		for i := range calls {
			run(i)
		}
	}
	return out, len(calls) > 0 && !slices.Contains(terminated, false)
}
