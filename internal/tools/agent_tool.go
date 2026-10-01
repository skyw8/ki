package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"ki/internal/loop"
	"strconv"
	"strings"
	"time"
)

type AgentWaitResult struct {
	WakeReason string `json:"wake_reason"`
	TimedOut   bool   `json:"timed_out"`
	Message    string `json:"message"`
}
type AgentView struct {
	AgentProgress
	TaskName     string     `json:"task_name"`
	SessionID    string     `json:"session_id,omitempty"`
	AgentID      string     `json:"agent_id,omitempty"`
	Generation   uint64     `json:"generation"`
	Status       TaskStatus `json:"status"`
	Description  string     `json:"description,omitempty"`
	StartedAt    time.Time  `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ToolUseCount int        `json:"tool_use_count"`
	TotalTokens  int        `json:"total_tokens"`
}

type agentTool struct {
	name    string
	runtime AgentRuntime
}

func (t agentTool) Name() string { return t.name }
func (t agentTool) Description() string {
	switch t.name {
	case "spawn_agent":
		return "Start a named child agent asynchronously."
	case "send_message":
		return "Deliver a message without starting an idle agent."
	case "followup_task":
		return "Start or queue the next task on an existing child agent."
	case "wait_agent":
		return "Wait for your mailbox or a user steer."
	case "interrupt_agent":
		return "Interrupt a child agent's current turn, retaining its identity."
	default:
		return "Inspect registered agents in this root tree."
	}
}
func (t agentTool) Snippet() string { return t.Description() }
func (t agentTool) Prompt() string {
	return t.Description() + " Use canonical paths such as /root/research; relative targets resolve from the caller. spawn_agent returns immediately. send_message is QueueOnly and does not resume idle agents; followup_task explicitly starts work and cannot target root. Final results are delivered automatically to the structural parent. After independent work, use wait_agent rather than sleep, repeated polling, or duplicate delegation. Waiting child turns still occupy execution capacity. interrupt_agent does not terminate owned shell processes. Names accept snake_case or PascalCase; prefer snake_case."
}
func (t agentTool) Parameters() map[string]any {
	properties := map[string]any{}
	required := []any{}
	switch t.name {
	case "spawn_agent":
		required = []any{"task_name", "message"}
		properties["task_name"] = map[string]any{"type": "string", "description": "Child name using lowercase ASCII letters, digits and underscores.", "maxLength": 64}
		properties["message"] = map[string]any{"type": "string", "description": "Self-contained initial task."}
		properties["fork_turns"] = map[string]any{"type": "string", "description": "all (default), none, or a positive number of complete turns."}
	case "send_message", "followup_task":
		required = []any{"target", "message"}
		properties["target"] = map[string]any{"type": "string", "description": "Canonical or caller-relative agent task path."}
		properties["message"] = map[string]any{"type": "string", "description": "Message or task instruction."}
	case "interrupt_agent":
		required = []any{"target"}
		properties["target"] = map[string]any{"type": "string", "description": "Child task path; root and self cannot be interrupted."}
	case "wait_agent":
		properties["timeout_ms"] = map[string]any{"type": "integer", "minimum": 10000, "maximum": 3600000, "description": "Mailbox observation budget; defaults to 30000ms."}
	case "list_agents":
		properties["path_prefix"] = map[string]any{"type": "string", "description": "Optional canonical or caller-relative subtree prefix."}
	}
	return objectSchema(required, properties)
}
func (t agentTool) Validate(args map[string]any) error {
	if err := validateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	if t.name == "spawn_agent" {
		if err := ValidateTaskName(stringArg(args, "task_name", "")); err != nil {
			return err
		}
		if _, err := ParseForkTurns(stringArg(args, "fork_turns", "all")); err != nil {
			return err
		}
	}
	if t.name == "spawn_agent" || t.name == "send_message" || t.name == "followup_task" {
		if strings.TrimSpace(stringArg(args, "message", "")) == "" {
			return fmt.Errorf("message must not be empty")
		}
	}
	return nil
}
func (t agentTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	if err := t.Validate(args); err != nil {
		return errRes(err.Error())
	}
	var value any
	var err error
	switch t.name {
	case "spawn_agent":
		launch, e := t.runtime.SpawnAgent(ctx, AgentRequest{TaskName: stringArg(args, "task_name", ""), Description: stringArg(args, "task_name", ""), Prompt: stringArg(args, "message", ""), ForkTurns: stringArg(args, "fork_turns", "all")})
		err = e
		value = map[string]any{"task_name": launch.TaskPath, "agent_id": launch.TaskID, "session_id": launch.SessionID}
	case "send_message", "followup_task":
		value, err = t.runtime.SendAgentMessage(ctx, AgentMessageRequest{Target: stringArg(args, "target", ""), Message: stringArg(args, "message", ""), TriggerTurn: t.name == "followup_task"})
	case "wait_agent":
		value, err = t.runtime.WaitAgent(ctx, "", time.Duration(intArg(args, "timeout_ms", 30000))*time.Millisecond)
	case "list_agents":
		views, e := t.runtime.ListAgents("", stringArg(args, "path_prefix", ""))
		err = e
		count := len(views)
		if count > 128 {
			views = views[:128]
		}
		value = map[string]any{"agents": views, "total": count, "truncated": count > 128}
	case "interrupt_agent":
		value, err = t.runtime.InterruptAgent(ctx, "", stringArg(args, "target", ""))
	}
	if err != nil {
		return errRes(err.Error())
	}
	b, err := json.Marshal(value)
	if err != nil {
		return errRes(err.Error())
	}
	result := okRes(string(b))
	result.Details = value
	return result
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
func intArg(args map[string]any, key string, def int) int {
	if n, ok := asInt(args[key]); ok {
		return n
	}
	return def
}
func boolDefault(args map[string]any, key string, def bool) bool {
	if n, ok := args[key].(bool); ok {
		return n
	}
	return def
}
