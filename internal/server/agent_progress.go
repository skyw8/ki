package server

import (
	"ki/internal/agent"
	"ki/internal/loop"
)

// projectAgentProgress keeps the scheduler independent of the loop event union.
func projectAgentProgress(ev loop.Event) agent.ProgressEvent {
	progress := agent.ProgressEvent{Timestamp: ev.Timestamp, RunID: ev.RunID, CallID: ev.ToolCallID, ToolName: ev.ToolName, DurationMs: ev.DurationMs, IsError: ev.IsError}
	switch ev.Type {
	case loop.AgentStart:
		progress.Type = agent.RunStarted
	case loop.RequestHeader:
		progress.Type = agent.RequestStarted
	case loop.MessageEnd:
		progress.Type = agent.MessageCompleted
		if ev.Message != nil && ev.Message.Role == "assistant" {
			progress.Text = ev.Message.Text()
			progress.Usage = ev.Message.Usage
		}
	case loop.ToolExecutionStart:
		progress.Type = agent.ToolStarted
	case loop.ToolExecutionEnd:
		progress.Type = agent.ToolFinished
	case loop.CompactionEnd:
		progress.Type = agent.CompactionCompleted
		progress.Usage = ev.Usage
	case loop.AgentEnd:
		progress.Type = agent.RunFinished
	}
	return progress
}
