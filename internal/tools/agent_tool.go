package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ki/internal/loop"
)

const agentPrompt = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches a child agent that works independently with its own conversation. It starts with a clean context and uses the current session's model provider and model.

When NOT to use the Agent tool:
- If you want to read a specific file path, use the Read tool instead of the Agent tool, to find the match more quickly.
- If you are searching for a specific definition, use the Grep tool instead, to find the match more quickly.
- If you are searching within a specific file or 2-3 files, use the Read tool instead of the Agent tool.

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do.
- Launch multiple agents concurrently whenever possible; to do that, use a single message with multiple tool uses.
- When the agent is done, it will return a single message back to you. The result is not visible to the user. To show the user the result, send a text message back with a concise summary.
- You can optionally run agents in the background with run_in_background. You are notified when a background agent completes, so do NOT sleep, poll, or proactively check its progress. Use TaskOutput to wait for or inspect it, and TaskStop to cancel it.
- Use SendMessage with the agentId to steer a live background agent or resume it after completion. Each Agent invocation starts fresh, so provide a complete task description.
- The agent's outputs should generally be trusted.
- Clearly tell the agent whether you expect it to write code or just to do research, since it is not aware of the user's intent.

## Writing the prompt

Brief the agent like a smart colleague who just walked into the room — it hasn't seen this conversation, doesn't know what you've tried, doesn't understand why this task matters.
- Explain what you're trying to accomplish and why.
- Describe what you've already learned or ruled out.
- Give enough context about the surrounding problem that the agent can make judgment calls rather than just following a narrow instruction.
- If you need a short response, say so ("report in under 200 words").
- Lookups: hand over the exact command. Investigations: hand over the question — prescribed steps become dead weight when the premise is wrong.

Terse command-style prompts produce shallow, generic work.

**Never delegate understanding.** Don't write "based on your findings, fix the bug" or "based on the research, implement it." Those phrases push synthesis onto the agent instead of doing it yourself. Write prompts that prove you understood: include file paths, line numbers, what specifically to change.`

type agentTool struct{ runtime AgentRuntime }

func (agentTool) Name() string        { return "Agent" }
func (agentTool) Description() string { return "Launch a new agent." }
func (agentTool) Snippet() string     { return "Delegate work to a child agent" }
func (agentTool) Prompt() string      { return agentPrompt }

// TODO: typed subagents. Claude Code exposes a subagent_type registry where each
// type carries its own system prompt, tool allow/deny list, and model (Explore
// and Plan are read-only; verification runs background checks). ki currently
// runs every child as one general-purpose agent, so the parameter is gone.
// Reintroduce it with those three axes rather than a label-only field.
func (agentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"description", "prompt"},
		"properties": map[string]any{
			"description":       map[string]any{"type": "string", "description": "A short (3-5 word) description of the task."},
			"prompt":            map[string]any{"type": "string", "description": "The task for the agent to perform."},
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
		"prompt":            req.Prompt,
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
