package tools

import (
	"slices"
	"strings"
	"time"

	"ki/internal/loop"
	"ki/internal/types"
)

// AgentRunStats separates usage accumulated during a generation from its latest context size.
type AgentRunStats struct {
	MailboxWaitMs    int64 `json:"mailbox_wait_ms"`
	Tools            int   `json:"tools"`
	Requests         int   `json:"requests"`
	ToolFailures     int   `json:"tool_failures"`
	InputTokens      int   `json:"input_tokens"`
	OutputTokens     int   `json:"output_tokens"`
	CacheReadTokens  int   `json:"cache_read_tokens"`
	CacheWriteTokens int   `json:"cache_write_tokens"`
	TotalTokens      int   `json:"total_tokens"`
	ContextTokens    int   `json:"context_tokens"`
}
type AgentToolActivity struct {
	CallID    string    `json:"call_id"`
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
}

// AgentProgress is an event-reduced snapshot. It never includes reasoning content.
type AgentProgress struct {
	PendingTasks          int                 `json:"pending_tasks"`
	LifetimeStatsComplete bool                `json:"lifetime_stats_complete"`
	QueueWaitMs           int64               `json:"queue_wait_ms"`
	Revision              uint64              `json:"revision"`
	RunID                 string              `json:"run_id,omitempty"`
	Phase                 string              `json:"phase,omitempty"`
	LastActivityAt        time.Time           `json:"last_activity_at,omitzero"`
	CurrentTools          []AgentToolActivity `json:"current_tools,omitempty"`
	WaitingFor            string              `json:"waiting_for,omitempty"`
	RunStats              AgentRunStats       `json:"run_stats"`
	LifetimeStats         AgentRunStats       `json:"agent_lifetime_stats"`
}

func (s *AgentRunStats) addUsage(u *types.Usage) {
	if u == nil {
		return
	}
	s.InputTokens += u.Input
	s.OutputTokens += u.Output
	s.CacheReadTokens += u.CacheRead
	s.CacheWriteTokens += u.CacheWrite
	total := u.TotalTokens
	if total == 0 {
		total = u.Input + u.Output + u.CacheRead + u.CacheWrite
	}
	s.TotalTokens += total
	s.ContextTokens = u.Input + u.CacheRead + u.CacheWrite
}

// ReduceEvent ignores callbacks from old generations and only persists bounded execution boundaries.
func (c *AgentController) ReduceEvent(id string, generation uint64, ev loop.Event) error {
	switch ev.Type {
	case loop.AgentStart, loop.RequestHeader, loop.MessageEnd, loop.ToolExecutionStart, loop.ToolExecutionEnd, loop.CompactionEnd, loop.AgentEnd:
	default:
		return nil
	}
	task, ok := c.task(id)
	if !ok {
		return nil
	}
	task.mu.Lock()
	if task.removed || !task.active || task.runCount != generation || task.snap.Status != TaskRunning {
		task.mu.Unlock()
		return nil
	}
	now := time.Now()
	if ev.Timestamp > 0 {
		now = time.UnixMilli(ev.Timestamp)
	}
	p := &task.snap.AgentProgress
	p.Revision++
	p.RunID = ev.RunID
	p.LastActivityAt = now
	if p.Phase == "starting" {
		p.Phase = "executing"
	}
	switch ev.Type {
	case loop.RequestHeader:
		p.RunStats.Requests++
		p.LifetimeStats.Requests++
	case loop.MessageEnd:
		if ev.Message != nil && ev.Message.Role == "assistant" {
			p.RunStats.addUsage(ev.Message.Usage)
			p.LifetimeStats.addUsage(ev.Message.Usage)
			if text := strings.TrimSpace(ev.Message.Text()); text != "" {
				task.snap.Result = text
			}
		}
	case loop.CompactionEnd:
		p.RunStats.addUsage(ev.Usage)
		p.LifetimeStats.addUsage(ev.Usage)
	case loop.ToolExecutionStart:
		if task.activeTools == nil {
			task.activeTools = map[string]AgentToolActivity{}
		}
		if _, exists := task.activeTools[ev.ToolCallID]; !exists {
			task.activeTools[ev.ToolCallID] = AgentToolActivity{CallID: ev.ToolCallID, Name: ev.ToolName, StartedAt: now}
			p.RunStats.Tools++
			p.LifetimeStats.Tools++
		}
	case loop.ToolExecutionEnd:
		if _, exists := task.activeTools[ev.ToolCallID]; exists && ev.IsError {
			p.RunStats.ToolFailures++
			p.LifetimeStats.ToolFailures++
		}
		if activity, ok := task.activeTools[ev.ToolCallID]; ok && activity.Name == "wait_agent" {
			p.RunStats.MailboxWaitMs += ev.DurationMs
			p.LifetimeStats.MailboxWaitMs += ev.DurationMs
		}
		delete(task.activeTools, ev.ToolCallID)
	case loop.AgentEnd:
		p.Phase = "settled"
		task.activeTools = nil
	}
	p.WaitingFor = ""
	activities := make([]AgentToolActivity, 0, len(task.activeTools))
	for _, activity := range task.activeTools {
		activities = append(activities, activity)
		if activity.Name == "wait_agent" {
			p.Phase = "waiting_message"
			p.WaitingFor = "mailbox"
		}
	}
	if p.Phase == "waiting_message" && p.WaitingFor == "" {
		p.Phase = "executing"
	}
	slices.SortFunc(activities, func(a, b AgentToolActivity) int { return strings.Compare(a.CallID, b.CallID) })
	if len(activities) > 8 {
		activities = activities[:8]
	}
	p.CurrentTools = activities
	task.snap.ToolUseCount = p.RunStats.Tools
	task.snap.TotalTokens = p.RunStats.TotalTokens
	err := task.persistLocked()
	task.mu.Unlock()
	if err != nil {
		return err
	}
	c.publishTask(task)
	return nil
}
