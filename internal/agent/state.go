package agent

import (
	"time"
)

// Status is the lifecycle state of a logical agent.
type Status string

const (
	// Pending is reserved for agents accepted before turn admission.
	Pending Status = "pending"
	// Running identifies an active child turn, including mailbox waits.
	Running Status = "running"
	// Completed identifies a task that exited successfully.
	Completed Status = "completed"
	// Failed identifies a task that exited unsuccessfully.
	Failed Status = "failed"
	// Killed identifies a task explicitly terminated by the user or abort.
	Killed Status = "killed"
	// Interrupted identifies a task whose server stopped before it reached
	// a normal terminal state. The durable agent metadata can be resumed later.
	Interrupted Status = "interrupted"
)

// Snapshot is the durable host view of a logical agent.
type Snapshot struct {
	Progress
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
	Status          Status     `json:"status"`
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

func isTerminal(status Status) bool {
	return status == Completed || status == Failed || status == Killed || status == Interrupted
}
