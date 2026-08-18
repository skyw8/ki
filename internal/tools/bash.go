package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"ki/internal/loop"
)

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

The working directory persists between commands, but shell state does not. The shell environment is initialized from the user's profile (bash or zsh).

IMPORTANT: Avoid using this tool to run cat, head, tail, sed, awk, or echo commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool.

You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). By default, your command will timeout after 120000ms (2 minutes).

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
		id, path := t.jobs.Start(ctx, t.cwd, cmdStr)
		return okRes(fmt.Sprintf("started background command %s\noutput_file: %s\nUse Read with file_path=%s to check output.", id, path, path))
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	out, code, err := runBash(cctx, t.cwd, cmdStr)
	text, note := truncateTail(out)
	text += note
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return errRes(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds", timeout/1000)))
	}
	if err != nil && code == 0 {
		return errRes(appendStatus(text, err.Error()))
	}
	if code != 0 {
		return errRes(appendStatus(text, fmt.Sprintf("Command exited with code %d", code)))
	}
	if text == "" {
		text = "(no output)"
	}
	return okRes(text)
}
func runBash(ctx context.Context, cwd, command string) (string, int, error) {
	//nolint:gosec // executing the explicit Bash tool command is the feature contract.
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			err = nil
		}
	}
	return buf.String(), code, err
}
