package process

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pty "github.com/aymanbagabas/go-pty"
)

const processBufferBytes = 1 << 20

// Snapshot is a session-owned terminal projection, never an agent identity.
type Snapshot struct {
	Revision       uint64     `json:"revision"`
	RunID          string     `json:"run_id,omitempty"`
	CallID         string     `json:"tool_call_id,omitempty"`
	AgentID        string     `json:"agent_id,omitempty"`
	Generation     uint64     `json:"generation,omitempty"`
	SessionID      int64      `json:"session_id"`
	OwnerSessionID string     `json:"owner_session_id,omitempty"`
	Command        string     `json:"cmd,omitempty"`
	CWD            string     `json:"workdir,omitempty"`
	TTY            bool       `json:"tty"`
	Status         string     `json:"status"`
	PID            int        `json:"pid,omitempty"`
	Output         string     `json:"output,omitempty"`
	OutputFile     string     `json:"output_file,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	Error          string     `json:"error,omitempty"`
	Truncated      bool       `json:"truncated,omitempty"`
	OutputOffset   int64      `json:"output_offset"`
	TotalBytes     int64      `json:"total_bytes"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type Update struct {
	Delta   string   `json:"delta,omitempty"`
	Process Snapshot `json:"process"`
}

// Manager retains processes independently of tool/turn contexts.
type Manager struct {
	mu        sync.Mutex
	processes map[int64]*shellProcess
	closed    bool
	spool     OutputSpool
	owner     string
	limit     int
	listener  func(Update)
}

type shellProcess struct {
	interaction   chan struct{}
	mu            sync.Mutex
	snapshot      Snapshot
	buffer        []byte
	dropped       int64
	total         int64
	cursor        int64
	sanitizer     outputSanitizer
	file          *os.File
	outputErr     error
	cmd           *exec.Cmd
	terminal      pty.Pty
	done          chan struct{}
	changed       chan struct{}
	terminate     sync.Once
	publish       func(Update)
	temporary     bool
	lastPublished time.Time
	pending       []byte
}

func NewManager() *Manager { return NewSpooledManager(nil, "") }

func NewSpooledManager(spool OutputSpool, owner string) *Manager {
	return &Manager{processes: make(map[int64]*shellProcess), spool: spool, owner: owner, limit: 64}
}

func (m *Manager) SetListener(listener func(Update)) {
	m.mu.Lock()
	m.listener = listener
	m.mu.Unlock()
}

func processID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(b[:]) & ((1 << 53) - 1))
	if id == 0 {
		id = 1
	}
	return id, nil
}

func (m *Manager) Start(ctx context.Context, shell Shell, cwd, command string, tty bool, identity Identity) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !shell.available() {
		return 0, errInterpreterUnavailable
	}
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return 0, err
	}
	if m.closed {
		m.mu.Unlock()
		return 0, errTaskStoreClosed
	}
	if len(m.processes) >= 256 {
		for id, p := range m.processes {
			p.mu.Lock()
			reclaim := p.snapshot.Status == "exited" && p.cursor >= p.total
			p.mu.Unlock()
			if reclaim {
				delete(m.processes, id)
				if p.temporary {
					_ = os.Remove(p.snapshot.OutputFile)
				}
			}
		}
		if len(m.processes) >= 256 {
			m.mu.Unlock()
			return 0, fmt.Errorf("process retention capacity reached; drain completed output with write_stdin")
		}
	}
	active := 0
	for _, p := range m.processes {
		p.mu.Lock()
		if p.snapshot.Status == "running" {
			active++
		}
		p.mu.Unlock()
	}
	if active >= m.limit {
		m.mu.Unlock()
		return 0, fmt.Errorf("live process capacity %d reached", m.limit)
	}
	id, err := processID()
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	for m.processes[id] != nil {
		id, err = processID()
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
	}
	var f *os.File
	if m.spool != nil {
		f, err = m.spool.CreateOutputFile(m.owner, "exec_command")
	} else {
		f, err = os.CreateTemp("", "ki-process-*.log")
	}
	temporary := m.spool == nil
	if err != nil && m.spool != nil {
		// A session spool failure must not prevent execution; the manager owns and cleans the fallback.
		f, err = os.CreateTemp("", "ki-process-*.log")
		temporary = true
	}
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	p := &shellProcess{temporary: temporary, interaction: make(chan struct{}, 1), file: f, done: make(chan struct{}), changed: make(chan struct{}), snapshot: Snapshot{Revision: 1, RunID: identity.RunID, CallID: identity.CallID, AgentID: identity.AgentID, Generation: identity.Generation, SessionID: id, OwnerSessionID: m.owner, Command: command, CWD: cwd, TTY: tty, Status: "running", OutputFile: f.Name(), StartedAt: time.Now()}, publish: m.listener}
	m.processes[id] = p
	// Register before yielding: a canceled observer must never become the last process owner.
	err = p.start(shell, cwd, command, tty)
	if err != nil {
		delete(m.processes, id)
		_ = f.Close()
		_ = os.Remove(f.Name())
		m.mu.Unlock()
		return 0, err
	}
	m.mu.Unlock()
	p.mu.Lock()
	snapshot := p.snapshot
	p.mu.Unlock()
	if p.publish != nil {
		p.publish(Update{Process: snapshot})
	}
	return id, nil
}

