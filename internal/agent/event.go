package agent

import (
	"ki/internal/types"
)

type ProgressEvent struct {
	Type                          ProgressEventType
	Timestamp                     int64
	RunID, CallID, ToolName, Text string
	DurationMs                    int64
	IsError                       bool
	Usage                         *types.Usage
}
type ProgressEventType uint8

const (
	RunStarted ProgressEventType = iota + 1
	RequestStarted
	MessageCompleted
	ToolStarted
	ToolFinished
	CompactionCompleted
	RunFinished
)
