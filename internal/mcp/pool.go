package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

// Pool is a serve-lifetime MCP connection and schema cache.
//
// Not session-scoped: ki is one serve and many sessions, and MCP config is
// global. Session Toggle only decides which cached tools that turn advertises.
type Pool struct {
	home string
	mu   sync.Mutex
	by   map[string]*entry
}

type entry struct {
	name   string
	spec   ServerSpec
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
	ready  bool
	tools  []toolSpec
	conn   transport
}

type diskCache struct {
	Key   string     `json:"key"`
	Tools []toolSpec `json:"tools"`
}

// NewPool builds a pool that persists schemas under {home}/mcp-cache so the
// next serve start can advertise tools without connecting first.
func NewPool(home string) *Pool {
	return &Pool{home: home, by: map[string]*entry{}}
}

// Bind returns loop tools from cached schemas. It does not spawn.
// runPrompt must stay off the initialize/tools/list path or the UI sits on
// "running" with an empty SSE until npx/HTTP handshake finishes.
func (p *Pool) Bind(file File, toggle session.Toggle) []loop.Tool {
	if p == nil {
		return nil
	}
	var out []loop.Tool
	for name, spec := range file.MCPServers {
		if !toggle.Allowed(name) {
			continue
		}
		e := p.entry(name, spec)
		e.mu.Lock()
		tools := append([]toolSpec(nil), e.tools...)
		e.mu.Unlock()
		for _, ts := range tools {
			ts := ts
			out = append(out, lazyTool{pool: p, name: name, spec: spec, ts: ts})
		}
	}
	return out
}

// Prefetch fills empty schema caches. Call it from a goroutine after Bind so
// the current turn can start; the next turn can advertise tools that were
// missing from a cold cache. Disabled names are skipped.
func (p *Pool) Prefetch(ctx context.Context, file File, toggle session.Toggle) {
	if p == nil {
		return
	}
	var wg sync.WaitGroup
	for name, spec := range file.MCPServers {
		if !toggle.Allowed(name) {
			continue
		}
		e := p.entry(name, spec)
		e.mu.Lock()
		need := len(e.tools) == 0
		e.mu.Unlock()
		if !need {
			continue
		}
		name, spec := name, spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.ensure(ctx, name, spec)
		}()
	}
	wg.Wait()
}

// Close kills live clients.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.by {
		e.mu.Lock()
		e.closed = true
		e.ready = false
		if e.conn != nil {
			e.conn.close()
			e.conn = nil
		}
		if e.cond != nil {
			e.cond.Broadcast()
		}
		e.mu.Unlock()
	}
	p.by = map[string]*entry{}
}

func (p *Pool) entry(name string, spec ServerSpec) *entry {
	key := specKey(name, spec)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.by[key]; ok {
		return e
	}
	e := &entry{name: name, spec: spec, tools: p.loadDisk(key, name)}
	e.cond = sync.NewCond(&e.mu)
	p.by[key] = e
	return e
}

func (p *Pool) ensure(ctx context.Context, name string, spec ServerSpec) (transport, error) {
	e := p.entry(name, spec)
	e.mu.Lock()
	for {
		if e.closed {
			e.mu.Unlock()
			return nil, context.Canceled
		}
		if e.ready && e.conn != nil {
			c := e.conn
			e.mu.Unlock()
			return c, nil
		}
		// conn set but !ready: another ensure is in handshake. Wait rather
		// than tools/call before initialize, or starting a second process.
		if e.conn != nil {
			e.cond.Wait()
			continue
		}
		break
	}
	t, err := startTransport(spec)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	e.conn = t
	// Unlock during handshake so Close can kill the child; holding the lock
	// here made Shutdown wait out HandshakeTimeout (npx that never speaks RPC).
	e.mu.Unlock()

	hs, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	tools, err := handshake(hs, t)
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.cond.Broadcast()
	if e.closed {
		t.close()
		if e.conn == t {
			e.conn = nil
		}
		return nil, context.Canceled
	}
	if err != nil {
		t.close()
		if e.conn == t {
			e.conn = nil
		}
		return nil, err
	}
	if e.conn != t {
		t.close()
		return e.conn, nil
	}
	if len(tools) > 0 {
		e.tools = tools
		p.saveDisk(specKey(name, spec), name, tools)
	}
	e.ready = true
	return t, nil
}

func (p *Pool) loadDisk(key, name string) []toolSpec {
	if p.home == "" {
		return nil
	}
	b, err := os.ReadFile(p.cachePath(name))
	if err != nil {
		return nil
	}
	var dc diskCache
	// Ignore a file written for a different command/url (same server name).
	if json.Unmarshal(b, &dc) != nil || dc.Key != key {
		return nil
	}
	return dc.Tools
}

func (p *Pool) saveDisk(key, name string, tools []toolSpec) {
	if p.home == "" {
		return
	}
	dir := filepath.Join(p.home, "mcp-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.Marshal(diskCache{Key: key, Tools: tools})
	if err != nil {
		return
	}
	_ = os.WriteFile(p.cachePath(name), append(b, '\n'), 0o644)
}

func (p *Pool) cachePath(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		b.WriteByte('_')
	}
	return filepath.Join(p.home, "mcp-cache", b.String()+".json")
}

type lazyTool struct {
	pool *Pool
	name string
	spec ServerSpec
	ts   toolSpec
}

func (t lazyTool) Name() string        { return t.ts.Name }
func (t lazyTool) Description() string { return t.ts.Description }
func (t lazyTool) Prompt() string      { return t.ts.Description }
func (t lazyTool) Snippet() string     { return t.name + ": " + t.ts.Description }
func (t lazyTool) Parameters() map[string]any {
	if t.ts.InputSchema != nil {
		return t.ts.InputSchema
	}
	return map[string]any{"type": "object"}
}

func (t lazyTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	conn, err := t.pool.ensure(ctx, t.name, t.spec)
	if err != nil {
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	raw, err := conn.call(ctx, "tools/call", map[string]any{"name": t.ts.Name, "arguments": args})
	if err != nil {
		// Drop a dead connection so the next ensure dials again instead of
		// reusing a half-closed stdio/HTTP session for the rest of the serve.
		t.pool.drop(t.name, t.spec)
		conn, err = t.pool.ensure(ctx, t.name, t.spec)
		if err != nil {
			return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
		}
		raw, err = conn.call(ctx, "tools/call", map[string]any{"name": t.ts.Name, "arguments": args})
		if err != nil {
			return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
		}
	}
	return decodeToolResult(raw)
}

func (p *Pool) drop(name string, spec ServerSpec) {
	e := p.entry(name, spec)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil {
		e.conn.close()
		e.conn = nil
	}
	e.ready = false
	if e.cond != nil {
		e.cond.Broadcast()
	}
}

func decodeToolResult(raw json.RawMessage) loop.ToolResult {
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
