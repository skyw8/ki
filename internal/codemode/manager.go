package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"ki/internal/process"
)

type Config struct {
	Executable string
	// Args replaces the default WorkerArg argv, chiefly for isolated tests.
	Args        []string
	Limits      Limits
	MaxSessions int
}

// Manager owns lazy workers. NewSession does not start a process; the first
// Execute does, and no worker restart silently replays tool side effects.
type Manager struct {
	mu       sync.Mutex
	config   Config
	sessions map[string]*Session
	closed   bool
}

func NewManager(config Config) *Manager {
	config.Limits = config.Limits.normalized()
	if config.MaxSessions <= 0 {
		config.MaxSessions = 32
	}
	return &Manager{config: config, sessions: make(map[string]*Session)}
}

func (m *Manager) NewSession(ctx context.Context, id string) (*Session, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if id == "" {
		return nil, errors.New("code mode session ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("code mode manager is closed")
	}
	if s := m.sessions[id]; s != nil {
		return s, nil
	}
	if len(m.sessions) >= m.config.MaxSessions {
		return nil, errors.New("code mode session capacity reached")
	}
	s := &Session{backend: &remoteSession{ctx: ctx, config: m.config, bindings: make(map[string]*callbackBinding)}}
	m.sessions[id] = s
	return s, nil
}

func (m *Manager) CloseSession(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	var first error
	for _, s := range sessions {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type callbackBinding struct {
	ctx      context.Context
	cancel   context.CancelFunc
	cb       Callbacks
	tools    map[string]bool
	freeform map[string]bool
	seen     map[string]bool
	done     chan struct{}
	cellID   string
	wg       sync.WaitGroup
}

type remoteSession struct {
	ctx             context.Context
	config          Config
	startMu         sync.Mutex
	mu              sync.Mutex
	transport       *peer
	cmd             *exec.Cmd
	processDone     chan struct{}
	bindings        map[string]*callbackBinding
	closed          bool
	stopping        bool
	callbacksClosed bool
	callbackWG      sync.WaitGroup
}

func (s *remoteSession) start(ctx context.Context) (*peer, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("code mode session is closed")
	}
	if p := s.transport; p != nil {
		s.mu.Unlock()
		select {
		case <-p.done:
			return nil, p.err()
		default:
			return p, nil
		}
	}
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, s.ctx.Err()
	}
	executable := s.config.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	args := s.config.Args
	if len(args) == 0 {
		args = []string{WorkerArg}
	}
	cmd := exec.Command(executable, args...)
	process.AttachProcessGroup(cmd)
	process.SetWaitDelay(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	// The worker is a capability-restricted JS host, not a shell. It must not
	// inherit API secrets merely because the daemon needed them for providers.
	cmd.Env = safeWorkerEnv()
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start code mode worker: %w", err)
	}
	process.AfterProcessStart(cmd)
	lifeCtx, cancel := context.WithCancel(s.ctx)
	p := newPeer(lifeCtx, stdout, stdin, s.config.Limits.MaxFrameBytes, s.callback)
	done := make(chan struct{})
	s.mu.Lock()
	s.transport, s.cmd, s.processDone = p, cmd, done
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if err != nil {
			p.fail(fmt.Errorf("code mode worker exited: %w", err))
		} else {
			p.fail(io.EOF)
		}
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		close(done)
	}()
	go func() { <-p.done; process.KillProcessGroup(cmd); _ = stdin.Close(); _ = stdout.Close() }()
	bootCtx, bootCancel := context.WithTimeout(ctx, 5*time.Second)
	defer bootCancel()
	if err = p.call(bootCtx, "init", s.config.Limits, nil); err != nil {
		p.fail(err)
		return nil, err
	}
	return p, nil
}

func safeWorkerEnv() []string {
	var result []string
	// Preserve only OS runtime essentials, not arbitrary provider or extension
	// environment. JS still receives no process/env object.
	allowed := map[string]bool{"systemroot": true, "windir": true, "temp": true, "tmp": true, "tmpdir": true, "path": true, "lang": true, "lc_all": true}
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && allowed[strings.ToLower(name)] {
			result = append(result, entry)
		}
	}
	return result
}

