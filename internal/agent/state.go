package agent

import (
	"time"
)

type Status string

const (
	Pending     Status = "pending"
	Running     Status = "running"
	Completed   Status = "completed"
	Failed      Status = "failed"
	Killed      Status = "killed"
	Interrupted Status = "interrupted"
)

type Snapshot struct {
	Progress
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
