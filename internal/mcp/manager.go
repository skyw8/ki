package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/config"
	"ki/internal/process"
	"ki/internal/tool"
)

var ErrRequired = errors.New("required MCP server unavailable")
var ErrClosed = errors.New("MCP manager closed")

const maxCatalogTools = 4096

type Manager struct {
	ctx             context.Context
	cancel          context.CancelFunc
	transportCtx    context.Context
	transportCancel context.CancelFunc
	mu              sync.Mutex
	closed          bool
	entries         []*serverEntry
	catalog         []tool.Tool
	wg              sync.WaitGroup
	monitorWG       sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
}

type serverEntry struct {
	name        string
	cfg         config.MCPServer
	gate        chan struct{}
	session     *sdk.ClientSession
	sessionDone <-chan struct{}
	catalog     []*adapter
	revision    atomic.Uint64
	loaded      uint64
}

func NewManager(servers map[string]config.MCPServer) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	transportCtx, transportCancel := context.WithCancel(context.Background())
	m := &Manager{ctx: ctx, cancel: cancel, transportCtx: transportCtx, transportCancel: transportCancel}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cfg := servers[name]
		cfg.Args = slices.Clone(cfg.Args)
		cfg.Env = maps.Clone(cfg.Env)
		cfg.EnvVars = slices.Clone(cfg.EnvVars)
		cfg.HTTPHeaders = maps.Clone(cfg.HTTPHeaders)
		cfg.EnabledTools = slices.Clone(cfg.EnabledTools)
		cfg.DisabledTools = slices.Clone(cfg.DisabledTools)
		if cfg.Enabled != nil {
			enabled := *cfg.Enabled
			cfg.Enabled = &enabled
		}
		e := &serverEntry{name: name, cfg: cfg, gate: make(chan struct{}, 1)}
		e.gate <- struct{}{}
		m.entries = append(m.entries, e)
	}
	return m
}

func (m *Manager) begin(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrClosed
	}
	m.wg.Add(1)
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() { stop(); cancel(); m.wg.Done() }, nil
}

// Tools acquires the allowed catalog for a new occupy, never mutating old tools.
func (m *Manager) Tools(ctx context.Context) ([]tool.Tool, error) {
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	type acquired struct {
		tools []*adapter
		err   error
	}
	results := make([]acquired, len(m.entries))
	jobs := make(chan int, len(m.entries))
	for i := range m.entries {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	// Independent servers must not multiply first-occupy startup timeouts.
	// The outer owned operation joins this bounded pool before publishing.
	for range min(8, len(m.entries)) {
		workers.Go(func() {
			for i := range jobs {
				e := m.entries[i]
				if e.cfg.IsEnabled() {
					results[i].tools, results[i].err = m.load(ctx, e)
				}
			}
		})
	}
	workers.Wait()
	var adapters []*adapter
	var failures []error
	for i, e := range m.entries {
		got, err := results[i].tools, results[i].err
		if err != nil {
			err = fmt.Errorf("MCP server %q: %w", e.name, err)
			if e.cfg.Required {
				err = errors.Join(ErrRequired, err)
			}
			failures = append(failures, err)
		} else {
			adapters = append(adapters, got...)
		}
	}
	out := normalizeCatalogNames(adapters)
	m.mu.Lock()
	if !m.closed {
		m.catalog = slices.Clone(out)
	}
	m.mu.Unlock()
	return out, errors.Join(failures...)
}

// Catalog returns only the last acquired catalog and has no connection effects.
func (m *Manager) Catalog() []tool.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.catalog)
}

func (m *Manager) load(ctx context.Context, e *serverEntry) ([]*adapter, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.gate:
	}
	defer func() { e.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.StartupTimeout())
	defer cancel()
	if e.sessionDone != nil {
		select {
		case <-e.sessionDone:
			_ = e.session.Close()
			e.session = nil
			e.sessionDone = nil
			e.catalog = nil
		default:
		}
	}
	if e.session == nil {
		if err := config.ValidateMCPServers(map[string]config.MCPServer{e.name: e.cfg}); err != nil {
			return nil, err
		}
		client := sdk.NewClient(&sdk.Implementation{Name: "ki", Version: "1"}, &sdk.ClientOptions{
			ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { e.revision.Add(1) },
		})
		transport, err := m.transport(e.cfg)
		if err != nil {
			return nil, err
		}
		cs, err := client.Connect(ctx, transport, nil)
		if err != nil {
			return nil, err
		}
		e.session = cs
		done := make(chan struct{})
		e.sessionDone = done
		m.monitorWG.Add(1)
		go func() {
			defer m.monitorWG.Done()
			_ = cs.Wait()
			close(done)
		}()
	}
	revision := e.revision.Load()
	if e.catalog != nil && e.loaded == revision {
		return slices.Clone(e.catalog), nil
	}
	source := ""
	if info := e.session.InitializeResult(); info != nil {
		source = boundedText(info.Instructions, 4096)
	}
	var catalog []*adapter
	totalBytes := 0
	seen := make(map[string]bool)
	for remote, err := range e.session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		if len(seen) >= maxCatalogTools {
			return nil, fmt.Errorf("catalog exceeds %d tools", maxCatalogTools)
		}
		if remote == nil || remote.Name == "" || seen[remote.Name] {
			return nil, errors.New("MCP catalog contains an empty or duplicate tool name")
		}
		seen[remote.Name] = true
		if !e.cfg.AllowsTool(remote.Name) {
			continue
		}
		t, err := newAdapter(m, e.session, e.name, source, e.cfg.ToolTimeout(), remote)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", remote.Name, err)
		}
		totalBytes += t.schemaBytes + len(t.description)
		if totalBytes > 16*1024*1024 {
			return nil, errors.New("MCP catalog exceeds 16MiB")
		}
		catalog = append(catalog, t)
	}
	// An empty successful catalog is cached too. A notification arriving during
	// fetch leaves a newer revision, forcing another refresh at the next occupy.
	e.catalog = catalog
	if e.catalog == nil {
		e.catalog = []*adapter{}
	}
	e.loaded = revision
	return slices.Clone(e.catalog), nil
}