func (p *shellProcess) start(shell Shell, cwd, command string, tty bool) error {
	if tty {
		terminal, err := pty.New()
		if err != nil {
			return err
		}
		native := terminal
		terminal, err = prepareTerminal(native)
		if err != nil {
			_ = native.Close()
			return err
		}
		p.terminal = terminal
		shell.interactive = true
		c := terminal.Command(shell.path, shell.args(command)...)
		c.Dir = cwd
		c.Env = shell.env()
		if err = c.Start(); err != nil {
			_ = terminal.Close()
			return err
		}
		// The PTY starts its own session on Unix; the existing tree helper can own
		// its process group and the Windows Job Object using this process handle.
		p.cmd = &exec.Cmd{Process: c.Process}
		afterStart(p.cmd)
		p.mu.Lock()
		p.snapshot.PID = c.Process.Pid
		p.mu.Unlock()
		copied := make(chan struct{})
		go func() { _, _ = io.Copy(p, terminal); close(copied) }()
		go func() {
			err := c.Wait()
			if u, ok := terminal.(pty.UnixPty); ok {
				_ = u.Slave().Close()
			}
			select {
			case <-copied:
			case <-time.After(shellWaitDelay):
				_ = terminal.Close()
				<-copied
			}
			_ = terminal.Close()
			code := -1
			if c.ProcessState != nil {
				code = c.ProcessState.ExitCode()
			}
			p.finish(code, err)
		}()
	} else {
		c := exec.Command(shell.path, shell.args(command)...)
		c.Dir = cwd
		c.Env = shell.env()
		c.Stdout = p
		c.Stderr = p
		detachCmd(c)
		c.WaitDelay = shellWaitDelay
		p.cmd = c
		if err := c.Start(); err != nil {
			return err
		}
		afterStart(c)
		p.mu.Lock()
		p.snapshot.PID = c.Process.Pid
		p.mu.Unlock()
		go func() {
			err := c.Wait()
			code := -1
			if c.ProcessState != nil {
				code = c.ProcessState.ExitCode()
			}
			p.finish(code, err)
		}()
	}
	return nil
}

func (p *shellProcess) Write(data []byte) (int, error) {
	p.mu.Lock()
	n, err := p.file.Write(data)
	if err != nil && p.outputErr == nil {
		// A PTY copier's return value is not the command's exit status. Retain
		// spool failures so a successful exit cannot hide incomplete output.
		p.outputErr = err
	}
	update := p.appendOutputLocked(p.sanitizer.Filter(data[:n]))
	p.signalLocked()
	p.mu.Unlock()
	if update != nil && p.publish != nil {
		p.publish(*update)
	}
	return n, err
}

