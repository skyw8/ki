package tools

import (
	"context"

	"ki/internal/loop"
)

type bashTool struct {
	cwd   string
	jobs  *JobStore
	shell shellSpec
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
	return shellParameters("The command to execute")
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
	shell := t.shell
	if !shell.available() {
		shell = fallbackShellRuntime().bash
	}
	return executeShell(ctx, args, emit, shell, t.cwd, t.jobs)
}
