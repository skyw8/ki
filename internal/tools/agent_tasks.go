package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentRun executes one run of a logical agent. The logical agent ID remains
// stable across resume; background is true for detached and resumed runs.
type AgentRun func(context.Context, string, string, bool) (AgentCompletion, error)

// MaxAgentDepth is the maximum number of Agent-created child layers below the
// main session. The main session is depth 0, so depth 3 is the deepest child
// that may run, but it cannot create another Agent child.
const MaxAgentDepth = 3

// AgentRequest describes one child agent launch. The parent session and entry
// identify the exact conversation point that is forked into the child.
type AgentRequest struct {
	Description     string
	Prompt          string
	SubagentType    string
	Model           string
	RunInBackground bool
	ParentSessionID string
	ParentEntryID   string
	// SessionID is assigned by server after it creates the tree child and is
	// used to stop the task if that session is deleted immediately.
	SessionID string
	// MetadataPath is next to the child transcript and makes the agent index
	// recoverable after the serve process exits.
	MetadataPath string
	// OutputFile is the child transcript path used in completion notifications.
	OutputFile string
}

// AgentLaunch is returned as soon as a child task has been accepted.
type AgentLaunch struct {
	TaskID      string
	SessionID   string
	Description string
	Prompt      string
	OutputFile  string
}

// AgentCompletion is the compact final result kept in the task registry.
type AgentCompletion struct {
	Result       string
	ToolUseCount int
	TotalTokens  int
}

// AgentMessageRequest is the provider-neutral message sent to one logical
// agent. The first implementation routes by stable agent ID; team names can
// be added later without changing the child run protocol.
type AgentMessageRequest struct {
	Target          string
	Summary         string
	Message         string
	SenderSessionID string
}

// AgentMessageResult describes whether a message steered a live run, waited
// behind a run boundary, or resumed a terminal run.
type AgentMessageResult struct {
	AgentID string
	Status  string
	Message string
}

// AgentMessenger is intentionally separate from AgentRuntime so lightweight
// test runtimes can keep the original spawn/task contract.
type AgentMessenger interface {
	SendAgentMessage(context.Context, AgentMessageRequest) (AgentMessageResult, error)
}

