package extension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ki/internal/loop"
	"ki/internal/tools"
	"ki/internal/types"
)

const (
	timeoutInit          = 10 * time.Second
	timeoutHook          = 2 * time.Second
	timeoutTool          = 120 * time.Second
	timeoutCommand       = 15 * time.Second
	timeoutProviderStart = 10 * time.Second
)

var errRPC = errors.New("extension rpc")

type sessionContextKey struct{}

func withSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionContextKey{}, sessionID)
}

func sessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(sessionContextKey{}).(string); ok {
		return id
	}
	return ""
}

func withSessionParam(sessionID string, params any) any {
	if sessionID == "" {
		return params
	}
	var out map[string]any
	if raw, err := json.Marshal(params); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	out["sessionId"] = sessionID
	return out
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcClient struct {
	name               string
	failClosed         bool
	fallback           bool
	cmd                *exec.Cmd
	enc                *json.Encoder
	mu                 sync.Mutex
	pending            map[string]chan rpcMsg
	idSeq              atomic.Int64
	closed             chan struct{}
	closedOnce         sync.Once
	closeOnce          sync.Once
	progress           map[string]func(any)
	progressMu         sync.Mutex
	providerStreams    map[string]*providerStreamPipe
	providerStreamMu   sync.Mutex
	providerAuthMu     sync.RWMutex
	providerAuthFn     func(ProviderAuthEvent)
	providerAuthEvents chan ProviderAuthEvent
	registration       Registration
	capabilities       []string
	undeclared         []string
	busChannels        map[string]bool
	busMu              sync.Mutex
	host               SessionHost
}

func startRPC(ctx context.Context, d Descriptor, sessionID, home, cwd string, host SessionHost) (*rpcClient, error) {
	env := sidecarEnv(d, sessionID, home, cwd)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := runInstall(ctx, d.root, d.manifest.Runtime.Install, env); err != nil {
		return nil, err
	}
	// Sidecar outlives prepare; lifetime is owned by KillProcessGroup/close.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), resolveRuntimeCommand(d.root, d.manifest.Runtime.Command), d.manifest.Runtime.Args...) //nolint:gosec // command/args come from the extension manifest
	cmd.Dir = d.root
	cmd.Env = env
	tools.AttachProcessGroup(cmd)
	tools.SetWaitDelay(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sidecar: %w", err)
	}
	c := &rpcClient{
		name:               d.Name,
		failClosed:         d.FailClosed,
		cmd:                cmd,
		enc:                json.NewEncoder(stdin),
		pending:            map[string]chan rpcMsg{},
		closed:             make(chan struct{}),
		progress:           map[string]func(any){},
		providerStreams:    map[string]*providerStreamPipe{},
		providerAuthEvents: make(chan ProviderAuthEvent, 32),
		busChannels:        map[string]bool{},
		host:               host,
	}
	go c.providerAuthLoop()
	go c.readLoop(stdout)
	initCtx, cancel := context.WithTimeout(ctx, timeoutInit)
	defer cancel()
	var reg Registration
	scope := map[string]any{"global": sessionID == ""}
	if host == nil {
		scope["provider"] = true
	}
	if err := c.call(initCtx, "initialize", map[string]any{
		"sessionId": sessionID, "cwd": cwd, "home": home, "extensionRoot": d.root,
		"capabilities": d.Capabilities, "scope": scope,
		"providers": d.Providers,
	}, &reg); err != nil {
		c.close()
		return nil, err
	}
	c.capabilities = d.Capabilities
	c.undeclared = applyRegistrationGates(d.Capabilities, &reg)
	if err := gateSubscriptions(d.Capabilities, &reg); err != nil {
		c.close()
		return nil, err
	}
	reg.indexSubscriptions()
	c.registration = reg
	c.fallback = reg.Fallback
	return c, nil
}

func sidecarEnv(d Descriptor, sessionID, home, cwd string) []string {
	env := []string{
		"KI_EXTENSION=" + d.Name,
		"KI_HOME=" + home,
		"KI_EXTENSION_ROOT=" + d.root,
	}
	// Global sidecars serve many sessions. Do not pin them to the first
	// session/cwd; each session-scoped RPC carries its own sessionId instead.
	if sessionID != "" {
		env = append(env, "KI_SESSION_ID="+sessionID)
	}
	if cwd != "" {
		env = append(env, "KI_CWD="+cwd)
	}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR", "PATHEXT", "COMSPEC"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	for k, v := range d.manifest.Runtime.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// resolveRuntimeCommand uses PATH for a name with no slash and the extension
// PATH (node, npx). A relative path is joined to the package root (bin/extension).
func resolveRuntimeCommand(root, command string) string {
	if command == "" || filepath.IsAbs(command) {
		return command
	}
	if !strings.Contains(filepath.ToSlash(command), "/") {
		return command
	}
	return filepath.Join(root, filepath.FromSlash(command))
}

func runInstall(ctx context.Context, root string, argv []string, env []string) error {
	if len(argv) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, resolveRuntimeCommand(root, argv[0]), argv[1:]...) //nolint:gosec // install argv comes from the extension manifest
	cmd.Dir = root
	cmd.Env = env
	// stdout would corrupt the sidecar NDJSON stream if it shared the pipe;
	// install runs first and both streams go to the ki process stderr.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("runtime.install: %w", err)
	}
	return nil
}

