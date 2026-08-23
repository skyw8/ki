package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ki/internal/loop"
)

// bashWaitDelay bounds Wait after we kill the process group. CommandContext
// only SIGKILLs the bash PID; grandchildren (find, pipelines) keep the stdout
// pipe open, and with WaitDelay==0 Wait hangs until they exit — abort and the
// default 120s timeout then look like a no-op.
// See docs/postmortem/2026-08-18-bash-abort.md.
const bashWaitDelay = 200 * time.Millisecond

type bashTool struct {
	cwd  string
	jobs *JobStore
}

func (bashTool) Name() string { return "Bash" }
func (bashTool) Description() string {
	return "Run shell command"
}
func (bashTool) Snippet() string { return "Execute bash commands (ls, grep, find, etc.)" }
func (bashTool) Prompt() string {
	return `Executes a given bash command and returns its output.

Each call starts in the session cwd. 'cd' only affects the current call and is not remembered. Use 'cd <dir> && <command>' when needed. The shell environment is initialized from the user's profile (bash or zsh).

IMPORTANT: Avoid using this tool to run cat, head, tail, sed, awk, or echo commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool.

You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). By default, your command will timeout after 120000ms (2 minutes). A long-running foreground command may continue in the background when this waiting timeout expires; use TaskOutput to wait or inspect it and TaskStop to terminate it.

You can use the run_in_background parameter to run the command in the background. Only use this if you don't need the result immediately.

When issuing multiple commands:
- If the commands are independent and can run in parallel, make multiple Bash tool calls in a single message.
- If the commands depend on each other and must run sequentially, use a single Bash call with '&&' to chain them together.
- DO NOT use newlines to separate commands (newlines are ok in quoted strings).`
}
func (bashTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"command"},
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": "The command to execute"},
			"timeout":           map[string]any{"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
			"description":       map[string]any{"type": "string", "description": "Clear, concise description of what this command does in active voice."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Set to true to run this command in the background. Use Read to read the output later."},
		},
	}
}

func (t bashTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t bashTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.execute(ctx, args, nil)
}

// ExecuteWithProgress lets the loop forward throttled stdout/stderr updates
// without changing the ordinary Tool interface used by other tools.
func (t bashTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	return t.execute(ctx, args, emit)
}

func (t bashTool) execute(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		return errRes("command is required")
	}
	timeout := 120000
	if v, ok := asInt(args["timeout"]); ok && v > 0 {
		timeout = min(v, 600000)
	}
	bg := false
	if v, ok := args["run_in_background"].(bool); ok {
		bg = v
	}
	if bg {
		if t.jobs == nil {
			t.jobs = NewJobStore()
		}
		id, path, err := t.jobs.Start(ctx, t.cwd, cmdStr, stringArg(args, "description", cmdStr), "local_bash")
		if err != nil {
			return errRes(err.Error())
		}
		return okRes(fmt.Sprintf("started background command %s\noutput_file: %s\nUse TaskOutput with task_id=%s to inspect it, or Read with file_path=%s.", id, path, id, path))
	}
	if t.jobs == nil {
		t.jobs = NewJobStore()
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	var progress func(TaskUpdate)
	if emit != nil {
		progress = func(update TaskUpdate) { emit(update) }
	}
	snapshot, runErr := t.jobs.RunForeground(cctx, t.cwd, cmdStr, stringArg(args, "description", cmdStr), progress)
	out, _ := t.jobs.Output(snapshot.TaskID)
	text, note := truncateTail(out)
	text += note
	if errors.Is(runErr, context.DeadlineExceeded) && snapshot.Status == TaskBackground {
		if strings.HasPrefix(strings.TrimSpace(cmdStr), "sleep") {
			_, _ = t.jobs.Stop(snapshot.TaskID)
			return errRes(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds", timeout/1000)))
		}
		return okRes(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds and continues in background as %s\noutput_file: %s\nUse TaskOutput with task_id=%s to inspect it, or TaskStop to terminate it.", timeout/1000, snapshot.TaskID, snapshot.OutputFile, snapshot.TaskID)))
	}
	if errors.Is(runErr, context.Canceled) || snapshot.Status == TaskKilled {
		return errRes(appendStatus(text, "Command aborted"))
	}
	if runErr != nil {
		return errRes(appendStatus(text, runErr.Error()))
	}
	if snapshot.Status == TaskFailed {
		if snapshot.ExitCode != nil {
			return errRes(appendStatus(text, fmt.Sprintf("Command exited with code %d", *snapshot.ExitCode)))
		}
		return errRes(appendStatus(text, snapshot.Error))
	}
	if text == "" {
		text = "(no output)"
	}
	return okRes(text)
}