func (p *shellProcess) appendOutputLocked(clean []byte) *Update {
	p.snapshot.Revision++
	p.total += int64(len(clean))
	p.buffer = append(p.buffer, clean...)
	if len(p.buffer) > processBufferBytes {
		drop := len(p.buffer) - processBufferBytes
		for drop < len(p.buffer) && !utf8.RuneStart(p.buffer[drop]) {
			drop++
		}
		p.buffer = bytes.Clone(p.buffer[drop:])
		p.dropped += int64(drop)
	}
	p.snapshot.TotalBytes = p.total
	tail := p.buffer
	if len(tail) > 16384 {
		tail = tail[len(tail)-16384:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	p.snapshot.Output = cleanUnicodeControls(string(tail))
	p.pending = append(p.pending, clean...)
	if len(p.pending) > 8192 {
		p.pending = bytes.Clone(p.pending[len(p.pending)-8192:])
	}
	var update *Update
	if time.Since(p.lastPublished) >= 100*time.Millisecond {
		delta := p.pending
		for len(delta) > 0 && !utf8.RuneStart(delta[0]) {
			delta = delta[1:]
		}
		value := Update{Delta: cleanUnicodeControls(string(delta)), Process: p.snapshot}
		update = &value
		p.pending = nil
		p.lastPublished = time.Now()
	}
	return update
}

func (p *shellProcess) signalLocked() { close(p.changed); p.changed = make(chan struct{}) }

func (p *shellProcess) finish(code int, err error) {
	p.mu.Lock()
	now := time.Now()
	_ = p.appendOutputLocked(p.sanitizer.Flush())
	p.snapshot.Revision++
	p.snapshot.Status = "exited"
	p.snapshot.ExitCode = &code
	p.snapshot.FinishedAt = &now
	outputErr := errors.Join(p.outputErr, p.file.Sync(), p.file.Close())
	if failure := errors.Join(err, outputErr); failure != nil {
		p.snapshot.Error = failure.Error()
	}
	p.signalLocked()
	snapshot := p.snapshot
	delta := cleanUnicodeControls(string(p.pending))
	p.pending = nil
	p.mu.Unlock()
	releaseCmd(p.cmd)
	close(p.done)
	if p.publish != nil {
		p.publish(Update{Delta: delta, Process: snapshot})
	}
}

func (m *Manager) get(id int64) (*shellProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.processes[id]
	if p == nil {
		return nil, fmt.Errorf("unknown terminal session_id %d for this session", id)
	}
	return p, nil
}

// Interact serializes both stdin and incremental output consumption per process.
func (m *Manager) Interact(ctx context.Context, id int64, chars string, yield time.Duration, budget int, emit func(Update)) (Snapshot, error) {
	p, err := m.get(id)
	if err != nil {
		return Snapshot{}, err
	}
	select {
	case p.interaction <- struct{}{}:
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
	releaseInteraction := true
	defer func() {
		if releaseInteraction {
			<-p.interaction
		}
	}()
	if chars != "" {
		p.mu.Lock()
		live := p.snapshot.Status == "running"
		terminal := p.terminal
		p.mu.Unlock()
		if !live {
			return p.observe(budget), fmt.Errorf("terminal has already exited")
		}
		if terminal != nil {
			written := make(chan error, 1)
			go func() { _, writeErr := io.WriteString(terminal, chars); written <- writeErr }()
			select {
			case err = <-written:
				if err != nil {
					return p.observe(budget), err
				}
			case <-ctx.Done():
				// A terminal may block when its process does not read stdin. The
				// observer can leave while the manager retains ordered input ownership.
				releaseInteraction = false
				go func() { <-written; <-p.interaction }()
				return p.observe(budget), ctx.Err()
			}
		} else if chars == "\x03" {
			if err = interruptProcess(p.cmd); err != nil {
				return p.observe(budget), err
			}
		} else {
			return p.observe(budget), fmt.Errorf("stdin requires tty=true")
		}
	}
	timer := time.NewTimer(yield)
	defer timer.Stop()
	for {
		p.mu.Lock()
		done := p.snapshot.Status == "exited"
		changed := p.changed
		p.mu.Unlock()
		if done {
			return p.observe(budget), nil
		}
		select {
		case <-ctx.Done():
			return p.observe(budget), ctx.Err()
		case <-timer.C:
			return p.observe(budget), nil
		case <-changed:
			if emit != nil {
				p.mu.Lock()
				snap := p.snapshot
				p.mu.Unlock()
				emit(Update{Process: snap})
			}
		}
	}
}

func (p *shellProcess) observe(budget int) Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.snapshot
	start := p.cursor
	if start < p.dropped {
		start = p.dropped
		s.Truncated = true
	}
	index := int(start - p.dropped)
	if index > len(p.buffer) {
		index = len(p.buffer)
	}
	data := p.buffer[index:]
	if budget <= 0 {
		budget = 10000
	}
	if budget > 10000 {
		budget = 10000
	}
	max := budget * 4
	if len(data) > max {
		data = data[:max]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		s.Truncated = true
	}
	s.Output = cleanUnicodeControls(string(data))
	s.OutputOffset = start
	p.cursor = start + int64(len(data))
	return s
}

func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.processes))
	for _, p := range m.processes {
		p.mu.Lock()
		out = append(out, p.snapshot)
		p.mu.Unlock()
	}
	return out
}