func gateSubscriptions(caps []string, reg *Registration) error {
	if reg == nil {
		return nil
	}
	var kept []Subscription
	for _, sub := range reg.Subscriptions {
		if err := AcceptSubscription(sub); err != nil {
			continue
		}
		kept = append(kept, sub)
	}
	reg.Subscriptions = kept
	if hasKind(caps, CapLifecycle) && len(kept) == 0 {
		return errNoSubscriptions
	}
	if !hasKind(caps, CapLifecycle) {
		reg.Subscriptions = nil
		reg.syncEvents = map[string]bool{}
		reg.asyncEvents = map[string]bool{}
	}
	return nil
}

// applyRegistrationGates drops initialize tools/commands whose capability
// was not declared (KD 11). Host still emits extension_error for each drop.
func applyRegistrationGates(caps []string, reg *Registration) []string {
	if reg == nil {
		return nil
	}
	var dropped []string
	if !hasKind(caps, CapTool) && len(reg.Tools) > 0 {
		dropped = append(dropped, string(CapTool))
		reg.Tools = nil
	}
	if !hasKind(caps, CapCommand) && len(reg.Commands) > 0 {
		dropped = append(dropped, string(CapCommand))
		reg.Commands = nil
	}
	if !hasKind(caps, CapProvider) && len(reg.Providers) > 0 {
		dropped = append(dropped, string(CapProvider))
		reg.Providers = nil
	}
	return dropped
}

