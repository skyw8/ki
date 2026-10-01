package tools

import (
	"os"
	"time"
)

// TaskStatus is the lifecycle state of a logical agent.
type TaskStatus string

const (
	// TaskPending is reserved for agents accepted before turn admission.
	TaskPending TaskStatus = "pending"
	// TaskRunning identifies an active child turn, including mailbox waits.
	TaskRunning TaskStatus = "running"
	// TaskCompleted identifies a task that exited successfully.
	TaskCompleted TaskStatus = "completed"
	// TaskFailed identifies a task that exited unsuccessfully.
	TaskFailed TaskStatus = "failed"
	// TaskKilled identifies a task explicitly terminated by the user or abort.
	TaskKilled TaskStatus = "killed"
	// TaskInterrupted identifies a task whose server stopped before it reached
	// a normal terminal state. The durable agent metadata can be resumed later.
	TaskInterrupted TaskStatus = "interrupted"
	// shellWaitDelay bounds Wait after process-tree termination. Killing only
	// the shell launcher can leave descendants holding output pipes open, which
	// otherwise makes abort appear to hang. See the Bash abort postmortem.
	shellWaitDelay = 200 * time.Millisecond
)

// AgentSnapshot is the durable host view of a logical agent.
type AgentSnapshot struct {
	AgentProgress
	// ParentSessionID is host-private delivery ownership. A sibling may inspect
	// the output, but that read must not consume the real parent's notification.
	ParentSessionID string     `json:"-"`
	ClientRequestID string     `json:"-"`
	Generation      uint64     `json:"generation,omitzero"`
	TaskID          string     `json:"task_id"`
	TaskName        string     `json:"task_name,omitempty"`
	TaskPath        string     `json:"task_path,omitempty"`
	RootSessionID   string     `json:"root_session_id,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	Status          TaskStatus `json:"status"`
	Description     string     `json:"description,omitempty"`
	OutputFile      string     `json:"output_file,omitempty"`
	Error           string     `json:"error,omitempty"`
	Prompt          string     `json:"prompt,omitempty"`
	Result          string     `json:"result,omitempty"`
	ToolUseCount    int        `json:"tool_use_count,omitzero"`
	TotalTokens     int        `json:"total_tokens,omitzero"`
	StartedAt       time.Time  `json:"started_at,omitzero"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

// OutputSpool is the tool-output store contract used by shell tasks. Keeping
// it an interface lets tests run without a store while the server roots every
// complete-output file in one session-scoped directory.
type OutputSpool interface {
	CreateOutputFile(sessionID, toolName string) (*os.File, error)
}

func isTerminal(status TaskStatus) bool {
	return status == TaskCompleted || status == TaskFailed || status == TaskKilled || status == TaskInterrupted
}
