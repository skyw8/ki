package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ki/internal/agent"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
)

type agentTool struct {
	name    string
	runtime agent.Runtime
}

func (t agentTool) Name() string { return t.name }

func (t agentTool) Description() string {
	switch t.name {
	case catalog.SpawnAgent:
		return "Start a named child agent asynchronously."
	case catalog.SendMessage:
		return "Deliver a message without starting an idle agent."
	case catalog.FollowupTask:
		return "Start or queue the next task on an existing child agent."
	case catalog.WaitAgent:
		return "Wait for your mailbox or a user steer."
	case catalog.InterruptAgent:
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
	case catalog.SpawnAgent:
		required = []any{"task_name", "message"}
		properties["task_name"] = map[string]any{"type": "string", "description": "Child name using lowercase ASCII letters, digits and underscores.", "maxLength": 64}
		properties["message"] = map[string]any{"type": "string", "description": "Self-contained initial task."}
		properties["fork_turns"] = map[string]any{"type": "string", "description": "all (default), none, or a positive number of complete turns."}
	case catalog.SendMessage, catalog.FollowupTask:
		required = []any{"target", "message"}
		properties["target"] = map[string]any{"type": "string", "description": "Canonical or caller-relative agent task path."}
		properties["message"] = map[string]any{"type": "string", "description": "Message or task instruction."}
	case catalog.InterruptAgent:
		required = []any{"target"}
		properties["target"] = map[string]any{"type": "string", "description": "Child task path; root and self cannot be interrupted."}
	case catalog.WaitAgent:
		properties["timeout_ms"] = map[string]any{"type": "integer", "minimum": 10000, "maximum": 3600000, "description": "Mailbox observation budget; defaults to 30000ms."}
	case catalog.ListAgents:
		properties["path_prefix"] = map[string]any{"type": "string", "description": "Optional canonical or caller-relative subtree prefix."}
	}
	return support.ObjectSchema(required, properties)
}

func (t agentTool) Validate(args map[string]any) error {
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	if t.name == catalog.SpawnAgent {
		if err := agent.ValidateTaskName(support.StringArg(args, "task_name", "")); err != nil {
			return err
		}
		if _, err := agent.ParseForkTurns(support.StringArg(args, "fork_turns", "all")); err != nil {
			return err
		}
	}
	if t.name == catalog.SpawnAgent || t.name == catalog.SendMessage || t.name == catalog.FollowupTask {
		if strings.TrimSpace(support.StringArg(args, "message", "")) == "" {
			return fmt.Errorf("message must not be empty")
		}
	}
	return nil
}

func (t agentTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	if err := t.Validate(args); err != nil {
		return support.Error(err.Error())
	}
	var value any
	var err error
	switch t.name {
	case catalog.SpawnAgent:
		launch, e := t.runtime.SpawnAgent(ctx, agent.Request{TaskName: support.StringArg(args, "task_name", ""), Description: support.StringArg(args, "task_name", ""), Prompt: support.StringArg(args, "message", ""), ForkTurns: support.StringArg(args, "fork_turns", "all")})
		err = e
		value = map[string]any{"task_name": launch.TaskPath, "agent_id": launch.TaskID, "session_id": launch.SessionID}
	case catalog.SendMessage, catalog.FollowupTask:
		value, err = t.runtime.SendAgentMessage(ctx, agent.MessageRequest{Target: support.StringArg(args, "target", ""), Message: support.StringArg(args, "message", ""), TriggerTurn: t.name == catalog.FollowupTask})
	case catalog.WaitAgent:
		value, err = t.runtime.WaitAgent(ctx, "", time.Duration(support.IntArg(args, "timeout_ms", 30000))*time.Millisecond)
	case catalog.ListAgents:
		views, e := t.runtime.ListAgents("", support.StringArg(args, "path_prefix", ""))
		err = e
		count := len(views)
		if count > 128 {
			views = views[:128]
		}
		value = map[string]any{"agents": views, "total": count, "truncated": count > 128}
	case catalog.InterruptAgent:
		value, err = t.runtime.InterruptAgent(ctx, "", support.StringArg(args, "target", ""))
	}
	if err != nil {
		return support.Error(err.Error())
	}
	b, err := json.Marshal(value)
	if err != nil {
		return support.Error(err.Error())
	}
	result := support.Text(string(b))
	result.Details = value
	return result
}