func (m *Manager) transport(cfg config.MCPServer) (sdk.Transport, error) {
	if cfg.Command != "" {
		// Do not bind a persistent MCP subprocess to the startup observation
		// context: its timeout must not kill a successfully connected server.
		cmd := exec.Command(cfg.Command, cfg.Args...)
		process.AttachProcessGroup(cmd)
		process.SetWaitDelay(cmd)
		cmd.Dir = cfg.CWD
		env := make(map[string]string)
		// Do not inherit provider credentials or the whole daemon environment.
		// Explicit env/env_vars opt additional variables into this MCP process.
		keysToInherit := append([]string{
			"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP",
			"SystemRoot", "SYSTEMROOT", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "COMSPEC",
		}, cfg.EnvVars...)
		for _, key := range keysToInherit {
			if value, ok := os.LookupEnv(key); ok {
				env[key] = value
			}
		}
		maps.Copy(env, cfg.Env)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			cmd.Env = append(cmd.Env, k+"="+env[k])
		}
		// MCP stdout is exclusively protocol; stderr never contaminates frames.
		cmd.Stderr = os.Stderr
		return &commandTransport{command: cmd, sdk: &sdk.CommandTransport{Command: cmd, TerminateDuration: 250 * time.Millisecond}}, nil
	}
	headers := http.Header{}
	for k, v := range cfg.HTTPHeaders {
		headers.Set(k, v)
	}
	if cfg.BearerTokenEnv != "" {
		token := os.Getenv(cfg.BearerTokenEnv)
		if token == "" {
			return nil, fmt.Errorf("bearer token environment variable %q is missing", cfg.BearerTokenEnv)
		}
		headers.Set("Authorization", "Bearer "+token)
	}
	return &sdk.StreamableClientTransport{
		Endpoint: cfg.URL,
		HTTPClient: &http.Client{
			Transport: &headerTransport{ctx: m.transportCtx, headers: headers, base: http.DefaultTransport},
			// Credentials are never forwarded to a redirected, unconfigured endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.catalog = nil
		m.cancel()
		m.mu.Unlock()
		// begin/Add is fenced by the same mutex, so no owned operation can
		// appear after this join. SDK sessions are then stable to close.
		m.wg.Wait()
		var failures []error
		for _, e := range m.entries {
			if e.session != nil {
				if err := e.session.Close(); err != nil && !errors.Is(err, context.Canceled) {
					failures = append(failures, err)
				}
			}
		}
		// Keep transports alive while the SDK sends cancellation notifications
		// and its DELETE session cleanup. Canceling HTTP first strands remote
		// calls whose server context is detached from the POST connection.
		m.transportCancel()
		m.monitorWG.Wait()
		m.closeErr = errors.Join(failures...)
	})
	return m.closeErr
}

type commandTransport struct {
	command *exec.Cmd
	sdk     *sdk.CommandTransport
}

func (t *commandTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.sdk.Connect(ctx)
	if err != nil {
		return nil, err
	}
	process.AfterProcessStart(t.command)
	return &commandConnection{Connection: conn, command: t.command}, nil
}

type commandConnection struct {
	sdk.Connection
	command *exec.Cmd
	once    sync.Once
	err     error
}

func (c *commandConnection) Close() error {
	c.once.Do(func() {
		// The official transport owns Wait and protocol shutdown; the host adds
		// a tree watchdog because descendants may keep protocol pipes open.
		done := make(chan error, 1)
		go func() { done <- c.Connection.Close() }()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case c.err = <-done:
		case <-timer.C:
			process.KillProcessGroup(c.command)
			c.err = <-done
		}
		process.KillProcessGroup(c.command)
	})
	return c.err
}

type headerTransport struct {
	ctx     context.Context
	headers http.Header
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	cloned := req.Clone(ctx)
	for key, values := range t.headers {
		cloned.Header[key] = slices.Clone(values)
	}
	resp, err := t.base.RoundTrip(cloned)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	// SSE response bodies outlive RoundTrip. Keep their lifetime cancellation
	// until Close instead of canceling the stream on returning HTTP headers.
	resp.Body = &ownedBody{ReadCloser: resp.Body, done: func() { stop(); cancel() }}
	return resp, nil
}

type ownedBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *ownedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}
