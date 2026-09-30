package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ki/internal/loop"
	"ki/internal/telemetry"
	"ki/internal/types"
)

func shellParameters(commandDescription string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"command"},
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": commandDescription},
			"timeout":           map[string]any{"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
			"description":       map[string]any{"type": "string", "description": "Clear, concise description of what this command does in active voice."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Set to true to run this command in the background. Use TaskOutput to inspect the output later."},
		},
	}
}

func executeShell(ctx context.Context, args map[string]any, emit func(any), shell shellSpec, cwd string, jobs *JobStore) loop.ToolResult {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		return errRes("command is required")
	}
	if !shell.available() {
		if shell.kind == shellPowerShell {
			return okRes("PowerShell is not available on this system.")
		}
		return errRes("Bash is not available on this system.")
	}
	timeout := 30000
	if v, ok := asInt(args["timeout"]); ok && v > 0 {
		timeout = min(v, 600000)
	}
	bg, _ := args["run_in_background"].(bool)
	if jobs == nil {
		jobs = NewJobStore()
	}
	if bg {
		id, path, err := jobs.Start(ctx, shell, cwd, cmdStr, stringArg(args, "description", cmdStr), "local_bash")
		if err != nil {
			return errRes(err.Error())
		}
		snapshot, _ := jobs.Get(id)
		return shellToolResult(fmt.Sprintf("started background command %s\noutput_file: %s\nUse TaskOutput with task_id=%s to inspect it, or Read with file_path=%s.", id, path, id, path), false, detailsForTask(snapshot, "success"))
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	var progress func(TaskUpdate)
	if emit != nil {
		progress = func(update TaskUpdate) { emit(update) }
	}
	snapshot, runErr := jobs.RunForeground(cctx, shell, cwd, cmdStr, stringArg(args, "description", cmdStr), progress)
	details := detailsForTask(snapshot, "success")
	text, note := truncateTaskTail(snapshot.Output, snapshot.Bytes, snapshot.Lines, snapshot.OutputFile)
	text += note
	if errors.Is(runErr, context.DeadlineExceeded) && snapshot.Status == TaskBackground {
		details.TimedOut = true
		if isLeadingSleep(shell.kind, cmdStr) {
			_, _ = jobs.Stop(snapshot.TaskID)
			details.Status = TaskKilled
			return shellToolResult(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds", timeout/1000)), true, details)
		}
		return shellToolResult(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds and continues in background as %s\noutput_file: %s\nUse TaskOutput with task_id=%s to inspect it, or TaskStop to terminate it.", timeout/1000, snapshot.TaskID, snapshot.OutputFile, snapshot.TaskID)), false, details)
	}
	if errors.Is(runErr, context.Canceled) || snapshot.Status == TaskKilled {
		details.Cancelled = true
		return shellToolResult(appendStatus(text, "Command aborted"), true, details)
	}
	if runErr != nil {
		return shellToolResult(appendStatus(text, runErr.Error()), true, details)
	}
	if snapshot.Status == TaskFailed {
		if snapshot.ExitCode != nil {
			return shellToolResult(appendStatus(text, fmt.Sprintf("Command exited with code %d", *snapshot.ExitCode)), true, details)
		}
		return shellToolResult(appendStatus(text, snapshot.Error), true, details)
	}
	if text == "" {
		text = "(no output)"
	}
	return shellToolResult(text, false, details)
}

func shellToolResult(text string, isError bool, details taskDetails) loop.ToolResult {
	diagnostic := telemetry.ToolDiagnostic{Status: "completed", Kind: "success", FaultDomain: "none"}
	switch {
	case details.Cancelled:
		diagnostic = telemetry.ToolDiagnostic{Status: "cancelled", Kind: "cancelled_by_parent", FaultDomain: "cancellation"}
	case details.TimedOut:
		diagnostic = telemetry.ToolDiagnostic{Status: "timed_out", Kind: "timeout", FaultDomain: "environment", Retryable: true}
	case details.ExitCode != nil && *details.ExitCode != 0:
		// The shell and process ran correctly; the invoked command reported a
		// negative outcome. Keep IsError for the model, but do not blame Ki.
		diagnostic = telemetry.ToolDiagnostic{Status: "completed", Kind: "command_nonzero", FaultDomain: "external_command"}
	case isError:
		diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "spawn_or_io_failed", FaultDomain: "environment", Retryable: true}
	}
	return loop.ToolResult{
		Content: []types.Content{{Type: "text", Text: text}}, IsError: isError,
		Details: details, Diagnostic: diagnostic,
	}
}

func isLeadingSleep(kind shellKind, command string) bool {
	trimmed := strings.TrimSpace(command)
	if kind == shellBash {
		return strings.HasPrefix(trimmed, "sleep")
	}
	statements := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\r' || r == '\n'
	})
	if len(statements) == 0 {
		return false
	}
	fields := strings.Fields(statements[0])
	if len(fields) == 0 {
		return false
	}
	name := strings.ToLower(fields[0])
	return name == "start-sleep" || name == "sleep"
}
