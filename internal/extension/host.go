package extension

import (
	"context"
	"encoding/json"
	"fmt"

	"ki/internal/types"
)

// SessionHost is implemented by the HTTP server. Sidecar inbound RPCs call it.
// Methods must return quickly; enqueue must not wait for a full run.
type SessionHost interface {
	Enqueue(sessionID, extension string, req EnqueueRequest) (EnqueueResult, error)
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
	UIConfirm(sessionID, extension, title, message string) (bool, error)
	UISelect(sessionID, extension, title string, options []string) (string, error)
	BusEmit(sessionID, from, channel string, data any) (any, error)
	BusBroadcast(sessionID, from, channel string, data any) error
}

// EnqueueRequest is session.enqueue params.
type EnqueueRequest struct {
	Content        []types.Content `json:"content"`
	DeliverAs      string          `json:"deliverAs"`
	When           string          `json:"when"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Kind           string          `json:"kind"`
	CustomType     string          `json:"customType"`
	Display        *bool           `json:"display"`
}

// EnqueueResult is the accept acknowledgement.
type EnqueueResult struct {
	Accepted string `json:"accepted"`
	QueueID  string `json:"queueId,omitempty"`
}

// SessionSnapshot is session.snapshot.
type SessionSnapshot struct {
	Idle        bool     `json:"idle"`
	Running     bool     `json:"running"`
	Queued      int      `json:"queued"`
	ExtQueued   int      `json:"extQueued"`
	Model       string   `json:"model,omitempty"`
	Thinking    string   `json:"thinking,omitempty"`
	ActiveTools []string `json:"activeTools,omitempty"`
	AllTools    []string `json:"allTools,omitempty"`
	Commands    []string `json:"commands,omitempty"`
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

// ExtensionUI is the session GET projection for one extension.
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

func (c *rpcClient) handleInbound(msg rpcMsg) {
	if c.host == nil {
		c.replyError(msg.ID, "host methods unavailable")
		return
	}
	ctx := context.Background()
	switch msg.Method {
	case "session.enqueue":
		var req EnqueueRequest
		_ = json.Unmarshal(msg.Params, &req)
		res, err := c.host.Enqueue(c.sessionID, c.name, req)
		if err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, res)
	case "session.snapshot":
		res, err := c.host.Snapshot(c.sessionID, c.name)
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
		if err := c.host.AppendEntry(c.sessionID, c.name, p.CustomType, p.Data); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.abort":
		if err := c.host.Abort(c.sessionID); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.compact":
		if err := c.host.Compact(c.sessionID); err != nil {
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
		if err := c.host.PatchSession(c.sessionID, p.Model, p.Thinking); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "session.setActiveTools":
		var p struct {
			Names []string `json:"names"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if err := c.host.SetActiveTools(c.sessionID, c.name, p.Names); err != nil {
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
		if err := c.host.RegisterTools(c.sessionID, c.name, p.Tools); err != nil {
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
		if err := c.host.UISetStatus(c.sessionID, c.name, p.Key, p.Text, p.Tone); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.setPanel":
		var panel UIPanel
		_ = json.Unmarshal(msg.Params, &panel)
		if err := c.host.UISetPanel(c.sessionID, c.name, panel); err != nil {
			c.replyError(msg.ID, err.Error())
			return
		}
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "ui.clearPanel":
		if err := c.host.UIClearPanel(c.sessionID, c.name); err != nil {
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
		ok, err := c.host.UIConfirm(c.sessionID, c.name, p.Title, p.Message)
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
		choice, err := c.host.UISelect(c.sessionID, c.name, p.Title, p.Options)
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
		data, err := c.host.BusEmit(c.sessionID, c.name, p.Channel, p.Data)
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
		if err := c.host.BusBroadcast(c.sessionID, c.name, p.Channel, p.Data); err != nil {
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
		c.busChannels[p.Channel] = true
		c.busMu.Unlock()
		c.replyResult(msg.ID, map[string]any{"ok": true})
	case "bus.unsubscribe":
		var p struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		c.busMu.Lock()
		delete(c.busChannels, p.Channel)
		c.busMu.Unlock()
		c.replyResult(msg.ID, map[string]any{"ok": true})
	default:
		c.replyError(msg.ID, fmt.Sprintf("unknown method %s", msg.Method))
	}
	_ = ctx
}

func (c *rpcClient) subscribedBus(channel string) bool {
	if !hasKind(c.capabilities, CapBus) {
		return false
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	if len(c.busChannels) == 0 {
		return true
	}
	return c.busChannels[channel]
}

func (c *rpcClient) deliverBus(channel string, data any, wait bool) any {
	if !c.subscribedBus(channel) {
		return data
	}
	if !wait {
		c.notify("bus.event", map[string]any{"channel": channel, "data": data})
		return data
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutHook)
	defer cancel()
	var out struct {
		Data any `json:"data"`
	}
	err := c.call(ctx, "bus.event", map[string]any{"channel": channel, "data": data}, &out)
	if err != nil || out.Data == nil {
		return data
	}
	return out.Data
}
