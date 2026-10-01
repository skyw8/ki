package agent

import (
	"errors"
)

var (
	errTaskStoreClosed         = errors.New("task store is closed")
	errTaskNotRunning          = errors.New("task is not running")
	errAgentRunnerNil          = errors.New("agent runner is nil")
	errAgentMetadataIncomplete = errors.New("agent metadata is missing task or session ID")
	errAgentMetadataNoRunner   = errors.New("agent metadata has no runner")
	errDuplicateAgentTaskID    = errors.New("duplicate agent task ID")
	errMessageRequired         = errors.New("message is required")
	errBadMessage              = errors.New("bad message")
)
