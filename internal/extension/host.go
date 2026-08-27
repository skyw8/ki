package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ki/internal/types"
)

// SessionHost is implemented by the HTTP server. Sidecar inbound RPCs call it.
// Methods must return quickly; enqueue and appendMessage must not wait for a
// full model run.
type SessionHost interface {
	CreateSession(req SessionCreateRequest) (SessionCreateResult, error)
	NewSession(sessionID string, cwd string) (SessionCreateResult, error)
	ReloadSession(sessionID string) error
	ListSessions(filter map[string]any) ([]SessionSnapshot, error)
	GetSession(sessionID string) (SessionSnapshot, error)
	Enqueue(sessionID, extension string, req EnqueueRequest) (EnqueueResult, error)
	AppendMessage(sessionID, extension string, req AppendMessageRequest) (AppendMessageResult, error)
	Snapshot(sessionID, extension string) (SessionSnapshot, error)
	AppendEntry(sessionID, extension, customType string, data any) error
	Abort(sessionID string) error
	Compact(sessionID string) error
	PatchSession(sessionID string, model, thinking string) error
	SetActiveTools(sessionID, extension string, names []string) error
	RegisterTools(sessionID, extension string, tools []ToolSpec) error
	UISetStatus(sessionID, extension, key, text, tone string) error
	UISetPanel(sessionID, extension string, panel UIPanel) error
	UIClearPanel(sessionID, extension string) error
	GlobalUISetStatus(extension, key, text, tone string) error
	GlobalUISetPanel(extension string, panel UIPanel) error
	GlobalUIClearPanel(extension string) error
	UIConfirm(sessionID, extension, title, message string) (bool, error)
	UISelect(sessionID, extension, title string, options []string) (string, error)
	BusEmit(sessionID, from, channel string, data any) (any, error)
	BusBroadcast(sessionID, from, channel string, data any) error
}

// EnqueueRequest is session.enqueue params.
type EnqueueRequest struct {
	Content        []types.Content   `json:"content"`
	DeliverAs      string            `json:"deliverAs"`
	When           string            `json:"when"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Kind           string            `json:"kind"`
	CustomType     string            `json:"customType"`
	Display        *bool             `json:"display"`
	External       map[string]string `json:"external,omitempty"`
	// ContextSequence is assigned by the Host for prompt ordering and is not
	// supplied by sidecars.
	ContextSequence uint64 `json:"-"`
	ContextBoundary bool   `json:"-"`
}

// SessionCreateRequest is the channel-safe session creation contract.
type SessionCreateRequest struct {
	WorkspaceID    string         `json:"workspaceId,omitempty"`
	CWD            string         `json:"cwd,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	ThinkingEffort string         `json:"thinkingEffort,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// SessionCreateResult is returned to a channel after create/new/cwd.
type SessionCreateResult struct {
	SessionID   string         `json:"sessionId"`
	CWD         string         `json:"cwd"`
	WorkspaceID string         `json:"workspaceId,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// EnqueueResult is the accept acknowledgement.
type EnqueueResult struct {
	Accepted string `json:"accepted"`
	QueueID  string `json:"queueId,omitempty"`
}

// AppendMessageRequest appends a normal user message without starting a run.
type AppendMessageRequest struct {
	Message        types.Message `json:"message"`
	IdempotencyKey string        `json:"idempotencyKey,omitempty"`
}

// AppendMessageResult reports whether a message was committed or waits for a
// currently running prompt to release the session transcript.
type AppendMessageResult struct {
	Accepted string `json:"accepted"`
	EntryID  string `json:"entryId,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
}

// SessionSnapshot is session.snapshot.
type SessionSnapshot struct {
	ID          string         `json:"id,omitempty"`
	CWD         string         `json:"cwd,omitempty"`
	WorkspaceID string         `json:"workspaceId,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Idle        bool           `json:"idle"`
	Running     bool           `json:"running"`
	Queued      int            `json:"queued"`
	ExtQueued   int            `json:"extQueued"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	Thinking    string         `json:"thinking,omitempty"`
	ActiveTools []string       `json:"activeTools,omitempty"`
	AllTools    []string       `json:"allTools,omitempty"`
	Commands    []string       `json:"commands,omitempty"`
}

// UIPanel is the generic detail model for any extension (not goal-specific).
// WebUI renders title/summary, then sections (items/kv/markdown/text), fields,
// then actions. Extensions decide which actions/fields apply; the shell does not.
type UIPanel struct {
	Title       string           `json:"title,omitempty"`
	Summary     string           `json:"summary,omitempty"`
	Sections    []map[string]any `json:"sections,omitempty"`
	Actions     []UIAction       `json:"actions,omitempty"`
	Fields      []UIField        `json:"fields,omitempty"`
	SubmitLabel string           `json:"submitLabel,omitempty"`
}

// UIAction is one panel button. Disabled is shown but not clickable.
type UIAction struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Style    string `json:"style,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Title    string `json:"title,omitempty"`
}

// UIField is one editable field.
type UIField struct {
	ID      string   `json:"id"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type,omitempty"`
	Value   any      `json:"value,omitempty"`
	Options []string `json:"options,omitempty"`
}