// AgentMetadata is the durable logical-agent record. Process handles such as
// cancel functions and goroutines are deliberately absent; a running record
// is marked interrupted when a new server rebuilds the index.
type AgentMetadata struct {
	Version         int        `json:"version"`
	TaskID          string     `json:"task_id"`
	SessionID       string     `json:"session_id"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	Description     string     `json:"description,omitempty"`
	Prompt          string     `json:"prompt,omitempty"`
	SubagentType    string     `json:"subagent_type,omitempty"`
	Model           string     `json:"model,omitempty"`
	OutputFile      string     `json:"output_file,omitempty"`
	Status          TaskStatus `json:"status"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	ToolUseCount    int        `json:"tool_use_count,omitempty"`
	TotalTokens     int        `json:"total_tokens,omitempty"`
	StartedAt       time.Time  `json:"started_at,omitzero"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Pending         []string   `json:"pending,omitempty"`
	RunCount        uint64     `json:"run_count"`
	NotifiedRun     uint64     `json:"notified_run,omitempty"`
}

var (
	errAgentBusy = errors.New("agent is already running")
)

// AgentRuntime is implemented by the server because it owns sessions and the
// provider. Tools only depend on this narrow boundary, which keeps the tools
// package independent from HTTP orchestration.
type AgentRuntime interface {
	TaskStore
	SpawnAgent(context.Context, AgentRequest) (AgentLaunch, error)
}

// AgentStore owns stable logical-agent records and the transient run state.
// The child session transcript and agent metadata are durable; this store is
// rebuilt when the server starts.
type AgentStore struct {
	mu     sync.RWMutex
	tasks  map[string]*agentTask
	closed bool
	seq    uint64
}

type agentTask struct {
	mu              sync.Mutex
	snap            TaskSnapshot
	done            chan struct{}
	doneClosed      bool
	runDone         chan struct{}
	active          bool
	removed         bool
	cancel          context.CancelFunc
	run             AgentRun
	metadataPath    string
	parentSessionID string
	subagentType    string
	model           string
	pending         []string
	runCount        uint64
	notifiedRun     uint64
}

// NewAgentStore creates a process-scoped child-agent registry.
func NewAgentStore() *AgentStore { return &AgentStore{tasks: map[string]*agentTask{}} }

// Start registers and runs one logical child agent. Background children
// detach from the parent's prompt context; foreground children inherit it and
// are waited on by Agent.Execute.
func (s *AgentStore) Start(ctx context.Context, req AgentRequest, outputFile string, run AgentRun) (AgentLaunch, error) {
	if run == nil {
		return AgentLaunch{}, errAgentRunnerNil
	}
	id := fmt.Sprintf("a-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&s.seq, 1))
	task := &agentTask{
		snap: TaskSnapshot{
			TaskID: id, TaskType: "local_agent", Status: TaskPending,
			Description: req.Description, Command: req.Prompt,
			Prompt: req.Prompt, SessionID: req.SessionID,
			OutputFile: outputFile,
		},
		done:            make(chan struct{}),
		run:             run,
		metadataPath:    req.MetadataPath,
		parentSessionID: req.ParentSessionID,
		subagentType:    req.SubagentType,
		model:           req.Model,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return AgentLaunch{}, errTaskStoreClosed
	}
	s.tasks[id] = task
	s.mu.Unlock()

	// Why: background children outlive the parent prompt; keep values but detach cancel.
	parent := ctx
	if req.RunInBackground {
		parent = context.WithoutCancel(ctx)
	}
	if err := s.startRun(parent, task, req.Prompt, req.RunInBackground); err != nil {
		s.mu.Lock()
		delete(s.tasks, id)
		s.mu.Unlock()
		return AgentLaunch{}, err
	}
	return AgentLaunch{TaskID: id, SessionID: req.SessionID, Description: req.Description, Prompt: req.Prompt, OutputFile: outputFile}, nil
}

func (s *AgentStore) startRun(ctx context.Context, task *agentTask, prompt string, background bool) error {
	if background {
		// Why: process-owned follow-ups must not cancel with a prior request.
		ctx = context.WithoutCancel(ctx)
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errTaskStoreClosed
	}

	runCtx, cancel := context.WithCancel(ctx)
	task.mu.Lock()
	if task.removed {
		task.mu.Unlock()
		s.mu.RUnlock()
		cancel()
		return os.ErrNotExist
	}
	if task.snap.Status == TaskRunning || task.active {
		task.mu.Unlock()
		s.mu.RUnlock()
		cancel()
		return errAgentBusy
	}
	task.runCount++
	task.active = true
	task.cancel = cancel
	task.runDone = make(chan struct{})
	task.done = make(chan struct{})
	task.doneClosed = false
	task.snap.Status = TaskRunning
	task.snap.Prompt = prompt
	task.snap.Command = prompt
	task.snap.Output = ""
	task.snap.Result = ""
	task.snap.Error = ""
	task.snap.ExitCode = nil
	task.snap.ToolUseCount = 0
	task.snap.TotalTokens = 0
	task.snap.StartedAt = time.Now()
	task.snap.FinishedAt = nil
	generation := task.runCount
	run := task.run
	runDone := task.runDone
	task.persistLocked()
	task.mu.Unlock()
	s.mu.RUnlock()

	go s.executeRun(runCtx, task, generation, prompt, background, run, runDone)
	return nil
}

func (s *AgentStore) executeRun(ctx context.Context, task *agentTask, generation uint64, prompt string, background bool, run AgentRun, runDone chan struct{}) {
	defer close(runDone)
	completion, err := run(ctx, task.snap.TaskID, prompt, background)

	var pending string
	task.mu.Lock()
	if generation != task.runCount {
		task.active = false
		task.runDone = nil
		task.mu.Unlock()
		return
	}
	// TaskStop/Close owns the terminal state when cancellation races the runner.
	if task.snap.Status != TaskKilled && task.snap.Status != TaskInterrupted {
		now := time.Now()
		task.snap.FinishedAt = &now
		task.snap.Result = completion.Result
		task.snap.Output = completion.Result
		task.snap.ToolUseCount = completion.ToolUseCount
		task.snap.TotalTokens = completion.TotalTokens
		if err != nil {
			task.snap.Status = TaskFailed
			task.snap.Error = err.Error()
		} else {
			task.snap.Status = TaskCompleted
		}
	}
	task.cancel = nil
	task.active = false
	task.runDone = nil
	task.closeDoneLocked()
	if len(task.pending) > 0 && task.snap.Status != TaskInterrupted && !task.removed {
		pending = task.pending[0]
		task.pending = task.pending[1:]
	}
	task.persistLocked()
	task.mu.Unlock()

	if pending != "" {
		// A message that arrived at the run boundary is a new detached run on
		// the same logical agent, not a new fork or agent ID.
		if err := s.startRun(ctx, task, pending, true); err != nil {
			task.mu.Lock()
			if !task.removed {
				task.pending = append([]string{pending}, task.pending...)
				task.persistLocked()
			}
			task.mu.Unlock()
		}
	}
}

func (t *agentTask) closeDoneLocked() {
	if !t.doneClosed {
		close(t.done)
		t.doneClosed = true
	}
}

func (t *agentTask) metadataLocked() AgentMetadata {
	return AgentMetadata{
		Version: 1, TaskID: t.snap.TaskID, SessionID: t.snap.SessionID,
		ParentSessionID: t.parentSessionID, Description: t.snap.Description,
		Prompt: t.snap.Prompt, SubagentType: t.subagentType, Model: t.model,
		OutputFile: t.snap.OutputFile, Status: t.snap.Status, Result: t.snap.Result,
		Error: t.snap.Error, ToolUseCount: t.snap.ToolUseCount,
		TotalTokens: t.snap.TotalTokens, StartedAt: t.snap.StartedAt,
		FinishedAt: t.snap.FinishedAt, Pending: append([]string(nil), t.pending...),
		RunCount: t.runCount, NotifiedRun: t.notifiedRun,
	}
}

func (t *agentTask) persistLocked() {
	if t.metadataPath == "" {
		return
	}
	meta := t.metadataLocked()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.metadataPath), 0o700); err != nil {
		return
	}
	tmp := t.metadataPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, t.metadataPath); err != nil {
		_ = os.Remove(tmp)
	}
}

// LoadMetadata rebuilds one logical agent after server restart. A run that
// was live in the previous process is explicitly marked interrupted because
// its provider goroutine and cancel function cannot be reconstructed.
func (s *AgentStore) LoadMetadata(path string, run AgentRun) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the session agent.json selected by the server index
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var meta AgentMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return false, fmt.Errorf("decode agent metadata %s: %w", path, err)
	}
	if meta.TaskID == "" || meta.SessionID == "" {
		return false, fmt.Errorf("%w: %s", errAgentMetadataIncomplete, path)
	}
	if run == nil {
		return false, fmt.Errorf("%w: %s", errAgentMetadataNoRunner, path)
	}
	task := &agentTask{
		snap: TaskSnapshot{
			TaskID: meta.TaskID, TaskType: "local_agent", SessionID: meta.SessionID,
			Status: meta.Status, Description: meta.Description, Command: meta.Prompt,
			OutputFile: meta.OutputFile, Error: meta.Error, Prompt: meta.Prompt,
			Result: meta.Result, Output: meta.Result, ToolUseCount: meta.ToolUseCount,
			TotalTokens: meta.TotalTokens, StartedAt: meta.StartedAt, FinishedAt: meta.FinishedAt,
		},
		done: make(chan struct{}), doneClosed: true, run: run, metadataPath: path,
		parentSessionID: meta.ParentSessionID, subagentType: meta.SubagentType,
		model: meta.Model, pending: append([]string(nil), meta.Pending...), runCount: meta.RunCount,
		notifiedRun: meta.NotifiedRun,
	}
	if task.snap.Status == TaskRunning || task.snap.Status == TaskPending {
		now := time.Now()
		task.snap.Status = TaskInterrupted
		task.snap.FinishedAt = &now
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, errTaskStoreClosed
	}
	if _, exists := s.tasks[meta.TaskID]; exists {
		s.mu.Unlock()
		return false, fmt.Errorf("%w %s", errDuplicateAgentTaskID, meta.TaskID)
	}
	s.tasks[meta.TaskID] = task
	s.mu.Unlock()
	task.mu.Lock()
	task.persistLocked()
	task.mu.Unlock()
	return true, nil
}

// QueueOrResume delivers a message when the live run boundary has already
// closed. A live run should first be steered through the server's runState;
// this method is the race-safe fallback and terminal resume path.
func (s *AgentStore) QueueOrResume(ctx context.Context, id, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errMessageRequired
	}
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return "", errTaskStoreClosed
	}
	task, ok := s.task(id)
	if !ok {
		return "", os.ErrNotExist
	}
	task.mu.Lock()
	if task.removed {
		task.mu.Unlock()
		return "", os.ErrNotExist
	}
	if task.active || task.snap.Status == TaskRunning || task.snap.Status == TaskPending {
		task.pending = append(task.pending, message)
		task.persistLocked()
		task.mu.Unlock()
		return "queued", nil
	}
	prompt := message
	if len(task.pending) > 0 {
		prompt = task.pending[0]
		task.pending = append(task.pending[:0], task.pending[1:]...)
		task.pending = append(task.pending, message)
	}
	task.persistLocked()
	task.mu.Unlock()
	// Why: resumed agents are process-owned and must outlive the delivering request.
	if err := s.startRun(context.WithoutCancel(ctx), task, prompt, true); err != nil {
		if errors.Is(err, errAgentBusy) {
			task.mu.Lock()
			if !task.removed {
				task.pending = append([]string{prompt}, task.pending...)
				task.persistLocked()
			}
			task.mu.Unlock()
			return "queued", nil
		}
		return "", err
	}
	return "resumed", nil
}

// ResumePending starts the oldest durable follow-up message, if one exists.
// Server startup uses this for messages accepted just before a crash or
// graceful shutdown; explicit TaskStop records are intentionally not resumed.
func (s *AgentStore) ResumePending(id string) (bool, error) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return false, errTaskStoreClosed
	}
	task, ok := s.task(id)
	if !ok {
		return false, os.ErrNotExist
	}
	task.mu.Lock()
	if task.removed || task.active || task.snap.Status != TaskInterrupted || len(task.pending) == 0 {
		task.mu.Unlock()
		return false, nil
	}
	prompt := task.pending[0]
	task.pending = task.pending[1:]
	task.persistLocked()
	task.mu.Unlock()
	if err := s.startRun(context.Background(), task, prompt, true); err != nil {
		task.mu.Lock()
		if !task.removed {
			task.pending = append([]string{prompt}, task.pending...)
			task.persistLocked()
		}
		task.mu.Unlock()
		return false, err
	}
	return true, nil
}

// ClaimNotification makes one completion notification idempotent per logical
// agent run. The claim is persisted before the caller writes the parent queue,
// so concurrent completion paths cannot enqueue the same run twice.
func (s *AgentStore) ClaimNotification(id string) bool {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return false
	}
	task, ok := s.task(id)
	if !ok {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.removed || task.runCount == 0 || task.notifiedRun >= task.runCount {
		return false
	}
	task.notifiedRun = task.runCount
	task.persistLocked()
	return true
}

// SetSessionID associates the in-memory task with its durable child session.
func (s *AgentStore) SetSessionID(taskID, sessionID string) {
	if task, ok := s.task(taskID); ok {
		task.mu.Lock()
		task.snap.SessionID = sessionID
		task.persistLocked()
		task.mu.Unlock()
	}
}

// StopSession cancels all live agent tasks owned by a child session.
func (s *AgentStore) StopSession(sessionID string) {
	s.mu.RLock()
	tasks := make([]*agentTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		task.mu.Lock()
		match := task.snap.SessionID == sessionID
		task.mu.Unlock()
		if match {
			tasks = append(tasks, task)
		}
	}
	s.mu.RUnlock()
	for _, task := range tasks {
		task.mu.Lock()
		if task.snap.Status == TaskRunning {
			if task.cancel != nil {
				task.cancel()
			}
			now := time.Now()
			task.snap.Status = TaskKilled
			task.snap.FinishedAt = &now
			task.snap.Error = "session deleted"
			task.closeDoneLocked()
			task.persistLocked()
		}
		task.mu.Unlock()
	}
}

// RemoveSession forgets all agent records owned by a deleted child session.
// The runner may still be unwinding after cancellation, so metadataPath is
// cleared before removing the map entry; a late completion cannot recreate an
// agent.json below a directory that the server is deleting.
func (s *AgentStore) RemoveSession(sessionID string) {
	s.mu.Lock()
	for id, task := range s.tasks {
		task.mu.Lock()
		if task.snap.SessionID != sessionID {
			task.mu.Unlock()
			continue
		}
		task.removed = true
		task.metadataPath = ""
		if task.cancel != nil {
			task.cancel()
		}
		task.closeDoneLocked()
		task.mu.Unlock()
		delete(s.tasks, id)
	}
	s.mu.Unlock()
}

// Get returns a child task by task ID or transcript path.
func (s *AgentStore) Get(key string) (TaskSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, task := range s.tasks {
		task.mu.Lock()
		match := task.snap.TaskID == key || task.snap.OutputFile == key
		if match {
			snapshot := task.snap
			task.mu.Unlock()
			return snapshot, true
		}
		task.mu.Unlock()
	}
	return TaskSnapshot{}, false
}

// Wait blocks until a child reaches a terminal state or ctx is cancelled.
func (s *AgentStore) Wait(ctx context.Context, id string) (TaskSnapshot, error) {
	task, ok := s.task(id)
	if !ok {
		return TaskSnapshot{}, os.ErrNotExist
	}
	for {
		task.mu.Lock()
		done := task.done
		task.mu.Unlock()
		select {
		case <-done:
			task.mu.Lock()
			changed := task.done != done && task.active
			snapshot := task.snap
			task.mu.Unlock()
			if changed {
				continue
			}
			return snapshot, nil
		case <-ctx.Done():
			return s.snapshot(task), ctx.Err()
		}
	}
}

// Stop cancels the current run and leaves the stable agent record resumable.
func (s *AgentStore) Stop(id string) (TaskSnapshot, error) {
	task, ok := s.task(id)
	if !ok {
		return TaskSnapshot{}, os.ErrNotExist
	}
	task.mu.Lock()
	if isTerminal(task.snap.Status) {
		snapshot := task.snap
		task.mu.Unlock()
		return snapshot, errTaskNotRunning
	}
	if task.cancel != nil {
		task.cancel()
	}
	now := time.Now()
	task.snap.Status = TaskKilled
	task.snap.FinishedAt = &now
	task.snap.Error = "task stopped"
	task.closeDoneLocked()
	task.persistLocked()
	runDone := task.runDone
	snapshot := task.snap
	task.mu.Unlock()
	// A resumed child must not occupy the same session while the cancelled
	// provider/loop callback is still unwinding its transcript read.
	if runDone != nil {
		<-runDone
	}
	return snapshot, nil
}

// Close interrupts live runs so their metadata can be resumed after a new
// server starts. Explicit TaskStop remains the killed terminal state.
func (s *AgentStore) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	tasks := make([]*agentTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	s.mu.Unlock()
	var activeRuns []chan struct{}
	for _, task := range tasks {
		task.mu.Lock()
		if task.active || task.snap.Status == TaskRunning || task.snap.Status == TaskPending {
			if task.cancel != nil {
				task.cancel()
			}
			now := time.Now()
			task.snap.Status = TaskInterrupted
			task.snap.FinishedAt = &now
			task.snap.Error = "server stopped"
			task.closeDoneLocked()
			task.persistLocked()
			if task.runDone != nil {
				activeRuns = append(activeRuns, task.runDone)
			}
		}
		task.mu.Unlock()
	}
	// Why: Shutdown must not let a just-started child reopen or append its
	// transcript after the next server rebuilds the task index. Providers are
	// context-aware; the bound keeps a misbehaving provider from hanging close.
	for _, done := range activeRuns {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *AgentStore) task(id string) (*agentTask, bool) {
	s.mu.RLock()
	task, ok := s.tasks[id]
	s.mu.RUnlock()
	return task, ok
}

func (s *AgentStore) snapshot(task *agentTask) TaskSnapshot {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.snap
}
