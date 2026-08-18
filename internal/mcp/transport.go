package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errEmptyCommand = errors.New("empty command")
	errMCPEOF       = errors.New("MCP EOF")
	errEmptyURL     = errors.New("empty URL")
	errMCPResponse  = errors.New("MCP response error")
	errMCPSSEE      = errors.New("MCP SSE EOF")
)

const (
	// HandshakeTimeout bounds initialize + tools/list. Without a cap a wedged
	// stdio Scan (e.g. npx that never speaks RPC) holds Prefetch until killed.
	HandshakeTimeout = 20 * time.Second
	callTimeout      = 60 * time.Second
)

type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(method string, params any) error
	close()
}

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func startTransport(ctx context.Context, spec ServerSpec) (transport, error) {
	if url := httpURL(spec); url != "" {
		return newHTTP(url, spec.Headers)
	}
	return newStdio(ctx, spec)
}

func handshake(ctx context.Context, t transport) ([]toolSpec, error) {
	if _, err := t.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ki", "version": "0.1"},
	}); err != nil {
		return nil, err
	}
	_ = t.notify("notifications/initialized", map[string]any{})
	raw, err := t.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

type stdioClient struct {
	cmd    *exec.Cmd
	stdin  *json.Encoder
	stdout *bufio.Scanner
	mu     sync.Mutex
	id     atomic.Int64
}

func newStdio(ctx context.Context, spec ServerSpec) (*stdioClient, error) {
	if spec.Command == "" {
		return nil, errEmptyCommand
	}
	// Not CommandContext(prompt): the client outlives one turn in the serve pool.
	// Prompt cancel must not SIGKILL a shared npx/node child.
	// MCP commands are explicitly supplied by the user in .mcp.json.
	//nolint:gosec // executing configured MCP commands is the feature contract
	cmd := exec.CommandContext(context.WithoutCancel(ctx), spec.Command, spec.Args...)
	detachCmd(cmd)
	if len(spec.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open MCP stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP command: %w", err)
	}
	c := &stdioClient{cmd: cmd, stdin: json.NewEncoder(stdin), stdout: bufio.NewScanner(stdout)}
	c.stdout.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return c, nil
}

func (c *stdioClient) close() {
	killCmd(c.cmd)
}

func (c *stdioClient) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call races Scan against ctx because bufio.Scanner cannot be canceled.
// On timeout we kill the child so Scan unblocks instead of leaking a wedged npx.
func (c *stdioClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	type result struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := c.doCall(method, params)
		ch <- result{raw, err}
	}()
	select {
	case <-ctx.Done():
		c.close()
		return nil, ctx.Err()
	case r := <-ch:
		return r.raw, r.err
	}
}

func (c *stdioClient) doCall(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.id.Add(1)
	if err := c.stdin.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for c.stdout.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(c.stdout.Bytes(), &msg) != nil {
			continue
		}
		if len(msg.ID) == 0 {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%w: %s", errMCPResponse, msg.Error.Message)
		}
		return msg.Result, nil
	}
	if err := c.stdout.Err(); err != nil {
		return nil, err
	}
	return nil, errMCPEOF
}

type httpClient struct {
	url     string
	headers map[string]string
	session string
	hc      *http.Client
	mu      sync.Mutex
	id      atomic.Int64
}

func newHTTP(url string, headers map[string]string) (*httpClient, error) {
	if url == "" {
		return nil, errEmptyURL
	}
	return &httpClient{
		url:     url,
		headers: headers,
		hc:      &http.Client{Timeout: callTimeout},
	}, nil
}

func (c *httpClient) close() {}

func (c *httpClient) notify(method string, params any) error {
	_, err := c.call(context.Background(), method, params)
	return err
}

func (c *httpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var id any
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !strings.HasPrefix(method, "notifications/") {
		id = c.id.Add(1)
		body["id"] = id
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Streamable HTTP may reply JSON or SSE; some servers require this Accept.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		c.session = sid
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("%w: HTTP %d: %s", errMCPResponse, res.StatusCode, bytes.TrimSpace(b))
	}
	if id == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, nil
	}
	return decodeRPC(res.Header.Get("Content-Type"), res.Body)
}

func decodeRPC(contentType string, r io.Reader) (json.RawMessage, error) {
	// Streamable HTTP servers may wrap the same JSON-RPC result in SSE.
	if strings.Contains(contentType, "text/event-stream") {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimSpace(after))
				continue
			}
			if line != "" || len(data) == 0 {
				continue
			}
			raw, err := parseRPCResult([]byte(strings.Join(data, "\n")))
			if err != nil {
				return nil, err
			}
			if raw != nil {
				return raw, nil
			}
			data = data[:0]
		}
		if len(data) > 0 {
			return parseRPCResult([]byte(strings.Join(data, "\n")))
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, errMCPSSEE
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return parseRPCResult(b)
}

func parseRPCResult(b []byte) (json.RawMessage, error) {
	var msg struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("%w: %s", errMCPResponse, msg.Error.Message)
	}
	return msg.Result, nil
}