func (s *remoteSession) execute(ctx context.Context, req ExecuteRequest, cb Callbacks) (Response, error) {
	if err := validateRequest(req, s.config.Limits); err != nil {
		return Response{}, err
	}
	p, err := s.start(ctx)
	if err != nil {
		return Response{}, err
	}
	bindingCtx, bindingCancel := context.WithCancel(ctx)
	b := &callbackBinding{ctx: bindingCtx, cancel: bindingCancel, cb: cb, tools: make(map[string]bool), freeform: make(map[string]bool), seen: make(map[string]bool), done: make(chan struct{})}
	for _, def := range req.Tools {
		b.tools[def.Name] = true
		b.freeform[def.Name] = def.Freeform
	}
	s.mu.Lock()
	if s.stopping || s.callbacksClosed || s.closed {
		s.mu.Unlock()
		bindingCancel()
		return Response{}, errors.New("code mode session is closing")
	}
	if s.bindings[req.ParentCallID] != nil {
		s.mu.Unlock()
		bindingCancel()
		return Response{}, errors.New("parent code call ID is already active")
	}
	s.bindings[req.ParentCallID] = b
	s.mu.Unlock()
	var resp Response
	err = p.call(ctx, "execute", req, &resp)
	if err != nil {
		// An interrupted Execute may have admitted a cell before its response;
		// terminate all rather than guess an ID or replay the execution.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.config.Limits.DrainTimeout+time.Second)
		_ = s.terminateAll(cleanupCtx)
		cancel()
		s.removeBinding(req.ParentCallID)
		return Response{}, err
	}
	s.mu.Lock()
	if b.cellID != "" && b.cellID != resp.CellID {
		s.mu.Unlock()
		p.fail(errors.New("worker returned a mismatched cell identity"))
		_ = s.terminateAll(context.Background())
		return Response{}, errors.New("worker returned a mismatched cell identity")
	}
	b.cellID = resp.CellID
	s.mu.Unlock()
	if !resp.Running() {
		s.removeBinding(req.ParentCallID)
	} else {
		go func() {
			select {
			case <-ctx.Done():
				cleanupCtx, cancel := context.WithTimeout(context.Background(), s.config.Limits.DrainTimeout+time.Second)
				defer cancel()
				_, _ = s.wait(cleanupCtx, WaitRequest{CellID: resp.CellID, Terminate: true})
			case <-b.done:
			case <-p.done:
			}
		}()
	}
	return resp, nil
}

func (s *remoteSession) wait(ctx context.Context, req WaitRequest) (Response, error) {
	s.mu.Lock()
	p := s.transport
	s.mu.Unlock()
	if p == nil {
		return Response{}, errors.New("unknown code mode cell")
	}
	var resp Response
	err := p.call(ctx, "wait", req, &resp)
	if err != nil && ctx.Err() != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.config.Limits.DrainTimeout+time.Second)
		defer cancel()
		if cleanupErr := p.call(cleanupCtx, "terminate", WaitRequest{CellID: req.CellID}, nil); cleanupErr != nil {
			p.fail(cleanupErr)
		}
		s.finishCellBinding(req.CellID)
	}
	if err == nil && !resp.Running() {
		s.finishCellBinding(resp.CellID)
	}
	return resp, err
}

func (s *remoteSession) finishCellBinding(cellID string) {
	s.mu.Lock()
	var retired []*callbackBinding
	for parent, b := range s.bindings {
		if b.cellID == cellID {
			retired = append(retired, s.removeBindingLocked(parent))
		}
	}
	s.mu.Unlock()
	for _, b := range retired {
		b.wg.Wait()
	}
}