func (m *Manager) Terminate(id int64) (Snapshot, error) {
	p, err := m.get(id)
	if err != nil {
		return Snapshot{}, err
	}
	p.terminate.Do(func() { killCmd(p.cmd) })
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		return p.observe(10000), fmt.Errorf("process termination pending")
	}
	return p.observe(10000), nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	ps := make([]*shellProcess, 0, len(m.processes))
	for _, p := range m.processes {
		ps = append(ps, p)
	}
	m.mu.Unlock()
	for _, p := range ps {
		p.mu.Lock()
		live := p.snapshot.Status == "running"
		p.mu.Unlock()
		if live {
			p.terminate.Do(func() { killCmd(p.cmd) })
		}
	}
	for _, p := range ps {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
		if p.temporary {
			_ = os.Remove(p.snapshot.OutputFile)
		}
	}
}

func ResolveShell(runtime ShellRuntime, path string, login bool) (Shell, error) {
	var shell Shell
	if path == "" {
		if runtime.powerShell != nil && runtime.powerShell.available() {
			shell = *runtime.powerShell
		} else {
			shell = runtime.bash
		}
	} else {
		name := strings.TrimSuffix(strings.ToLower(path), ".exe")
		// Discovery can find Git Bash outside PATH or through a configured host
		// path. Selecting the known shell by name must use that same executable.
		if name == "bash" && runtime.bash.available() {
			shell = runtime.bash
		} else if (name == "pwsh" || name == "powershell") && runtime.powerShell != nil && runtime.powerShell.available() &&
			strings.TrimSuffix(strings.ToLower(filepath.Base(runtime.powerShell.path)), ".exe") == name {
			shell = *runtime.powerShell
		}
		if shell.available() {
			shell.login = &login
			return shell, nil
		}
		absolute, err := exec.LookPath(path)
		if err != nil {
			return Shell{}, err
		}
		base := strings.TrimSuffix(strings.ToLower(filepath.Base(absolute)), ".exe")
		switch base {
		case "bash", "sh", "zsh":
			shell = Shell{kind: shellBash, path: absolute, setShellEnv: runtime.bash.setShellEnv}
		case "pwsh":
			shell = Shell{kind: shellPowerShell, path: absolute, powerShellEdition: powerShellCore}
		case "powershell":
			shell = Shell{kind: shellPowerShell, path: absolute, powerShellEdition: powerShellDesktop}
		default:
			return Shell{}, fmt.Errorf("unsupported shell %q", path)
		}
	}
	shell.login = &login
	if !shell.available() {
		return Shell{}, errInterpreterUnavailable
	}
	return shell, nil
}

// TerminateAll stops current process trees while keeping the manager reusable.
func (m *Manager) TerminateAll() {
	for _, snapshot := range m.Snapshots() {
		if snapshot.Status == "running" {
			_, _ = m.Terminate(snapshot.SessionID)
		}
	}
}
