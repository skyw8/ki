package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ki/internal/loop"
	"ki/internal/telemetry"
)

type execCommandTool struct {
	cwd       string
	processes *ShellProcessManager
	shells    ShellRuntime
	pathDirs  []string
}

func (execCommandTool) Name() string { return "exec_command" }
func (execCommandTool) Description() string {
	return "Run a shell command and return output or a terminal session_id for continued interaction."
}
func (execCommandTool) Snippet() string { return "Execute a command" }
func (t execCommandTool) Prompt() string {
	shell, err := resolveExecShell(t.shells, "", true)
	name := "unavailable"
	edition := ""
	if err == nil {
		name = shell.path
		if shell.kind == shellPowerShell {
			edition = " Use PowerShell syntax; native exit codes are preserved."
			if shell.powerShellEdition == powerShellDesktop {
				edition += " Windows PowerShell 5.1: do not use &&, || or PowerShell 7 syntax."
			}
		}
	}
	return fmt.Sprintf("Run commands with the default shell %s.%s Use workdir for the working directory. Each invocation starts an independent process; cd is not persistent. yield_time_ms only controls how long to observe before returning; a running command continues under its session_id. Use write_stdin to observe, write to tty=true stdin, or request Ctrl-C. Interruption of the agent turn does not terminate owned processes. Independent commands may run in parallel. Avoid sleep/poll loops when write_stdin can wait. Tool names accept snake_case and PascalCase; prefer snake_case.", name, edition)
}
func (execCommandTool) Parameters() map[string]any {
	return objectSchema([]any{"cmd"}, map[string]any{
		"cmd":               map[string]any{"type": "string", "description": "Shell command to execute."},
		"workdir":           map[string]any{"type": "string", "description": "Working directory; relative to current turn cwd."},
		"shell":             map[string]any{"type": "string", "description": "Shell executable; defaults to the resolved session shell."},
		"login":             map[string]any{"type": "boolean", "description": "Use login/profile behavior. Defaults to true."},
		"tty":               map[string]any{"type": "boolean", "description": "Allocate a PTY/ConPTY. Defaults to false (pipes)."},
		"yield_time_ms":     map[string]any{"type": "integer", "minimum": 250, "maximum": 30000, "description": "Observation budget; defaults to 10000ms. Does not terminate the process."},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 1, "description": "Output budget; defaults to 10000 tokens."},
	})
}
func (t execCommandTool) Validate(args map[string]any) error {
	if err := validateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	if strings.TrimSpace(stringArg(args, "cmd", "")) == "" {
		return fmt.Errorf("cmd is required")
	}
	return nil
}
func (t execCommandTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.ExecuteWithProgress(ctx, args, nil)
}
func (t execCommandTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	if err := t.Validate(args); err != nil {
		return errRes(err.Error())
	}
	cwd := stringArg(args, "workdir", t.cwd)
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(t.cwd, cwd)
	}
	var err error
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return errRes(err.Error())
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return errRes(err.Error())
	}
	if !info.IsDir() {
		return errRes("workdir must be a directory")
	}
	shell, err := resolveExecShell(t.shells, stringArg(args, "shell", ""), boolDefault(args, "login", true))
	if err != nil {
		return errRes(err.Error())
	}
	shell.pathDirs = t.pathDirs
	id, err := t.processes.Start(ctx, shell, cwd, stringArg(args, "cmd", ""), boolDefault(args, "tty", false))
	if err != nil {
		return errRes(err.Error())
	}
	snapshot, err := t.processes.Interact(ctx, id, "", time.Duration(intArg(args, "yield_time_ms", 10000))*time.Millisecond, intArg(args, "max_output_tokens", 10000), processEmit(emit))
	return processResult(snapshot, err)
}

type writeStdinTool struct{ processes *ShellProcessManager }

func (writeStdinTool) Name() string { return "write_stdin" }
func (writeStdinTool) Description() string {
	return "Write to or observe an existing terminal session."
}
func (writeStdinTool) Snippet() string { return "Continue terminal interaction" }
func (writeStdinTool) Prompt() string {
	return "Use the integer session_id returned by exec_command. Empty chars collects incremental output without writing; non-empty chars writes to a tty=true terminal. chars=\"\\u0003\" requests Ctrl-C, which does not guarantee exit. A canceled observation leaves the process running. Terminal handles are separate from agent task paths."
}
func (writeStdinTool) Parameters() map[string]any {
	return objectSchema([]any{"session_id"}, map[string]any{
		"session_id":        map[string]any{"type": "integer", "minimum": 1, "description": "Terminal handle returned by exec_command."},
		"chars":             map[string]any{"type": "string", "description": "Input; defaults to empty for output collection."},
		"yield_time_ms":     map[string]any{"type": "integer", "minimum": 0, "maximum": 300000, "description": "Empty input: 5000-300000ms (default 5000); non-empty input: 250-30000ms (default 250)."},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 1, "description": "Output budget; defaults to 10000 tokens."},
	})
}
func (t writeStdinTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}
func (t writeStdinTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.ExecuteWithProgress(ctx, args, nil)
}
func (t writeStdinTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	if err := t.Validate(args); err != nil {
		return errRes(err.Error())
	}
	chars := stringArg(args, "chars", "")
	def, min, max := 5000, 5000, 300000
	if chars != "" {
		def, min, max = 250, 250, 30000
	}
	yield := intArg(args, "yield_time_ms", def)
	if yield < min {
		yield = min
	}
	if yield > max {
		yield = max
	}
	snapshot, err := t.processes.Interact(ctx, int64(intArg(args, "session_id", 0)), chars, time.Duration(yield)*time.Millisecond, intArg(args, "max_output_tokens", 10000), processEmit(emit))
	return processResult(snapshot, err)
}
func objectSchema(required []any, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}
func processEmit(emit func(any)) func(ProcessUpdate) {
	if emit == nil {
		return nil
	}
	return func(update ProcessUpdate) { emit(update) }
}
func processResult(snapshot ProcessSnapshot, err error) loop.ToolResult {
	b, encodeErr := json.Marshal(snapshot)
	if encodeErr != nil {
		return errRes(encodeErr.Error())
	}
	result := okRes(string(b))
	result.Details = snapshot
	if err != nil {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "process_interaction", FaultDomain: "harness"}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Diagnostic = telemetry.ToolDiagnostic{Status: "cancelled", Kind: "observer_cancelled", FaultDomain: "cancellation"}
		}
		result.Content = append(result.Content, errRes(err.Error()).Content...)
	} else if snapshot.ExitCode != nil && *snapshot.ExitCode != 0 {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "completed", Kind: "command_nonzero", FaultDomain: "external_command"}
	} else if snapshot.Error != "" {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "process_io", FaultDomain: "harness"}
	}
	return result
}