func (s *remoteSession) callback(ctx context.Context, op string, raw json.RawMessage) (any, error) {
	var parent, cellID string
	var inv Invocation
	var notification Notification
	switch op {
	case "invoke":
		if err := json.Unmarshal(raw, &inv); err != nil {
			return nil, err
		}
		parent, cellID = inv.ParentCallID, inv.CellID
	case "notify":
		if err := json.Unmarshal(raw, &notification); err != nil {
			return nil, err
		}
		parent, cellID = notification.ParentCallID, notification.CellID
	default:
		return nil, errors.New("unknown worker callback")
	}
	s.mu.Lock()
	b := s.bindings[parent]
	if b == nil || s.callbacksClosed {
		s.mu.Unlock()
		return nil, errors.New("worker callback has no active execution capability")
	}
	if b.cellID != "" && b.cellID != cellID {
		s.mu.Unlock()
		return nil, errors.New("worker callback cell mismatch")
	}
	if cellID == "" {
		s.mu.Unlock()
		return nil, errors.New("worker callback has no cell identity")
	}
	if b.cellID == "" {
		b.cellID = cellID
	}
	if op == "invoke" {
		if !b.tools[inv.Name] || b.freeform[inv.Name] != inv.Freeform || !strings.HasPrefix(inv.ToolCallID, cellID+"/tool-") || b.seen[inv.ToolCallID] || len(b.seen) >= s.config.Limits.MaxToolCalls {
			s.mu.Unlock()
			return nil, errors.New("worker callback is outside advertised capability")
		}
		b.seen[inv.ToolCallID] = true
	}
	cb, runCtx := b.cb, b.ctx
	s.callbackWG.Add(1)
	b.wg.Add(1)
	s.mu.Unlock()
	defer s.callbackWG.Done()
	defer b.wg.Done()
	callbackCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(runCtx, cancel)
	defer stop()
	if callbackCtx.Err() != nil || runCtx.Err() != nil {
		return nil, context.Canceled
	}
	if op == "notify" {
		if notification.Text == "" || len(notification.Text) > s.config.Limits.MaxOutputBytes {
			return nil, errors.New("invalid notification")
		}
		if cb.Notify == nil {
			return struct{}{}, nil
		}
		return struct{}{}, safeNotify(callbackCtx, cb.Notify, notification)
	}
	result, err := safeInvoke(callbackCtx, cb.Invoke, inv)
	if err == nil {
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			err = marshalErr
		} else if len(data) > s.config.Limits.MaxResultBytes {
			err = errors.New("nested tool result exceeds intermediate result limit")
		}
	}
	return result, err
}

func (s *remoteSession) removeBinding(parent string) {
	s.mu.Lock()
	b := s.removeBindingLocked(parent)
	s.mu.Unlock()
	if b != nil {
		b.wg.Wait()
	}
}
func (s *remoteSession) removeBindingLocked(parent string) *callbackBinding {
	if b := s.bindings[parent]; b != nil {
		close(b.done)
		delete(s.bindings, parent)
		if b.cancel != nil {
			b.cancel()
		}
		return b
	}
	return nil
}

func (s *remoteSession) terminateAll(ctx context.Context) error {
	s.mu.Lock()
	p := s.transport
	if p != nil {
		s.stopping = true
	}
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	// The worker watchdog is bounded even when owned parent cleanup must join
	// indefinitely. Killing a stuck worker does not cancel that ownership duty.
	workerCtx, cancel := context.WithTimeout(ctx, s.config.Limits.DrainTimeout+time.Second)
	// A callback may return its cancellation error immediately and make the
	// worker commit a failed cell's writes. Wait for the worker's stop ACK before
	// fencing callbacks: FIFO frames alone do not order asynchronous handlers.
	err := p.call(workerCtx, "stopAll", struct{}{}, nil)
	if err != nil {
		p.fail(err)
	}
	s.mu.Lock()
	s.callbacksClosed = true
	for _, b := range s.bindings {
		if b.cancel != nil {
			b.cancel()
		}
	}
	s.mu.Unlock()
	if err == nil {
		err = p.call(workerCtx, "terminateAll", struct{}{}, nil)
	}
	cancel()
	if err != nil {
		p.fail(err)
	}
	p.cancelInbound()
	_ = p.drain(context.Background())
	s.callbackWG.Wait()
	s.mu.Lock()
	for parent := range s.bindings {
		s.removeBindingLocked(parent)
	}
	s.callbacksClosed = s.closed
	s.stopping = false
	s.mu.Unlock()
	return err
}

func (s *remoteSession) close() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	p, done := s.transport, s.processDone
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.config.Limits.DrainTimeout+time.Second)
	defer cancel()
	err := s.terminateAll(ctx)
	p.fail(context.Canceled)
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
		}
	}
	return err
}
