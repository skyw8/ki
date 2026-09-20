package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ki/internal/loop"
)

// agentForegroundTimeout bounds how long a foreground Agent call pins the parent
// turn open. When it expires the child is promoted to a background task and the
// caller receives the same async_launched result as run_in_background, matching
// Claude Code's auto-background; promotion never cancels the child.
//
// Promotion deliberately leaves the parent turn running: the caller asked to
// block, so it keeps the decision to work on something else, to wait through
// TaskOutput, or to end its turn. The result it gets back says so. The
// completion reaches the caller either way, mid-turn through the run Inbox or as
// the next queued turn once this one has ended.
//
// It is a var so tests can shorten the wait.
var agentForegroundTimeout = 2 * time.Minute

const agentPrompt = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches a child agent that works independently with its own conversation and uses the current session's model provider and model. By default the child inherits this conversation's finished history as background; set inherit_context to false when it should start with no history at all.

When NOT to use the Agent tool:
- If you want to read a specific file path, use the Read tool instead of the Agent tool, to find the match more quickly.
- If you are searching for a specific definition, use the Grep tool instead, to find the match more quickly.
- If you are searching within a specific file or 2-3 files, use the Read tool instead of the Agent tool.

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do.
- Launch multiple agents concurrently whenever possible; to do that, use a single message with multiple tool uses.
- When the agent is done, it will return a single message back to you. The result is not visible to the user. To show the user the result, send a text message back with a concise summary.
- You can optionally run agents in the background with run_in_background. A foreground agent that runs longer than 2 minutes is promoted to a background task: it keeps running, the call returns async_launched, and your turn continues. A background agent's completion arrives as a task-notification, so do NOT sleep, poll, or proactively check its progress. Use TaskOutput to wait for or inspect a task, and TaskStop to cancel it.
- Use SendMessage with the agentId to steer a live background agent or resume it after completion.
- The agent's outputs should generally be trusted.
- Clearly tell the agent whether you expect it to write code or just to do research, since it is not aware of the user's intent.

## Writing the prompt

The child does not see the user's latest request — that message was addressed to you — so the goal has to come from your directive.
- Say what to do, where, and what "done" looks like. Include file paths and line numbers rather than delegating understanding with "based on your findings, fix the bug".
- Describe what you already learned or ruled out when it changes the approach.
- With the default inherit_context the child can see this conversation's finished turns; with inherit_context:false it starts empty and needs the full background restated.
- If you need a short response, say so ("report in under 200 words"). Lookups want the exact command; investigations want the question, because prescribed steps become dead weight when the premise is wrong.

Terse command-style prompts produce shallow, generic work.`

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
			"prompt":            map[string]any{"type": "string", "description": "The directive for the agent to carry out; it never sees the user's latest request."},
			"inherit_context":   map[string]any{"type": "boolean", "description": "Give the child this conversation's finished history as background. Defaults to true; set false to start the child with a clean context."},
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
		InheritContext:  agentBoolArg(args, "inherit_context", true),
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
		return agentAsyncResult(launch, req, false)
	}
	// Foreground: bound the wait, then promote to background. The child is never
	// cancelled by promotion; it keeps running and reports through the task store.
	waitCtx, cancelWait := context.WithTimeout(ctx, agentForegroundTimeout)
	defer cancelWait()
	snapshot, waitErr := t.runtime.Wait(waitCtx, launch.TaskID)
	switch {
	case errors.Is(waitErr, context.DeadlineExceeded):
		current, ok := t.runtime.Get(launch.TaskID)
		if !ok {
			return errRes(fmt.Sprintf("agent %s is no longer available", launch.TaskID))
		}
		snapshot = current
		if !isTerminal(snapshot.Status) {
			// Mark the task backgrounded so its completion notifies the parent
			// instead of being dropped with the foreground wait.
			if _, err := t.runtime.Background(launch.TaskID); err == nil {
				return agentAsyncResult(launch, req, true)
			}
			// Raced to completion between Get and Background: report it inline.
			if current, ok := t.runtime.Get(launch.TaskID); ok {
				snapshot = current
			}
		}
	case errors.Is(waitErr, context.Canceled):
		// Why: the child does not inherit the parent turn's cancellation, so an
		// aborted parent must stop it explicitly or it leaks as an orphan.
		_, _ = t.runtime.Stop(launch.TaskID)
		return errRes(fmt.Sprintf("agent %s aborted", launch.TaskID))
	case waitErr != nil:
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

// agentAsyncResult is the shared result for run_in_background and for a
// foreground call promoted to background on timeout.
//
// promoted distinguishes the two: only an explicit run_in_background call
// terminates the parent turn (the caller chose async, so the queued completion
// notification is what the session runs next). A promoted call keeps its turn —
// the child keeps running either way, and both shapes carry the note that tells
// the caller what to do with the rest of the turn.
func agentAsyncResult(launch AgentLaunch, req AgentRequest, promoted bool) loop.ToolResult {
	res := jsonResult(map[string]any{
		"status": "async_launched", "agentId": launch.TaskID,
		"description": req.Description, "prompt": req.Prompt,
		"outputFile": launch.OutputFile, "canReadOutputFile": true,
		"note": agentAsyncNote(promoted),
	})
	res.Terminate = !promoted
	return res
}

// agentAsyncNote is the model-facing guidance that travels with every async
// result. Claude Code attaches the same points to its own async result instead
// of leaving them in the tool description alone: the agent is detached, its
// result arrives on its own, and the caller must not poll it or redo its work.
// Which of those matters most is what differs between the two shapes.
func agentAsyncNote(promoted bool) string {
	if promoted {
		return fmt.Sprintf("The %s foreground wait expired, so this agent keeps running in the background and this turn is yours to continue; do not start it again. A <task-notification> with its result arrives automatically, so work on non-overlapping tasks or wait for it with TaskOutput (block=true) instead of polling.", agentForegroundTimeout)
	}
	return "The agent is running in the background; this turn ends here, and its <task-notification> starts the next one automatically. Do not duplicate its work or poll it: check progress with TaskOutput (block=false) or by reading outputFile."
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
