package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ki/internal/idgen"
	"ki/internal/state"
	"ki/internal/types"
)

// Run executes one run of a logical agent. The logical agent ID remains
// stable across detached turns and explicit follow-up tasks.
type Run func(context.Context, string, string) (Completion, error)

// Request describes one child agent launch. The parent session owns the
// child edge; the server selects completed history using ForkTurns.
type Request struct {
	TaskName        string
	TaskPath        string
	RootSessionID   string
	ForkTurns       string
	ClientRequestID string
	Description     string
	Prompt          string
	ParentSessionID string
	// SessionID is assigned by server after it creates the tree child and is
	// used to stop the task if that session is deleted immediately.
	SessionID string
	// MetadataPath is next to the child transcript and makes the agent index
	// recoverable after the serve process exits.
	MetadataPath string
	// OutputFile is the child transcript path used in completion notifications.
	OutputFile string
}

// Launch is returned as soon as a child task has been accepted.
type Launch struct {
	TaskName    string
	TaskPath    string
	TaskID      string
	SessionID   string
	Description string
	Prompt      string
	OutputFile  string
}

// Completion is the compact final result kept in the task registry.
type Completion struct {
	Result       string
	ToolUseCount int
	TotalTokens  int
}

// MessageRequest is the provider-neutral message sent to one logical
// agent, addressed by a root-scoped canonical or relative task path.
type MessageRequest struct {
	Target          string
	Message         string
	SenderSessionID string
	TriggerTurn     bool
}

