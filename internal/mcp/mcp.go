package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

// ServerSpec is one MCP server entry (MCP config spec).
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// File is the on-disk .mcp.json document.
type File struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// Load merges global then project (project wins on same name).
func Load(home, cwd string) File {
	out := File{MCPServers: map[string]ServerSpec{}}
	merge := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var f File
		if json.Unmarshal(b, &f) != nil || f.MCPServers == nil {
			return
		}
		for k, v := range f.MCPServers {
			out.MCPServers[k] = v
		}
	}
	if home != "" {
		merge(filepath.Join(home, ".mcp.json"))
	}
	if cwd != "" {
		merge(filepath.Join(cwd, ".ki", ".mcp.json"))
	}
	return out
}

// Tools starts enabled servers and returns loop tools. Failures are skipped.
func Tools(ctx context.Context, file File, toggle session.Toggle) []loop.Tool {
	var out []loop.Tool
	for name, spec := range file.MCPServers {
		if !toggle.Allowed(name) {
			continue
		}
		c, err := start(ctx, spec)
		if err != nil {
			continue
		}
		listed, err := c.listTools(ctx)
		if err != nil {
			c.close()
			continue
		}
		for _, lt := range listed {
			lt := lt
			out = append(out, mcpTool{client: c, server: name, spec: lt})
		}
	}
	return out
}

type client struct {
	cmd    *exec.Cmd
	stdin  *json.Encoder
	stdout *bufio.Scanner
	mu     sync.Mutex
	id     atomic.Int64
}

func start(ctx context.Context, spec ServerSpec) (*client, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	if len(spec.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
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
	c := &client{cmd: cmd, stdin: json.NewEncoder(stdin), stdout: bufio.NewScanner(stdout)}
	c.stdout.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ki", "version": "0.1"},
	}); err != nil {
		c.close()
		return nil, err
	}
	_ = c.notify("notifications/initialized", map[string]any{})
	return c, nil
}

func (c *client) close() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func (c *client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
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
			return nil, fmt.Errorf("%s", msg.Error.Message)
		}
		return msg.Result, nil
	}
	if err := c.stdout.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("mcp eof")
}

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *client) listTools(ctx context.Context) ([]toolSpec, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
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

type mcpTool struct {
	client *client
	server string
	spec   toolSpec
}

func (t mcpTool) Name() string        { return t.spec.Name }
func (t mcpTool) Description() string { return t.spec.Description }
func (t mcpTool) Prompt() string      { return t.spec.Description }
func (t mcpTool) Snippet() string     { return t.server + ": " + t.spec.Description }
func (t mcpTool) Parameters() map[string]any {
	if t.spec.InputSchema != nil {
		return t.spec.InputSchema
	}
	return map[string]any{"type": "object"}
}

func (t mcpTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	raw, err := t.client.call(ctx, "tools/call", map[string]any{"name": t.spec.Name, "arguments": args})
	if err != nil {
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(raw, &res)
	var content []types.Content
	for _, c := range res.Content {
		content = append(content, types.Content{Type: "text", Text: c.Text})
	}
	if len(content) == 0 {
		content = []types.Content{{Type: "text", Text: string(raw)}}
	}
	return loop.ToolResult{Content: content, IsError: res.IsError}
}

// FilterNames is used by tests to inspect merge without spawning.
func FilterNames(f File, toggle session.Toggle) []string {
	var n []string
	for name := range f.MCPServers {
		if toggle.Allowed(name) {
			n = append(n, name)
		}
	}
	return n
}