// UIPrompt is a pending confirm/select dialog.
type UIPrompt struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title,omitempty"`
	Message string   `json:"message,omitempty"`
	Options []string `json:"options,omitempty"`
}

// ExtensionUI is the WebUI projection for one extension. Session responses
// contain session-scoped values; the extension catalog may contain a global
// value emitted by a process-level sidecar.
type ExtensionUI struct {
	Extension string    `json:"extension"`
	Status    *UIStatus `json:"status,omitempty"`
	Panel     *UIPanel  `json:"panel,omitempty"`
	Prompt    *UIPrompt `json:"prompt,omitempty"`
}

// UIStatus is a top-bar chip.
type UIStatus struct {
	Key  string `json:"key"`
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

func (c *rpcClient) replyError(id any, message string) {
	c.mu.Lock()
	_ = c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32000, Message: message}})
	c.mu.Unlock()
}

func (c *rpcClient) replyResult(id any, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		c.replyError(id, err.Error())
		return
	}
	c.mu.Lock()
	_ = c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Result: raw})
	c.mu.Unlock()
}

func inboundSessionID(params json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", err
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("sessionId required for global extension")
	}
	return p.SessionID, nil
}

func inboundNeedsSession(method string) bool {
	switch method {
	case "session.enqueue", "session.appendMessage", "session.snapshot", "session.appendEntry", "session.abort",
		"session.compact", "session.patch", "session.setActiveTools", "session.new",
		"session.reload", "tools.register",
		"ui.setStatus", "ui.setPanel", "ui.clearPanel", "ui.confirm", "ui.select",
		"bus.emit", "bus.broadcast", "bus.subscribe", "bus.unsubscribe":
		return true
	default:
		return false
	}
}