// MessageResult reports durable context acceptance or explicit task admission.
type MessageResult struct {
	AgentID  string `json:"agent_id"`
	TaskName string `json:"task_name,omitempty"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

// Input keeps a follow-up's identity stable while it waits for a new run.
type Input struct {
	AcceptedAt      time.Time `json:"accepted_at,omitzero"`
	Prompt          string    `json:"prompt"`
	ClientRequestID string    `json:"clientRequestId"`
}

const agentMetadataVersion = 3

// Metadata is the durable logical-agent record. Process handles such as
// cancel functions and goroutines are deliberately absent; a running record
// is marked interrupted when a new server rebuilds the index.
type Metadata struct {
	Progress
	TaskName        string            `json:"task_name,omitempty"`
	TaskPath        string            `json:"task_path,omitempty"`
	RootSessionID   string            `json:"root_session_id,omitempty"`
	ClientRequestID string            `json:"clientRequestId,omitempty"`
	Version         int               `json:"version"`
	TaskID          string            `json:"task_id"`
	SessionID       string            `json:"session_id"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	Description     string            `json:"description,omitempty"`
	Prompt          string            `json:"prompt,omitempty"`
	OutputFile      string            `json:"output_file,omitempty"`
	Status          Status            `json:"status"`
	Result          string            `json:"result,omitempty"`
	Error           string            `json:"error,omitempty"`
	ToolUseCount    int               `json:"tool_use_count,omitzero"`
	TotalTokens     int               `json:"total_tokens,omitzero"`
	StartedAt       time.Time         `json:"started_at,omitzero"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	Pending         []Input           `json:"pending,omitempty"`
	RunCount        uint64            `json:"run_count"`
	Deliveries      map[uint64]string `json:"deliveries,omitempty"`
}

var (
	errAgentBusy     = errors.New("agent is already running")
	errAgentCapacity = errors.New("agent execution capacity reached")
)

// Runtime is implemented by the server because it owns sessions and the
// provider. Tools only depend on this narrow boundary, which keeps the tools
// package independent from HTTP orchestration.
type Runtime interface {
	SpawnAgent(context.Context, Request) (Launch, error)
	SendAgentMessage(context.Context, MessageRequest) (MessageResult, error)
	WaitAgent(context.Context, string, time.Duration) (WaitResult, error)
	ListAgents(string, string) ([]View, error)
	InterruptAgent(context.Context, string, string) (View, error)
}

// Controller owns stable logical-agent records and the transient run state.
// The child session transcript and agent metadata are durable; this store is
// rebuilt when the server starts.
type Controller struct {
	listener      func(Snapshot)
	executionMu   sync.Mutex
	executing     map[string]int
	maxConcurrent int
	reservations  map[string]chan struct{}
	blocked       map[string]int
	mu            sync.RWMutex
	tasks         map[string]*agentTask
	closed        bool
	closeDone     chan struct{}
	runWG         sync.WaitGroup
	seq           atomic.Uint64
}

type agentTask struct {
	activeTools     map[string]ToolActivity
	admission       sync.Mutex
	mu              sync.Mutex
	snap            Snapshot
	done            chan struct{}
	doneClosed      bool
	runDone         chan struct{}
	active          bool
	removed         bool
	cancel          context.CancelFunc
	run             Run
	metadataPath    string
	parentSessionID string
	pending         []Input
	runCount        uint64
	deliveries      map[uint64]string
	result          *Snapshot
}

// NewController creates a process-scoped child-agent registry.
func NewController() *Controller {
	return &Controller{tasks: map[string]*agentTask{}, executing: map[string]int{}, maxConcurrent: 4, reservations: map[string]chan struct{}{}, blocked: map[string]int{}}
}

// Start registers and runs one logical child agent. Every child is process-owned
// (its run context does not inherit the caller's), so observation or parent
// turn cancellation does not cancel its work.
func (s *Controller) Start(ctx context.Context, req Request, outputFile string, run Run) (Launch, error) {
	if run == nil {
		return Launch{}, errAgentRunnerNil
	}
	id := fmt.Sprintf("a-%d-%d", time.Now().UnixNano(), s.seq.Add(1))
	task := &agentTask{
		snap: Snapshot{
			Progress: Progress{LifetimeStatsComplete: true},
			TaskID:   id, Status: Pending,
			ParentSessionID: req.ParentSessionID, TaskName: req.TaskName, TaskPath: req.TaskPath, RootSessionID: req.RootSessionID,
			Description: req.Description,
			Prompt:      req.Prompt, SessionID: req.SessionID,
			OutputFile: outputFile,
		},
		done:            make(chan struct{}),
		run:             run,
		metadataPath:    req.MetadataPath,
		parentSessionID: req.ParentSessionID,
	}
	// Why: publishing a pending identity before its initial input is admitted
	// lets a concurrent interrupt or follow-up overtake the launch. Register
	// the identity in the same transaction that persists its first generation.
	if err := s.startRun(ctx, task, req.Prompt, req.ClientRequestID, true); err != nil {
		return Launch{}, err
	}
	return Launch{TaskID: id, SessionID: req.SessionID, Description: req.Description, Prompt: req.Prompt, OutputFile: outputFile}, nil
}

func (s *Controller) startRun(ctx context.Context, task *agentTask, prompt, requestID string, register bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if requestID == "" {
		var err error
		requestID, err = idgen.NewV7()
		if err != nil {
			return err
		}
	}
	var unlockStore func()
	if register {
		s.mu.Lock()
		unlockStore = s.mu.Unlock
	} else {
		s.mu.RLock()
		unlockStore = s.mu.RUnlock
	}
	if s.closed {
		unlockStore()
		return errTaskStoreClosed
	}
	// Why: a child agent is process-owned, so its run never inherits the caller's
	// cancellation. Only explicit interruption, deletion or server shutdown owns
	// cancellation; observer timeouts never stop a child.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	task.mu.Lock()
	if task.removed {
		task.mu.Unlock()
		unlockStore()
		cancel()
		return os.ErrNotExist
	}
	if task.snap.Status == Running || task.active {
		task.mu.Unlock()
		unlockStore()
		cancel()
		return errAgentBusy
	}
	root := task.snap.RootSessionID
	if root == "" {
		root = task.parentSessionID
	}
	if s.blockedLocked(root, task.snap.TaskPath) {
		task.mu.Unlock()
		unlockStore()
		cancel()
		return fmt.Errorf("agent subtree is stopping")
	}
	s.executionMu.Lock()
	if s.executing[root] >= s.maxConcurrent {
		limit := s.maxConcurrent
		s.executionMu.Unlock()
		task.mu.Unlock()
		unlockStore()
		cancel()
		return fmt.Errorf("%w: active child turn capacity %d reached", errAgentCapacity, limit)
	}
	s.executing[root]++
	s.executionMu.Unlock()
	// Move pending input into the active generation in the same metadata write.
	// A crash between dequeue and start must not lose an accepted follow-up.
	var consumed *Input
	if len(task.pending) > 0 && task.pending[0].ClientRequestID == requestID {
		input := task.pending[0]
		consumed = &input
		task.pending = task.pending[1:]
	}
	task.snap.PendingTasks = len(task.pending)
	task.snap.Progress.Revision++
	task.snap.Progress.RunID = ""
	task.snap.Progress.Phase = "starting"
	task.snap.Progress.LastActivityAt = time.Now()
	task.snap.Progress.CurrentTools = nil
	task.snap.Progress.WaitingFor = ""
	task.snap.Progress.RunStats = RunStats{}
	task.snap.Progress.QueueWaitMs = 0
	if consumed != nil && !consumed.AcceptedAt.IsZero() {
		task.snap.Progress.QueueWaitMs = time.Since(consumed.AcceptedAt).Milliseconds()
	}
	task.activeTools = nil
	task.runCount++
	task.snap.Generation = task.runCount
	task.snap.ClientRequestID = requestID
	task.active = true
	task.cancel = cancel
	task.runDone = make(chan struct{})
	task.done = make(chan struct{})
	task.result = new(Snapshot)
	task.doneClosed = false
	task.snap.Status = Running
	task.snap.Prompt = prompt
	task.snap.Result = ""
	task.snap.Error = ""
	task.snap.ToolUseCount = 0
	task.snap.TotalTokens = 0
	task.snap.StartedAt = time.Now()
	task.snap.FinishedAt = nil
	generation := task.runCount
	run := task.run
	runDone := task.runDone
	if err := task.persistLocked(); err != nil {
		if consumed != nil {
			task.pending = append([]Input{*consumed}, task.pending...)
			task.snap.PendingTasks = len(task.pending)
		}
		task.active = false
		task.cancel = nil
		close(runDone)
		task.runDone = nil
		task.snap.Status = Failed
		task.snap.Error = err.Error()
		task.closeDoneLocked()
		task.mu.Unlock()
		unlockStore()
		cancel()
		s.executionMu.Lock()
		s.executing[root]--
		s.executionMu.Unlock()
		return err
	}
	if register {
		s.tasks[task.snap.TaskID] = task
	}
	// Admission is fenced by the controller lock, so Close cannot start waiting
	// before every accepted generation has joined the shared cleanup barrier.
	s.runWG.Add(1)
	task.mu.Unlock()
	unlockStore()

	s.publishTask(task)
	go func() {
		defer s.runWG.Done()
		// Why: the generation owns this context even when its runner succeeds.
		// Release context-bound watchers after settlement, not only on Stop.
		defer cancel()
		s.executeRun(runCtx, task, generation, prompt, run, runDone, root)
	}()
	return nil
}

func (s *Controller) executeRun(ctx context.Context, task *agentTask, generation uint64, prompt string, run Run, runDone chan struct{}, root string) {
	release := sync.OnceFunc(func() { s.executionMu.Lock(); s.executing[root]--; s.executionMu.Unlock(); go s.resumeReady(root) })
	defer release()
	defer func() {
		close(runDone)
		task.mu.Lock()
		if task.runDone == runDone {
			task.runDone = nil
		}
		task.mu.Unlock()
	}()
	completion, err := run(ctx, task.snap.TaskID, prompt)

	task.admission.Lock()
	defer task.admission.Unlock()
	task.mu.Lock()
	if generation != task.runCount {
		task.active = false
		task.mu.Unlock()
		return
	}
	// Explicit interruption or shutdown owns the terminal state when cancellation races the runner.
	if task.snap.Status != Killed && task.snap.Status != Interrupted {
		now := time.Now()
		task.snap.FinishedAt = &now
		task.snap.Result = completion.Result
		if task.snap.RunID == "" {
			task.snap.RunStats.Tools = completion.ToolUseCount
			task.snap.RunStats.TotalTokens = completion.TotalTokens
			task.snap.LifetimeStats.Tools += completion.ToolUseCount
			task.snap.LifetimeStats.TotalTokens += completion.TotalTokens
		}
		task.snap.Phase = "settled"
		task.snap.WaitingFor = ""
		task.snap.CurrentTools = nil
		task.snap.Revision++
		task.snap.LastActivityAt = now
		task.snap.ToolUseCount = completion.ToolUseCount
		task.snap.TotalTokens = completion.TotalTokens
		if err != nil {
			task.snap.Status = Failed
			task.snap.Error = err.Error()
		} else {
			task.snap.Status = Completed
		}
	}
	task.cancel = nil
	task.active = false
	task.closeDoneLocked()
	task.persistLocked()
	task.mu.Unlock()

	s.publishTask(task)
	release()
	// Follow-up work runs on the same identity after this generation finishes.
	// startNext keeps pending input durable until the new generation is admitted.
	_, _ = s.startNext(ctx, task, false)
}

func (t *agentTask) closeDoneLocked() {
	if !t.doneClosed {
		if t.result != nil {
			*t.result = t.snap
		}
		close(t.done)
		t.doneClosed = true
	}
}

func (t *agentTask) metadataLocked() Metadata {
	return Metadata{
		Progress: t.snap.Progress,
		Version:  agentMetadataVersion, TaskID: t.snap.TaskID, SessionID: t.snap.SessionID, TaskName: t.snap.TaskName, TaskPath: t.snap.TaskPath, RootSessionID: t.snap.RootSessionID,
		ParentSessionID: t.parentSessionID, Description: t.snap.Description,
		ClientRequestID: t.snap.ClientRequestID, Prompt: t.snap.Prompt, OutputFile: t.snap.OutputFile, Status: t.snap.Status, Result: t.snap.Result,
		Error: t.snap.Error, ToolUseCount: t.snap.ToolUseCount,
		TotalTokens: t.snap.TotalTokens, StartedAt: t.snap.StartedAt,
		FinishedAt: t.snap.FinishedAt, Pending: slices.Clone(t.pending),
		RunCount: t.runCount, Deliveries: t.deliveries,
	}
}

func (t *agentTask) persistLocked() error {
	if t.metadataPath == "" {
		return nil
	}
	return state.WriteVersioned(t.metadataPath, agentMetadataVersion, t.metadataLocked(), 0o600)
}

// LoadMetadata rebuilds one logical agent after server restart. A run that
// was live in the previous process is explicitly marked interrupted because
// its provider goroutine and cancel function cannot be reconstructed.
func (s *Controller) LoadMetadata(path string, run Run) (bool, error) {
	meta, err := ReadMetadata(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if meta.TaskID == "" || meta.SessionID == "" {
		return false, fmt.Errorf("%w: %s", errAgentMetadataIncomplete, path)
	}
	if run == nil {
		return false, fmt.Errorf("%w: %s", errAgentMetadataNoRunner, path)
	}
	task := &agentTask{
		snap: Snapshot{
			Progress:        meta.Progress,
			ClientRequestID: meta.ClientRequestID, Generation: meta.RunCount, TaskID: meta.TaskID, SessionID: meta.SessionID,
			ParentSessionID: meta.ParentSessionID, TaskName: meta.TaskName, TaskPath: meta.TaskPath, RootSessionID: meta.RootSessionID,
			Status: meta.Status, Description: meta.Description,
			OutputFile: meta.OutputFile, Error: meta.Error, Prompt: meta.Prompt,
			Result: meta.Result, ToolUseCount: meta.ToolUseCount,
			TotalTokens: meta.TotalTokens, StartedAt: meta.StartedAt, FinishedAt: meta.FinishedAt,
		},
		done: make(chan struct{}), doneClosed: true, run: run, metadataPath: path,
		parentSessionID: meta.ParentSessionID,
		pending:         slices.Clone(meta.Pending), runCount: meta.RunCount,
		deliveries: meta.Deliveries,
	}
	if task.snap.Status == Running || task.snap.Status == Pending {
		now := time.Now()
		task.snap.Status = Interrupted
		task.snap.Phase = "interrupted"
		task.snap.CurrentTools = nil
		task.snap.WaitingFor = ""
		task.snap.Revision++
		task.snap.FinishedAt = &now
	}
	task.snap.PendingTasks = len(task.pending)
	snapshot := task.snap
	task.result = &snapshot
	close(task.done)
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

// QueueOrResume journals follow-up work before starting or returning a receipt.
func (s *Controller) QueueOrResume(ctx context.Context, id, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errMessageRequired
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	task, ok := s.task(id)
	if !ok {
		return "", os.ErrNotExist
	}
	task.admission.Lock()
	defer task.admission.Unlock()
	requestID, err := idgen.NewV7()
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	task.mu.Lock()
	root := task.snap.RootSessionID
	if root == "" {
		root = task.parentSessionID
	}
	if s.closed || s.blockedLocked(root, task.snap.TaskPath) {
		task.mu.Unlock()
		s.mu.RUnlock()
		return "", fmt.Errorf("agent controller or subtree is stopping")
	}
	if task.removed {
		task.mu.Unlock()
		s.mu.RUnlock()
		return "", os.ErrNotExist
	}
	task.pending = append(task.pending, Input{AcceptedAt: time.Now(), Prompt: message, ClientRequestID: requestID})
	previousStatus, previousPhase := task.snap.Status, task.snap.Phase
	task.snap.PendingTasks = len(task.pending)
	task.snap.LastActivityAt = time.Now()
	if !task.active {
		task.snap.Status = Pending
		task.snap.Phase = "waiting_resource"
	}
	task.snap.Revision++
	if err := task.persistLocked(); err != nil {
		task.snap.Status, task.snap.Phase = previousStatus, previousPhase
		task.snap.PendingTasks--
		task.pending = task.pending[:len(task.pending)-1]
		task.mu.Unlock()
		s.mu.RUnlock()
		return "", err
	}
	live := task.active
	task.mu.Unlock()
	s.mu.RUnlock()
	s.publishTask(task)
	if live {
		return "queued", nil
	}
	started, err := s.startNext(ctx, task, true)
	if errors.Is(err, errAgentCapacity) || errors.Is(err, errAgentBusy) {
		return "queued", nil
	}
	if err != nil {
		return "", err
	}
	if started {
		return "resumed", nil
	}
	return "queued", nil
}

// ResumePending recovers accepted work, while an explicit interrupt discards pending tasks.
func (s *Controller) ResumePending(id string) (bool, error) {
	task, ok := s.task(id)
	if !ok {
		return false, os.ErrNotExist
	}
	task.admission.Lock()
	defer task.admission.Unlock()
	return s.startNext(context.Background(), task, false)
}

func (s *Controller) startNext(ctx context.Context, task *agentTask, _ bool) (bool, error) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return false, errTaskStoreClosed
	}
	task.mu.Lock()
	if task.removed || task.active || len(task.pending) == 0 {
		task.mu.Unlock()
		return false, nil
	}
	prompt := task.pending[0]
	task.mu.Unlock()
	if err := s.startRun(context.WithoutCancel(ctx), task, prompt.Prompt, prompt.ClientRequestID, false); err != nil {
		if errors.Is(err, errAgentCapacity) {
			if waitErr := s.markWaitingForCapacity(task); waitErr != nil {
				return false, waitErr
			}
		}
		return false, err
	}
	return true, nil
}

func (s *Controller) markWaitingForCapacity(task *agentTask) error {
	s.mu.RLock()
	task.mu.Lock()
	if s.closed || task.removed || task.active || len(task.pending) == 0 || task.snap.Status == Pending && task.snap.Phase == "waiting_resource" {
		task.mu.Unlock()
		s.mu.RUnlock()
		return nil
	}
	// Why: another identity can acquire capacity between settlement and the
	// next generation. Accepted work must remain visibly pending and stoppable,
	// while the immutable prior-generation Wait result stays completed.
	task.snap.Status = Pending
	task.snap.Phase = "waiting_resource"
	task.snap.Revision++
	task.snap.LastActivityAt = time.Now()
	err := task.persistLocked()
	task.mu.Unlock()
	s.mu.RUnlock()
	if err == nil {
		s.publishTask(task)
	}
	return err
}

// Capacity release wakes accepted tasks from other identities in the same root.
func (s *Controller) resumeReady(root string) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return
	}
	ids := []string{}
	for id, task := range s.tasks {
		task.mu.Lock()
		taskRoot := task.snap.RootSessionID
		if taskRoot == "" {
			taskRoot = task.parentSessionID
		}
		match := taskRoot == root && !task.active && len(task.pending) > 0
		task.mu.Unlock()
		if match {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	slices.Sort(ids)
	for _, id := range ids {
		if _, err := s.ResumePending(id); errors.Is(err, errAgentCapacity) {
			return
		}
	}
}

// CommitNotification serializes durable parent persistence per generation.
// Mailbox observation and list_agents never consume a completion.
// The callback must persist the message before returning; on failure no claim is
// committed, so the same generation remains eligible on another handoff.
func (s *Controller) CommitNotification(identity types.CompletionIdentity, persist func() error) (bool, error) {
	task, ok := s.task(identity.TaskID)
	if !ok {
		return false, nil
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.removed || identity.Generation == 0 || task.deliveries[identity.Generation] != "" {
		return false, nil
	}
	if err := persist(); err != nil {
		return false, err
	}
	task.recordDeliveryLocked(identity.Generation, "notification")
	return true, nil
}

func (t *agentTask) recordDeliveryLocked(generation uint64, owner string) {
	if t.deliveries == nil {
		t.deliveries = make(map[uint64]string)
	}
	t.deliveries[generation] = owner
	t.persistLocked()
}

func (s *Controller) markConsumedLocked(task *agentTask) {
	if !task.removed && task.runCount > 0 && task.deliveries[task.runCount] == "" {
		task.recordDeliveryLocked(task.runCount, "interrupted")
	}
}

// CompletionDelivered is only an optimization before enqueue/dispatch. The
// authoritative claim still happens in CommitNotification at persistence.
func (s *Controller) CompletionDelivered(identity types.CompletionIdentity) bool {
	task, ok := s.task(identity.TaskID)
	if !ok {
		return true
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.removed || task.deliveries[identity.Generation] != ""
}

// StopSession cancels all live agent tasks owned by a child session.
func (s *Controller) StopSession(sessionID string) {
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
		if task.snap.Status == Running {
			if task.cancel != nil {
				task.cancel()
			}
			now := time.Now()
			task.snap.Status = Killed
			task.snap.Phase = "settled"
			task.snap.WaitingFor = ""
			task.snap.CurrentTools = nil
			task.snap.Revision++
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
func (s *Controller) RemoveSession(sessionID string) {
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
func (s *Controller) Get(key string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if task, ok := s.tasks[key]; ok {
		return s.snapshot(task), true
	}
	for _, task := range s.tasks {
		task.mu.Lock()
		match := task.snap.OutputFile == key
		if match {
			snapshot := task.snap
			task.mu.Unlock()
			return snapshot, true
		}
		task.mu.Unlock()
	}
	return Snapshot{}, false
}

// Wait binds to one generation: a concurrent resume must not replace the
// result a waiter is about to consume. The closed channel publishes an
// immutable snapshot retained by waiters, not an unbounded result history.
func (s *Controller) Wait(ctx context.Context, id string) (Snapshot, error) {
	task, ok := s.task(id)
	if !ok {
		return Snapshot{}, os.ErrNotExist
	}
	task.mu.Lock()
	done, result := task.done, task.result
	task.mu.Unlock()
	select {
	case <-done:
		// Finalization writes metadata under this lock after publishing done.
		// Wait must not return while that write still races caller cleanup.
		task.mu.Lock()
		snapshot := *result
		task.mu.Unlock()
		return snapshot, nil
	case <-ctx.Done():
		return s.snapshot(task), ctx.Err()
	}
}

// Stop cancels the current run and leaves the stable agent record resumable.
//
// Explicit interruption suppresses the current generation's completion and
// discards queued follow-up tasks; the identity remains available.
func (s *Controller) Stop(id string) (Snapshot, error) {
	task, ok := s.task(id)
	if !ok {
		return Snapshot{}, os.ErrNotExist
	}
	task.admission.Lock()
	task.mu.Lock()
	if isTerminal(task.snap.Status) && len(task.pending) == 0 {
		snapshot := task.snap
		runDone := task.runDone
		task.mu.Unlock()
		task.admission.Unlock()
		// Why: terminal status precedes the final publication callback. Host
		// deletion must drain that callback even when no running work remains.
		if runDone != nil {
			<-runDone
		}
		return snapshot, errTaskNotRunning
	}
	interruptedGeneration := task.active
	if task.cancel != nil {
		task.cancel()
	}
	now := time.Now()
	task.snap.Status = Killed
	task.snap.Phase = "settled"
	task.snap.WaitingFor = ""
	task.snap.CurrentTools = nil
	task.snap.Revision++
	task.snap.FinishedAt = &now
	task.snap.Error = "task interrupted"
	task.pending = nil
	task.snap.PendingTasks = 0
	// Pending-only interruption must not suppress a previous generation's
	// completed result that has not yet reached its structural parent.
	if interruptedGeneration {
		s.markConsumedLocked(task)
	}
	task.closeDoneLocked()
	task.persistLocked()
	runDone := task.runDone
	snapshot := task.snap
	task.mu.Unlock()
	task.admission.Unlock()
	s.publishTask(task)
	// A resumed child must not occupy the same session while the cancelled
	// provider/loop callback is still unwinding its transcript read.
	if runDone != nil {
		<-runDone
	}
	return snapshot, nil
}

// Close interrupts live runs and waits for all owned cleanup.
func (s *Controller) Close() {
	_ = s.CloseContext(context.Background())
}

// CloseContext fences admission once and observes a shared cleanup barrier.
// Caller cancellation only stops observation; owned cleanup still cancels and
// drains every accepted generation and in-flight spawn reservation.
func (s *Controller) CloseContext(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.closeDone = make(chan struct{})
		tasks := make([]*agentTask, 0, len(s.tasks))
		for _, task := range s.tasks {
			tasks = append(tasks, task)
		}
		reservations := make([]<-chan struct{}, 0, len(s.reservations))
		for _, done := range s.reservations {
			reservations = append(reservations, done)
		}
		go s.closeOwned(tasks, reservations, s.closeDone)
	}
	done := s.closeDone
	s.mu.Unlock()
	// A completed barrier wins over an already-cancelled observer.
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Controller) closeOwned(tasks []*agentTask, reservations []<-chan struct{}, done chan struct{}) {
	for _, task := range tasks {
		task.mu.Lock()
		if task.active || task.snap.Status == Running || task.snap.Status == Pending {
			if task.cancel != nil {
				task.cancel()
			}
			now := time.Now()
			task.snap.Status = Interrupted
			task.snap.Phase = "interrupted"
			task.snap.CurrentTools = nil
			task.snap.WaitingFor = ""
			task.snap.Revision++
			task.snap.FinishedAt = &now
			task.snap.Error = "server stopped"
			task.closeDoneLocked()
			task.persistLocked()
		}
		task.mu.Unlock()
	}
	// Why: a deadline bounds the caller's wait, not resource ownership. A
	// timed-out shutdown must not let a late callback reopen a deleted child
	// transcript or overlap a replacement server. Wait for full goroutine
	// settlement rather than abandoning runners after per-task timeouts.
	s.runWG.Wait()
	for _, reservation := range reservations {
		<-reservation
	}
	close(done)
}

// TaskForSession finds the logical agent that owns a child session.
func (s *Controller) TaskForSession(sessionID string) (Snapshot, bool) {
	if sessionID == "" {
		return Snapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, task := range s.tasks {
		task.mu.Lock()
		match := task.snap.SessionID == sessionID
		snapshot := task.snap
		task.mu.Unlock()
		if match {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

func (s *Controller) task(id string) (*agentTask, bool) {
	s.mu.RLock()
	task, ok := s.tasks[id]
	s.mu.RUnlock()
	return task, ok
}

func (s *Controller) snapshot(task *agentTask) Snapshot {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.snap
}

// ReadMetadata validates the durable version before either restoration or
// resumed-run setup. Old queue strings receive identity once during migration.
func ReadMetadata(path string) (Metadata, error) {
	data, _, err := state.ReadFile(path, agentMetadataVersion, map[int]state.Migration{1: func(raw []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		var pending []string
		if err := json.Unmarshal(doc["pending"], &pending); err != nil && len(doc["pending"]) != 0 {
			return nil, err
		}
		inputs := make([]Input, 0, len(pending))
		for _, text := range pending {
			id, err := idgen.NewV7()
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, Input{Prompt: text, ClientRequestID: id})
		}
		doc["pending"], _ = json.Marshal(inputs)
		var consumed uint64
		_ = json.Unmarshal(doc["consumed_run"], &consumed)
		if consumed > 0 {
			doc["deliveries"], _ = json.Marshal(map[uint64]string{consumed: "tool"})
		}
		delete(doc, "consumed_run")
		delete(doc, "notified_run")
		doc["version"] = json.RawMessage("2")
		return json.Marshal(doc)
	}, 2: func(raw []byte) ([]byte, error) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		var sessionID string
		_ = json.Unmarshal(doc["session_id"], &sessionID)
		doc["task_name"], _ = json.Marshal("legacy_" + sessionID)
		doc["version"] = json.RawMessage("3")
		return json.Marshal(doc)
	}})
	if err != nil {
		return Metadata{}, err
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, fmt.Errorf("decode agent metadata %s: %w", path, err)
	}
	return meta, nil
}

func (s *Controller) SetListener(listener func(Snapshot)) {
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
}

func (s *Controller) publishTask(task *agentTask) {
	s.mu.RLock()
	listener := s.listener
	s.mu.RUnlock()
	if listener == nil {
		return
	}
	task.mu.Lock()
	snapshot := task.snap
	task.mu.Unlock()
	listener(snapshot)
}