func (c *rpcClient) readLoop(r io.Reader) {
	defer c.markClosed()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "tool.progress" {
			var p struct {
				ID         string `json:"id"`
				ToolCallID string `json:"toolCallId"`
				Partial    any    `json:"partial"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			c.progressMu.Lock()
			fn := c.progress[p.ID]
			c.progressMu.Unlock()
			if fn != nil {
				fn(p.Partial)
			}
			continue
		}
		if msg.Method == "provider.stream.event" {
			var event ProviderStreamEvent
			if err := json.Unmarshal(msg.Params, &event); err != nil || event.RequestID == "" {
				continue
			}
			c.providerStreamMu.Lock()
			ch := c.providerStreams[event.RequestID]
			c.providerStreamMu.Unlock()
			if ch != nil {
				// A bounded channel makes a slow loop consumer apply backpressure
				// to the sidecar without retaining an unbounded token queue.
				select {
				case ch.events <- event:
				case <-ch.done:
				case <-c.closed:
				}
			}
			continue
		}
		if msg.Method == "provider.auth.event" {
			var event ProviderAuthEvent
			if err := json.Unmarshal(msg.Params, &event); err != nil || event.RequestID == "" {
				continue
			}
			// Queue auth events instead of spawning one goroutine per event. OAuth
			// progress is ordered (URL/device code must precede completed), while
			// the worker still keeps filesystem/UI work off the RPC reader.
			select {
			case c.providerAuthEvents <- event:
			case <-c.closed:
			}
			continue
		}
		id, _ := stringifyID(msg.ID)
		if msg.Method != "" && id != "" {
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch == nil {
				go c.handleInbound(msg)
				continue
			}
			select {
			case ch <- msg:
			default:
			}
			continue
		}
		if id == "" {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
	// Why: gopls flags Scan loops without Err(); a truncated NDJSON line is the
	// only interesting failure mode once the peer closes stdout.
	if err := sc.Err(); err != nil {
		slog.Debug("extension rpc read", "extension", c.name, "err", err)
	}
}

func (c *rpcClient) providerAuthLoop() {
	for {
		select {
		case <-c.closed:
			return
		case event := <-c.providerAuthEvents:
			c.providerAuthMu.RLock()
			fn := c.providerAuthFn
			c.providerAuthMu.RUnlock()
			if fn != nil {
				fn(event)
			}
		}
	}
}

func (c *rpcClient) setProviderAuthHandler(fn func(ProviderAuthEvent)) {
	c.providerAuthMu.Lock()
	c.providerAuthFn = fn
	c.providerAuthMu.Unlock()
}

func stringifyID(id any) (string, bool) {
	switch v := id.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatInt(int64(v), 10), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

func (c *rpcClient) call(ctx context.Context, method string, params any, result any) error {
	id := strconv.FormatInt(c.idSeq.Add(1), 10)
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	err = c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err != nil {
		return err
	}
	var msg rpcMsg
	select {
	case <-ctx.Done():
		c.notify("cancel", withSessionParam(sessionIDFromContext(ctx), map[string]any{"id": id}))
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("%w: sidecar closed", errRPC)
	case msg = <-ch:
	}
	if msg.Error != nil {
		return fmt.Errorf("%w: %s", errRPC, msg.Error.Message)
	}
	if result != nil && len(msg.Result) > 0 {
		return json.Unmarshal(msg.Result, result)
	}
	return nil
}

func (c *rpcClient) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.encodeLocked(rpcMsg{JSONRPC: "2.0", Method: method, Params: raw})
	c.mu.Unlock()
}

func (c *rpcClient) close() {
	c.closeOnce.Do(func() {
		// A provider stream may have filled its bounded event queue while its
		// consumer is being torn down. Signal readers before waiting for the
		// process so they cannot wait forever for readLoop to reach EOF.
		c.markClosed()
		if c.cmd != nil {
			tools.KillProcessGroup(c.cmd)
			_ = c.cmd.Wait()
		}
	})
}

func (c *rpcClient) markClosed() {
	c.closedOnce.Do(func() { close(c.closed) })
}

func (c *rpcClient) hasSync(event string) bool {
	return c.registration.hasSync(event)
}

func (c *rpcClient) hasAsync(event string) bool {
	return c.registration.hasAsync(event)
}

func compactCtx(ctx context.Context, model string) CompactCtx {
	aborted := ctx != nil && ctx.Err() != nil
	return CompactCtx{Idle: false, Model: model, Aborted: aborted}
}

func (c *rpcClient) BeforeRun(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	if !c.hasSync(EventBeforeAgentStart) {
		return system, msgs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		System   string          `json:"system"`
		Messages []types.Message `json:"messages"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventBeforeAgentStart, "payload": map[string]any{"system": system, "messages": redactMessages(msgs)},
		"ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return system, msgs, err
	}
	if out.System != "" {
		system = out.System
	}
	if out.Messages != nil {
		msgs = out.Messages
	}
	return system, msgs, nil
}

func (c *rpcClient) TransformContext(ctx context.Context, msgs []types.Message) ([]types.Message, error) {
	if !c.hasSync(EventContext) {
		return msgs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Messages []types.Message `json:"messages"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventContext, "payload": map[string]any{"messages": redactMessages(msgs)},
		"ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return msgs, err
	}
	if out.Messages != nil {
		return out.Messages, nil
	}
	return msgs, nil
}

func (c *rpcClient) BeforeTool(ctx context.Context, in ToolCall) (ToolCall, *Block, error) {
	if !c.hasSync(EventToolCall) {
		return in, nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Args      map[string]any `json:"args"`
		Block     bool           `json:"block"`
		Reason    string         `json:"reason"`
		Terminate bool           `json:"terminate"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventToolCall, "payload": in, "ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return in, nil, err
	}
	if out.Block {
		return in, &Block{Reason: out.Reason, Terminate: out.Terminate}, nil
	}
	if out.Args != nil {
		in.Args = out.Args
	}
	return in, nil, nil
}

func (c *rpcClient) AfterTool(ctx context.Context, in ToolCall, res ResultPatch) (ResultPatch, error) {
	if !c.hasSync(EventToolResult) {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out ResultPatch
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventToolResult, "payload": map[string]any{"call": in, "result": res},
		"ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return res, err
	}
	return out, nil
}

func (c *rpcClient) BeforeProvider(ctx context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	if !c.hasSync(EventBeforeProviderRequest) {
		return req, nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Request      *ProviderRequest `json:"request"`
		ShortCircuit *ShortCircuit    `json:"shortCircuit"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventBeforeProviderRequest, "payload": req, "ctx": compactCtx(ctx, req.Model),
	}, &out)
	if err != nil {
		return req, nil, err
	}
	if out.ShortCircuit != nil && out.ShortCircuit.Text != "" {
		return req, out.ShortCircuit, nil
	}
	if out.Request != nil {
		if out.Request.Provider != "" {
			req.Provider = out.Request.Provider
		}
		if out.Request.Model != "" {
			req.Model = out.Request.Model
		}
		if out.Request.Tools != nil {
			req.Tools = out.Request.Tools
		}
		if out.Request.MaxTokens != 0 {
			req.MaxTokens = out.Request.MaxTokens
		}
		if out.Request.ThinkingEffort != "" {
			req.ThinkingEffort = out.Request.ThinkingEffort
		}
		if out.Request.Messages != nil {
			req.Messages = out.Request.Messages
		}
	}
	return req, nil, nil
}

func (c *rpcClient) BeforeProviderHTTP(ctx context.Context, view HTTPRequestView) (HTTPRequestPatch, error) {
	if !c.hasSync(EventBeforeProviderHeaders) {
		return HTTPRequestPatch{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out HTTPRequestPatch
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventBeforeProviderHeaders, "payload": view, "ctx": compactCtx(ctx, ""),
	}, &out)
	return out, err
}

func (c *rpcClient) AfterProviderHTTP(ctx context.Context, status int, headers map[string]string) error {
	if !c.hasAsync(EventAfterProviderResponse) {
		return nil
	}
	c.notify("lifecycle.event", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventAfterProviderResponse,
		"payload":   map[string]any{"status": status, "headers": headers},
	})
	return nil
}

func (c *rpcClient) AfterProviderError(ctx context.Context, errClass string) (Fallback, error) {
	if !c.hasSync(EventProviderError) || !c.fallback {
		return Fallback{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out Fallback
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventProviderError, "payload": map[string]any{"errClass": errClass},
		"ctx": compactCtx(ctx, ""),
	}, &out)
	return out, err
}

func (c *rpcClient) OnEvent(ctx context.Context, ev Event) error {
	if !c.hasAsync(ev.Type) {
		return nil
	}
	c.notify("lifecycle.event", map[string]any{
		"sessionId": sessionIDFromContext(ctx), "event": ev.Type, "payload": ev,
	})
	return nil
}

func (c *rpcClient) executeTool(ctx context.Context, spec ToolSpec, toolCallID, name string, args map[string]any, emit func(any)) loop.ToolResult {
	timeout := timeoutTool
	if spec.TimeoutMs > 0 {
		timeout = time.Duration(spec.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id := strconv.FormatInt(c.idSeq.Add(1), 10)
	if emit != nil {
		c.progressMu.Lock()
		c.progress[id] = emit
		c.progressMu.Unlock()
		defer func() {
			c.progressMu.Lock()
			delete(c.progress, id)
			c.progressMu.Unlock()
		}()
	}
	raw, err := json.Marshal(map[string]any{
		"sessionId": sessionIDFromContext(ctx), "toolCallId": toolCallID, "name": name, "args": args,
	})
	if err != nil {
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	if err := c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Method: "tool.execute", Params: raw}); err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		slog.Debug("extension tool.execute encode", "extension", c.name, "err", err)
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	c.mu.Unlock()
	var msg rpcMsg
	select {
	case <-ctx.Done():
		c.notify("cancel", withSessionParam(sessionIDFromContext(ctx), map[string]any{"id": id}))
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: ctx.Err().Error()}}, IsError: true}
	case <-c.closed:
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: "sidecar closed"}}, IsError: true}
	case msg = <-ch:
	}
	if msg.Error != nil {
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: msg.Error.Message}}, IsError: true}
	}
	var out struct {
		Content []types.Content `json:"content"`
		IsError bool            `json:"isError"`
		Details any             `json:"details"`
	}
	if err := json.Unmarshal(msg.Result, &out); err != nil {
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	return loop.ToolResult{Content: out.Content, IsError: out.IsError, Details: out.Details}
}

func (c *rpcClient) rewriteInput(ctx context.Context, text string) (string, bool, error) {
	if !c.hasSync(EventInput) {
		return text, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Text    string `json:"text"`
		Swallow bool   `json:"swallow"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventInput, "payload": map[string]any{"text": text}, "ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return text, false, err
	}
	if out.Swallow {
		return "", true, nil
	}
	if out.Text != "" {
		return out.Text, false, nil
	}
	return text, false, nil
}

func (c *rpcClient) rewriteMessageEnd(ctx context.Context, msg types.Message) (types.Message, error) {
	if !c.hasSync(EventMessageEnd) {
		return msg, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Message *types.Message `json:"message"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventMessageEnd, "payload": map[string]any{"message": msg}, "ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return msg, err
	}
	if out.Message != nil {
		return *out.Message, nil
	}
	return msg, nil
}

func (c *rpcClient) beforeCompact(ctx context.Context) (bool, string, error) {
	if !c.hasSync(EventSessionBeforeCompact) {
		return true, "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Cancel  bool   `json:"cancel"`
		Summary string `json:"summary"`
	}
	err := c.call(ctx, "lifecycle.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx),
		"event":     EventSessionBeforeCompact, "payload": map[string]any{}, "ctx": compactCtx(ctx, ""),
	}, &out)
	if err != nil {
		return true, "", err
	}
	if out.Cancel {
		return false, "", nil
	}
	return true, out.Summary, nil
}

func (c *rpcClient) invokeCommand(ctx context.Context, name, args string) (handled bool, notice, prompt string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutCommand)
	defer cancel()
	var out struct {
		Handled bool   `json:"handled"`
		Notice  string `json:"notice"`
		Prompt  string `json:"prompt"`
	}
	err = c.call(ctx, "command.invoke", map[string]any{
		"sessionId": sessionIDFromContext(ctx), "name": name, "args": args,
	}, &out)
	return out.Handled, out.Notice, out.Prompt, err
}
