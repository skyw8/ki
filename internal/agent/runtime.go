package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WaitResult struct {
	WakeReason string `json:"wake_reason"`
	TimedOut   bool   `json:"timed_out"`
	Message    string `json:"message"`
}

type View struct {
	Progress
	TaskName     string     `json:"task_name"`
	SessionID    string     `json:"session_id,omitempty"`
	AgentID      string     `json:"agent_id,omitempty"`
	Generation   uint64     `json:"generation"`
	Status       Status     `json:"status"`
	Description  string     `json:"description,omitempty"`
	StartedAt    time.Time  `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ToolUseCount int        `json:"tool_use_count"`
	TotalTokens  int        `json:"total_tokens"`
}

func ValidateTaskName(name string) error {
	if name == "" || name == "root" || len(name) > 64 {
		return fmt.Errorf("invalid task_name %q", name)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("invalid task_name %q", name)
		}
	}
	return nil
}

// ParseForkTurns returns -1 for all and 0 for none.
func ParseForkTurns(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return -1, nil
	case "none":
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("fork_turns must be all, none or a positive integer")
	}
	return n, nil
}
