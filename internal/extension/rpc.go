package extension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ki/internal/loop"
	"ki/internal/tools"
	"ki/internal/types"
)

const (
	timeoutInit    = 10 * time.Second
	timeoutHook    = 2 * time.Second
	timeoutTool    = 120 * time.Second
	timeoutCommand = 15 * time.Second
)

var errRPC = errors.New("extension rpc")

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
	name         string
	sessionID    string
	failClosed   bool
	points       []string
	hasHook      bool
	fallback     bool
	cmd          *exec.Cmd
	enc          *json.Encoder
	mu           sync.Mutex
	pending      map[string]chan rpcMsg
	idSeq        atomic.Int64
	closed       chan struct{}
	progress     map[string]func(any)
	progressMu   sync.Mutex
	registration Registration
	capabilities []string
	undeclared   []string
}

func startRPC(ctx context.Context, d Descriptor, sessionID, home, cwd string) (*rpcClient, error) {
	cmdPath := d.manifest.Runtime.Command
	if !filepath.IsAbs(cmdPath) {
		cmdPath = filepath.Join(d.root, cmdPath)
	}
	cmd := exec.Command(cmdPath, d.manifest.Runtime.Args...)
	cmd.Dir = d.root
	env := []string{
		"KI_EXTENSION=" + d.Name,
		"KI_SESSION_ID=" + sessionID,
		"KI_CWD=" + cwd,
		"KI_HOME=" + home,
		"KI_EXTENSION_ROOT=" + d.root,
	}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR", "PATHEXT", "COMSPEC"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	for k, v := range d.manifest.Runtime.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	tools.AttachProcessGroup(cmd)
	tools.SetWaitDelay(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &rpcClient{
		name:       d.Name,
		sessionID:  sessionID,
		failClosed: d.FailClosed,
		points:     d.Intercept,
		hasHook:    hasKind(d.Capabilities, CapHook),
		cmd:        cmd,
		enc:        json.NewEncoder(stdin),
		pending:    map[string]chan rpcMsg{},
		closed:     make(chan struct{}),
		progress:   map[string]func(any){},
	}
	go c.readLoop(stdout)
	initCtx, cancel := context.WithTimeout(context.Background(), timeoutInit)
	defer cancel()
	var reg Registration
	if err := c.call(initCtx, "initialize", map[string]any{
		"sessionId": sessionID, "cwd": cwd, "home": home, "extensionRoot": d.root, "capabilities": d.Capabilities,
	}, &reg); err != nil {
		c.close()
		return nil, err
	}
	c.capabilities = d.Capabilities
	c.undeclared = applyRegistrationGates(d.Capabilities, &reg)
	c.registration = reg
	c.fallback = reg.Fallback
	return c, nil
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
	return dropped
}

func (c *rpcClient) readLoop(r io.Reader) {
	defer close(c.closed)
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
		id, _ := stringifyID(msg.ID)
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
	c.mu.Unlock()
	c.mu.Lock()
	err = c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
	c.mu.Unlock()
	if err != nil {
		return err
	}
	var msg rpcMsg
	select {
	case <-ctx.Done():
		c.notify("cancel", map[string]any{"id": id})
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("%w: sidecar closed", errRPC)
	case msg = <-ch:
	}
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
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
	_ = c.enc.Encode(rpcMsg{JSONRPC: "2.0", Method: method, Params: raw})
	c.mu.Unlock()
}

func (c *rpcClient) close() {
	if c.cmd != nil {
		tools.KillProcessGroup(c.cmd)
		_ = c.cmd.Wait()
	}
}

func (c *rpcClient) BeforeRun(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	if !hasPoint(c.points, InterceptProvider) {
		return system, msgs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		System   string          `json:"system"`
		Messages []types.Message `json:"messages"`
	}
	err := c.call(ctx, "intercept.provider.beforeRun", map[string]any{"system": system, "messages": redactMessages(msgs)}, &out)
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
	if !hasPoint(c.points, InterceptProvider) {
		return msgs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Messages []types.Message `json:"messages"`
	}
	err := c.call(ctx, "intercept.provider.transformContext", map[string]any{"messages": redactMessages(msgs)}, &out)
	if err != nil {
		return msgs, err
	}
	if out.Messages != nil {
		return out.Messages, nil
	}
	return msgs, nil
}

func (c *rpcClient) BeforeTool(ctx context.Context, in ToolCall) (ToolCall, *Block, error) {
	if !hasPoint(c.points, InterceptTool) {
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
	err := c.call(ctx, "intercept.tool.before", in, &out)
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
	if !hasPoint(c.points, InterceptTool) {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out ResultPatch
	err := c.call(ctx, "intercept.tool.after", map[string]any{"call": in, "result": res}, &out)
	if err != nil {
		return res, err
	}
	return out, nil
}

func (c *rpcClient) BeforeProvider(ctx context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	if !hasPoint(c.points, InterceptProvider) {
		return req, nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out struct {
		Request      *ProviderRequest `json:"request"`
		ShortCircuit *ShortCircuit    `json:"shortCircuit"`
	}
	err := c.call(ctx, "intercept.provider.request", req, &out)
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
	if !hasPoint(c.points, InterceptProviderHTTP) {
		return HTTPRequestPatch{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out HTTPRequestPatch
	err := c.call(ctx, "intercept.provider.http", view, &out)
	return out, err
}

func (c *rpcClient) AfterProviderHTTP(ctx context.Context, status int, headers map[string]string) error {
	if !hasPoint(c.points, InterceptProviderHTTP) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	return c.call(ctx, "intercept.provider.http.after", map[string]any{"status": status, "headers": headers}, nil)
}

func (c *rpcClient) AfterProviderError(ctx context.Context, errClass string) (Fallback, error) {
	if !hasPoint(c.points, InterceptProvider) || !c.fallback {
		return Fallback{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutHook)
	defer cancel()
	var out Fallback
	err := c.call(ctx, "intercept.provider.error", map[string]any{"errClass": errClass}, &out)
	return out, err
}

func (c *rpcClient) OnEvent(ctx context.Context, ev Event) error {
	if !c.hasHook {
		return nil
	}
	c.notify("event", ev)
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
	raw, _ := json.Marshal(map[string]any{"toolCallId": toolCallID, "name": name, "args": args})
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	_ = c.enc.Encode(rpcMsg{JSONRPC: "2.0", ID: id, Method: "tool.execute", Params: raw})
	c.mu.Unlock()
	var msg rpcMsg
	select {
	case <-ctx.Done():
		c.notify("cancel", map[string]any{"id": id})
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

func (c *rpcClient) invokeCommand(ctx context.Context, name, args string) (handled bool, notice, prompt string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutCommand)
	defer cancel()
	var out struct {
		Handled bool   `json:"handled"`
		Notice  string `json:"notice"`
		Prompt  string `json:"prompt"`
	}
	err = c.call(ctx, "command.invoke", map[string]any{"name": name, "args": args}, &out)
	return out.Handled, out.Notice, out.Prompt, err
}
