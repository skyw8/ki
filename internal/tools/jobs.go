package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatus is the lifecycle state exposed by Bash, TaskOutput, TaskStop and
// Monitor. A backgrounded task is still running; the separate state makes it
// possible to distinguish an explicit background task from a foreground one
// that was promoted after its waiting budget expired.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskRunning    TaskStatus = "running"
	TaskBackground TaskStatus = "backgrounded"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
	TaskKilled     TaskStatus = "killed"
	maxTaskTail               = 64 * 1024
	// shellWaitDelay bounds Wait after process-tree termination. Killing only
	// the shell launcher can leave descendants holding output pipes open, which
	// otherwise makes abort appear to hang. See the Bash abort postmortem.
	shellWaitDelay = 200 * time.Millisecond
)

// TaskSnapshot is the stable, model-facing view of a background task.
type TaskSnapshot struct {
	TaskID      string     `json:"task_id"`
	TaskType    string     `json:"task_type"`
	Status      TaskStatus `json:"status"`
	Description string     `json:"description,omitempty"`
	Command     string     `json:"command,omitempty"`
	OutputFile  string     `json:"output_file,omitempty"`
	PID         int        `json:"pid,omitempty"`
	Output      string     `json:"output,omitempty"`
	ExitCode    *int       `json:"exitCode,omitempty"`
	Error       string     `json:"error,omitempty"`
	Bytes       int64      `json:"bytes,omitempty"`
	Lines       int64      `json:"lines,omitempty"`
	StartedAt   time.Time  `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// TaskUpdate is emitted while a task writes output. Delta contains only the
// newly observed output; Snapshot contains the current counters and status.
type TaskUpdate struct {
	TaskID   string       `json:"task_id"`
	Delta    string       `json:"delta,omitempty"`
	Snapshot TaskSnapshot `json:"task"`
}

type JobStore struct {
	mu     sync.RWMutex
	jobs   map[string]*bgJob
	closed bool
	seq    uint64
}

type bgJob struct {
	mu          sync.Mutex
	id          string
	path        string
	cwd         string
	command     string
	description string
	taskType    string
	shell       shellSpec
	status      TaskStatus
	cmd         *exec.Cmd
	pid         int
	cancel      context.CancelFunc
	file        *os.File
	done        chan struct{}
	stopOnce    sync.Once
	startedAt   time.Time
	finishedAt  *time.Time
	exitCode    *int
	errText     string
	bytes       int64
	lines       int64
	tail        []byte
	lastEmit    time.Time
	pendingEmit []byte
	subs        map[chan TaskUpdate]struct{}
}

// NewJobStore creates a session-scoped task registry.
func NewJobStore() *JobStore { return &JobStore{jobs: map[string]*bgJob{}} }

// Start launches a command that remains independent of the prompt context.
func (s *JobStore) Start(_ context.Context, shell shellSpec, cwd, command, description, taskType string) (id, path string, err error) {
	job, err := s.newJob(shell, cwd, command, description, taskType, TaskBackground)
	if err != nil {
		return "", "", err
	}
	if err := job.start(); err != nil {
		s.remove(job)
		return "", "", err
	}
	return job.id, job.path, nil
}

// RunForeground starts a task with a foreground waiting context. If that
// context expires, the process is deliberately left alive and promoted to a
// background task. Parent cancellation still stops the process.
func (s *JobStore) RunForeground(ctx context.Context, shell shellSpec, cwd, command, description string, emit func(TaskUpdate)) (TaskSnapshot, error) {
	job, err := s.newJob(shell, cwd, command, description, "local_bash", TaskRunning)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if err := job.start(); err != nil {
		s.remove(job)
		return TaskSnapshot{}, err
	}
	updates, stop := job.subscribe()
	defer stop()
	for {
		select {
		case update := <-updates:
			if emit != nil {
				emit(update)
			}
		case <-job.done:
			return job.snapshot(), nil
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				job.setBackgrounded()
				return job.snapshot(), ctx.Err()
			}
			job.stop(true)
			return job.snapshot(), ctx.Err()
		}
	}
}

func (s *JobStore) newJob(shell shellSpec, cwd, command, description, taskType string, status TaskStatus) (*bgJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("task store is closed")
	}
	f, err := os.CreateTemp("", "ki-bg-*.log")
	if err != nil {
		return nil, fmt.Errorf("create task output: %w", err)
	}
	seq := atomic.AddUint64(&s.seq, 1)
	id := fmt.Sprintf("bg-%d-%d", time.Now().UnixNano(), seq)
	job := &bgJob{
		id: id, path: f.Name(), cwd: cwd, command: command,
		description: description, taskType: taskType, shell: shell, status: status, file: f,
		done: make(chan struct{}), subs: map[chan TaskUpdate]struct{}{}, startedAt: time.Now(),
	}
	s.jobs[id] = job
	s.jobs[job.path] = job
	return job, nil
}

func (j *bgJob) start() error {
	if !j.shell.available() {
		_ = j.file.Close()
		return errors.New("command interpreter is unavailable")
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	//nolint:gosec // executing the explicit shell tool command is the feature contract.
	cmd := exec.CommandContext(jobCtx, j.shell.path, j.shell.args(j.command)...)
	if j.cwd != "" {
		cmd.Dir = j.cwd
	}
	if env := j.shell.env(); env != nil {
		cmd.Env = env
	}
	detachCmd(cmd)
	cmd.Cancel = func() error {
		killCmd(cmd)
		return nil
	}
	cmd.WaitDelay = shellWaitDelay
	j.mu.Lock()
	j.cmd = cmd
	j.cancel = cancel
	j.mu.Unlock()
	cmd.Stdout = j
	cmd.Stderr = j
	if err := cmd.Start(); err != nil {
		cancel()
		_ = j.file.Close()
		return fmt.Errorf("start background command: %w", err)
	}
	j.mu.Lock()
	j.pid = cmd.Process.Pid
	j.mu.Unlock()
	go j.wait(cmd)
	return nil
}

func (j *bgJob) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	j.mu.Lock()
	if err == nil {
		j.status = TaskCompleted
	} else if j.status == TaskKilled {
		// Keep the explicit killed state even when the child reports SIGKILL.
	} else {
		j.status = TaskFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			j.exitCode = &code
		} else {
			j.errText = err.Error()
		}
	}
	if j.exitCode == nil && err == nil {
		code := 0
		j.exitCode = &code
	}
	now := time.Now()
	j.finishedAt = &now
	_ = j.file.Sync()
	_ = j.file.Close()
	pending := append([]byte(nil), j.pendingEmit...)
	j.pendingEmit = nil
	snapshot := j.snapshotLocked()
	j.mu.Unlock()
	if len(pending) > 0 {
		j.publish(TaskUpdate{TaskID: j.id, Delta: string(pending), Snapshot: snapshot})
	}
	j.publish(TaskUpdate{TaskID: j.id, Snapshot: snapshot})
	close(j.done)
}

func (j *bgJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	if j.file == nil {
		j.mu.Unlock()
		return 0, errors.New("task output is closed")
	}
	n, err := j.file.Write(p)
	if err != nil {
		j.mu.Unlock()
		return n, err
	}
	j.bytes += int64(n)
	j.lines += int64(bytes.Count(p[:n], []byte{'\n'}))
	j.tail = append(j.tail, p[:n]...)
	if len(j.tail) > maxTaskTail {
		j.tail = j.tail[len(j.tail)-maxTaskTail:]
	}
	j.pendingEmit = append(j.pendingEmit, p[:n]...)
	// Keep the output file lossless, but throttle live notifications so a noisy
	// compiler/log stream cannot turn every line into a model event.
	shouldEmit := time.Since(j.lastEmit) >= 100*time.Millisecond
	var delta []byte
	if shouldEmit {
		delta = append([]byte(nil), j.pendingEmit...)
		j.pendingEmit = nil
		j.lastEmit = time.Now()
	}
	j.mu.Unlock()
	if len(delta) > 0 {
		j.publish(TaskUpdate{TaskID: j.id, Delta: string(delta), Snapshot: j.snapshot()})
	}
	return n, nil
}

func (j *bgJob) setBackgrounded() {
	j.mu.Lock()
	if j.status == TaskRunning {
		j.status = TaskBackground
	}
	j.mu.Unlock()
}

func (j *bgJob) stop(killed bool) {
	j.stopOnce.Do(func() {
		j.mu.Lock()
		if killed && j.status != TaskCompleted && j.status != TaskFailed {
			j.status = TaskKilled
		}
		cancel, cmd := j.cancel, j.cmd
		j.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if cmd != nil {
			killCmd(cmd)
		}
	})
}

func (j *bgJob) subscribe() (<-chan TaskUpdate, func()) {
	ch := make(chan TaskUpdate, 64)
	j.mu.Lock()
	j.subs[ch] = struct{}{}
	j.mu.Unlock()
	return ch, func() {
		j.mu.Lock()
		if _, ok := j.subs[ch]; ok {
			delete(j.subs, ch)
			close(ch)
		}
		j.mu.Unlock()
	}
}

func (j *bgJob) publish(update TaskUpdate) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for ch := range j.subs {
		select {
		case ch <- update:
		default:
		}
	}
}

func (j *bgJob) snapshot() TaskSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *bgJob) snapshotLocked() TaskSnapshot {
	return TaskSnapshot{
		TaskID: j.id, TaskType: j.taskType, Status: j.status,
		Description: j.description, Command: j.command, OutputFile: j.path,
		PID:    j.pid,
		Output: string(j.tail), ExitCode: j.exitCode, Error: j.errText,
		Bytes: j.bytes, Lines: j.lines, StartedAt: j.startedAt, FinishedAt: j.finishedAt,
	}
}

// Get returns a task by task id or output file path.
func (s *JobStore) Get(key string) (TaskSnapshot, bool) {
	s.mu.RLock()
	j, ok := s.jobs[key]
	s.mu.RUnlock()
	if !ok {
		return TaskSnapshot{}, false
	}
	return j.snapshot(), true
}

// Subscribe returns a loss-tolerant progress stream for one task. The output
// file remains the source of truth; subscribers are only for live UI/model
// updates and may miss intermediate chunks when they are too slow.
func (s *JobStore) Subscribe(id string) (<-chan TaskUpdate, func(), error) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, nil, os.ErrNotExist
	}
	ch, stop := j.subscribe()
	return ch, stop, nil
}

// Done returns the task completion signal without exposing the process.
func (s *JobStore) Done(id string) (<-chan struct{}, bool) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return j.done, true
}

// Output returns the complete output captured so far by task id or path.
func (s *JobStore) Output(key string) (string, bool) {
	s.mu.RLock()
	j, ok := s.jobs[key]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(j.path)
	if err == nil {
		return string(b), true
	}
	return j.snapshot().Output, true
}

// Wait blocks until a task reaches a terminal state or ctx is cancelled.
func (s *JobStore) Wait(ctx context.Context, id string) (TaskSnapshot, error) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return TaskSnapshot{}, os.ErrNotExist
	}
	select {
	case <-j.done:
		return j.snapshot(), nil
	case <-ctx.Done():
		return j.snapshot(), ctx.Err()
	}
}

// Stop terminates a task's process group. Terminal tasks are left untouched.
func (s *JobStore) Stop(id string) (TaskSnapshot, error) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return TaskSnapshot{}, os.ErrNotExist
	}
	snapshot := j.snapshot()
	if isTerminal(snapshot.Status) {
		return snapshot, errors.New("task is not running")
	}
	j.stop(true)
	return j.snapshot(), nil
}

func isTerminal(status TaskStatus) bool {
	return status == TaskCompleted || status == TaskFailed || status == TaskKilled
}

func (s *JobStore) remove(j *bgJob) {
	s.mu.Lock()
	delete(s.jobs, j.id)
	delete(s.jobs, j.path)
	s.mu.Unlock()
	_ = os.Remove(j.path)
}

// Close stops all live tasks and removes temporary output files.
func (s *JobStore) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	seen := map[*bgJob]bool{}
	jobs := make([]*bgJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !seen[j] {
			seen[j] = true
			jobs = append(jobs, j)
		}
	}
	s.mu.Unlock()
	for _, j := range jobs {
		if !isTerminal(j.snapshot().Status) {
			j.stop(true)
		}
	}
	deadline := time.After(2 * time.Second)
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-deadline:
		}
		_ = j.file.Close()
		_ = os.Remove(j.path)
	}
}

var _ io.Writer = (*bgJob)(nil)
