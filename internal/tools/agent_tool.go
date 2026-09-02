package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ki/internal/loop"
)

const agentPrompt = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches a specialized child agent that works independently with its own conversation branch. Specify subagent_type to select a specialized agent, or omit it to use the general-purpose agent. The child starts from the current conversation fork and uses the current session's model provider and model, so give it a directive describing exactly what to do.

## When to use

- Delegate research, implementation, review, or verification that can proceed independently.
- Launch independent agents in parallel when their work does not overlap.
- Use run_in_background=true for work whose result is not needed before continuing; use TaskOutput to wait for or inspect it.
- Use SendMessage with the returned agentId to steer a live background agent or resume it after completion; TaskOutput only observes and TaskStop terminates.

## Writing the prompt

Brief the agent like a smart colleague who just walked into the room. Explain the goal, relevant context, exact scope, files or commands to inspect, and the expected report. Do not ask it to delegate the understanding back to you.

## Fork behavior

Every child is created from the current session branch with forkMode=tree. The child has its own session transcript and can create further children. Child results are returned as a compact completed result or as an async task notification record.`

type agentTool struct{ runtime AgentRuntime }

func (agentTool) Name() string        { return "Agent" }
func (agentTool) Description() string { return "Launch a new agent." }
func (agentTool) Snippet() string     { return "Delegate work to a child agent" }
func (agentTool) Prompt() string      { return agentPrompt }

func (agentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"description", "prompt"},
		"properties": map[string]any{
			"description":       map[string]any{"type": "string", "description": "A short (3-5 word) description of the task."},
			"prompt":            map[string]any{"type": "string", "description": "The task for the agent to perform."},
			"subagent_type":     map[string]any{"type": "string", "description": "The type of specialized agent to use for this task."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Set to true to run this agent in the background. You will be notified when it completes."},
		},
	}
}

func (t agentTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t agentTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	if t.runtime == nil {
		return errRes("agent runtime is unavailable")
	}
	req := AgentRequest{
		Description:     stringArg(args, "description", "Running task"),
		Prompt:          stringArg(args, "prompt", ""),
		SubagentType:    stringArg(args, "subagent_type", "general-purpose"),
		RunInBackground: agentBoolArg(args, "run_in_background", false),
	}
	if req.Prompt == "" {
		return errRes("prompt is required")
	}
	launch, err := t.runtime.SpawnAgent(ctx, req)
	if err != nil {
		return errRes(err.Error())
	}
	if req.RunInBackground {
		res := jsonResult(map[string]any{
			"status": "async_launched", "agentId": launch.TaskID,
			"description": req.Description, "prompt": req.Prompt,
			"outputFile": launch.OutputFile, "canReadOutputFile": true,
		})
		res.Terminate = true
		return res
	}
	snapshot, waitErr := t.runtime.Wait(ctx, launch.TaskID)
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		return errRes(waitErr.Error())
	}
	if snapshot.Status != TaskCompleted {
		message := snapshot.Error
		if message == "" {
			message = fmt.Sprintf("agent %s ended with status %s", launch.TaskID, snapshot.Status)
		}
		return errRes(message)
	}
	return jsonResult(map[string]any{
		"status": "completed", "agentId": launch.TaskID,
		"agentType": req.SubagentType, "prompt": req.Prompt,
		"content":           []any{map[string]any{"type": "text", "text": snapshot.Result}},
		"totalToolUseCount": snapshot.ToolUseCount,
		"totalDurationMs":   durationMillis(snapshot), "totalTokens": snapshot.TotalTokens,
		"usage": map[string]any{
			"input_tokens": 0, "output_tokens": 0,
			"cache_creation_input_tokens": nil, "cache_read_input_tokens": nil,
			"server_tool_use": nil, "service_tier": nil, "cache_creation": nil,
		},
	})
}

func jsonResult(value any) loop.ToolResult {
	b, err := json.Marshal(value)
	if err != nil {
		return errRes(err.Error())
	}
	return okRes(string(b))
}

func agentBoolArg(args map[string]any, name string, fallback bool) bool {
	if value, ok := args[name].(bool); ok {
		return value
	}
	return fallback
}

func durationMillis(snapshot TaskSnapshot) int64 {
	if snapshot.FinishedAt == nil || snapshot.StartedAt.IsZero() {
		return 0
	}
	return snapshot.FinishedAt.Sub(snapshot.StartedAt).Milliseconds()
}