func (c *rpcClient) handleInbound(msg rpcMsg) {
	if c.host == nil {
		c.replyError(msg.ID, "host methods unavailable")
		return
	}
	sessionID := ""
	if inboundNeedsSession(msg.Method) {
		var err error
		sessionID, err = inboundSessionID(msg.Params)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
	}
	switch msg.Method {
	case "session.create":
		var req SessionCreateRequest
		if err := json.Unmarshal(msg.Params, &req); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		res, err := c.host.CreateSession(req)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.new":
		var p struct {
			CWD string `json:"cwd"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.host.NewSession(sessionID, p.CWD)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.reload":
		if err := c.host.ReloadSession(sessionID); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.list":
		var p struct {
			Filter   map[string]any `json:"filter"`
			Metadata map[string]any `json:"metadata"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		filter := p.Filter
		if filter == nil {
			filter = p.Metadata
		}
		res, err := c.host.ListSessions(filter)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"sessions": res})
	case "session.get":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.SessionID == "" {
			c.replyError(msg.ID, "sessionId required")
			return
		}
		res, err := c.host.GetSession(p.SessionID)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.enqueue":
		var req EnqueueRequest
		_ = json.Unmarshal(msg.Params, &req)
		res, err := c.host.Enqueue(sessionID, c.name, req)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.appendMessage":
		var req AppendMessageRequest
		if err := json.Unmarshal(msg.Params, &req); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		res, err := c.host.AppendMessage(sessionID, c.name, req)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.snapshot":
		res, err := c.host.Snapshot(sessionID, c.name)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.appendEntry":
		var p struct {
			CustomType string `json:"customType"`
			Data       any    `json:"data"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.AppendEntry(sessionID, c.name, p.CustomType, p.Data); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.abort":
		if err := c.host.Abort(sessionID); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.compact":
		if err := c.host.Compact(sessionID); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.patch":
		var p struct {
			Model    string `json:"model"`
			Thinking string `json:"thinkingEffort"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.PatchSession(sessionID, p.Model, p.Thinking); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.setActiveTools":
		var p struct {
			Names []string `json:"names"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.SetActiveTools(sessionID, c.name, p.Names); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "tools.register":
		if !hasKind(c.capabilities, CapTool) {
			c.replyError(msg.ID, "capability tool required")
			return
		}
		var p struct {
			Tools []ToolSpec `json:"tools"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.RegisterTools(sessionID, c.name, p.Tools); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.setStatus":
		var p struct {
			Key  string `json:"key"`
			Text string `json:"text"`
			Tone string `json:"tone"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.UISetStatus(sessionID, c.name, p.Key, p.Text, p.Tone); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.setPanel":
		var panel UIPanel
		_ = json.Unmarshal(msg.Params, &panel)
		if err := c.host.UISetPanel(sessionID, c.name, panel); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.clearPanel":
		if err := c.host.UIClearPanel(sessionID, c.name); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.setGlobalStatus":
		var p struct {
			Key  string `json:"key"`
			Text string `json:"text"`
			Tone string `json:"tone"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.GlobalUISetStatus(c.name, p.Key, p.Text, p.Tone); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.setGlobalPanel":
		var panel UIPanel
		_ = json.Unmarshal(msg.Params, &panel)
		if err := c.host.GlobalUISetPanel(c.name, panel); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.clearGlobalPanel":
		if err := c.host.GlobalUIClearPanel(c.name); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.confirm":
		var p struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		ok, err := c.host.UIConfirm(sessionID, c.name, p.Title, p.Message)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": ok})
	case "ui.select":
		var p struct {
			Title   string   `json:"title"`
			Options []string `json:"options"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		choice, err := c.host.UISelect(sessionID, c.name, p.Title, p.Options)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"value": choice})
	case "bus.emit":
		if !hasKind(c.capabilities, CapBus) {
			c.replyError(msg.ID, "capability bus required")
			return
		}
		var p struct {
			Channel string `json:"channel"`
			Data    any    `json:"data"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		data, err := c.host.BusEmit(sessionID, c.name, p.Channel, p.Data)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"data": data})
	case "bus.broadcast":
		if !hasKind(c.capabilities, CapBus) {
			c.replyError(msg.ID, "capability bus required")
			return
		}
		var p struct {
			Channel string `json:"channel"`
			Data    any    `json:"data"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.BusBroadcast(sessionID, c.name, p.Channel, p.Data); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "bus.subscribe":
		var p struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		c.busMu.Lock()
		if c.busChannels == nil {
			c.busChannels = map[string]bool{}
		}
		c.busChannels[busKey(sessionID, p.Channel)] = true
		c.busMu.Unlock()
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "bus.unsubscribe":
		var p struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		c.busMu.Lock()
		delete(c.busChannels, busKey(sessionID, p.Channel))
		c.busMu.Unlock()
		c.replyResult(msg.ID, map[string]any{"ok": true})
	default:
		c.replyError(msg.ID, fmt.Sprintf("unknown method %s", msg.Method))
	}
}

func busKey(sessionID, channel string) string { return sessionID + "\x00" + channel }

func (c *rpcClient) subscribedBus(sessionID, channel string) bool {
	if !hasKind(c.capabilities, CapBus) {
		return false
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	prefix := sessionID + "\x00"
	hasSession := false
	for key := range c.busChannels {
		if strings.HasPrefix(key, prefix) {
			hasSession = true
			break
		}
	}
	if !hasSession {
		return true
	}
	return c.busChannels[busKey(sessionID, channel)]
}

func (c *rpcClient) deliverBus(sessionID, channel string, data any, wait bool) any {
	if !c.subscribedBus(sessionID, channel) {
		return data
	}
	if !wait {
		c.notify("bus.event", map[string]any{"sessionId": sessionID, "channel": channel, "data": data})
		return data
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutHook)
	defer cancel()
	var out struct {
		Data any `json:"data"`
	}
	err := c.call(ctx, "bus.event", map[string]any{"sessionId": sessionID, "channel": channel, "data": data}, &out)
	if err != nil || out.Data == nil {
		return data
	}
	return out.Data
}
