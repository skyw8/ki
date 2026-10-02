package shelltools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ki/internal/process"
	"ki/internal/telemetry"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
)

type execCommandTool struct {
	cwd       string
	processes *process.Manager
	shells    process.ShellRuntime
	pathDirs  []string
}

func (execCommandTool) Name() string { return catalog.ExecCommand }

func (execCommandTool) Description() string {
	return "Run a shell command and return output or a terminal session_id for continued interaction."
}

func (execCommandTool) Snippet() string { return "Execute a command" }

func (t execCommandTool) Prompt() string {
	shell, err := process.ResolveShell(t.shells, "", true)
	name := "unavailable"
	edition := ""
	if err == nil {
		name = shell.Path()
		if shell.PowerShell() {
			edition = " Use PowerShell syntax; native exit codes are preserved."
			if shell.DesktopPowerShell() {
				edition += " Windows PowerShell 5.1: do not use &&, || or PowerShell 7 syntax."
			}
		}
	}
	return fmt.Sprintf("Run commands with the default shell %s.%s Use workdir for the working directory. Each invocation starts an independent process; cd is not persistent. yield_time_ms only controls how long to observe before returning; a running command continues under its session_id. Use write_stdin to observe, write to tty=true stdin, or request Ctrl-C. Interruption of the agent turn does not terminate owned processes. Independent commands may run in parallel. Avoid sleep/poll loops when write_stdin can wait. Tool names accept snake_case and PascalCase; prefer snake_case.", name, edition)
}

func (execCommandTool) Parameters() map[string]any {
	return support.ObjectSchema([]any{"cmd"}, map[string]any{
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
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	if strings.TrimSpace(support.StringArg(args, "cmd", "")) == "" {
		return fmt.Errorf("cmd is required")
	}
	return validateObservationArgs(args)
}

func validateObservationArgs(args map[string]any) error {
	for _, key := range []string{"yield_time_ms", "max_output_tokens"} {
		value, exists := args[key]
		if !exists {
			continue
		}
		n, ok := support.AsInt(value)
		if !ok {
			return fmt.Errorf("%s must be a representable integer", key)
		}
		if n < 0 {
			return fmt.Errorf("%s must be non-negative", key)
		}
		if key == "max_output_tokens" && n == 0 {
			return fmt.Errorf("max_output_tokens must be positive")
		}
	}
	return nil
}

func (t execCommandTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	return t.ExecuteWithProgress(ctx, args, nil)
}

func (t execCommandTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) toolapi.Result {
	if err := t.Validate(args); err != nil {
		return support.Error(err.Error())
	}
	cwd := support.StringArg(args, "workdir", t.cwd)
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(t.cwd, cwd)
	}
	var err error
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return support.Error(err.Error())
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return support.Error(err.Error())
	}
	if !info.IsDir() {
		return support.Error("workdir must be a directory")
	}
	shell, err := process.ResolveShell(t.shells, support.StringArg(args, "shell", ""), support.BoolDefault(args, "login", true))
	if err != nil {
		return support.Error(err.Error())
	}
	shell = shell.WithPathDirs(t.pathDirs)
	identity := toolapi.ExecutionFromContext(ctx)
	id, err := t.processes.Start(ctx, shell, cwd, support.StringArg(args, "cmd", ""), support.BoolDefault(args, "tty", false), process.Identity{RunID: identity.RunID, CallID: identity.CallID, AgentID: identity.AgentID, Generation: identity.Generation})
	if err != nil {
		return support.Error(err.Error())
	}
	// Clamp before multiplying: the schema is not sufficient to stop untrusted
	// oversized integers from wrapping a duration.
	yield := max(250, min(30000, support.IntArg(args, "yield_time_ms", 10000)))
	snapshot, err := t.processes.Interact(ctx, id, "", time.Duration(yield)*time.Millisecond, support.IntArg(args, "max_output_tokens", 10000), processEmit(emit))
	return processResult(snapshot, err)
}

type writeStdinTool struct{ processes *process.Manager }

func (writeStdinTool) Name() string { return catalog.WriteStdin }

func (writeStdinTool) Description() string {
	return "Write to or observe an existing terminal session."
}

func (writeStdinTool) Snippet() string { return "Continue terminal interaction" }

func (writeStdinTool) Prompt() string {
	return "Use the integer session_id returned by exec_command. Empty chars collects incremental output without writing; non-empty chars writes to a tty=true terminal. chars=\"\\u0003\" requests Ctrl-C, which does not guarantee exit. A canceled observation leaves the process running. Terminal handles are separate from agent task paths."
}

func (writeStdinTool) Parameters() map[string]any {
	return support.ObjectSchema([]any{"session_id"}, map[string]any{
		"session_id":        map[string]any{"type": "integer", "minimum": 1, "maximum": (1 << 53) - 1, "description": "Terminal handle returned by exec_command."},
		"chars":             map[string]any{"type": "string", "description": "Input; defaults to empty for output collection."},
		"yield_time_ms":     map[string]any{"type": "integer", "minimum": 0, "maximum": 300000, "description": "Empty input: 5000-300000ms (default 5000); non-empty input: 250-30000ms (default 250)."},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 1, "description": "Output budget; defaults to 10000 tokens."},
	})
}

func (t writeStdinTool) Validate(args map[string]any) error {
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	id, ok := support.AsInt(args["session_id"])
	if !ok || id < 1 || int64(id) > (1<<53)-1 {
		return fmt.Errorf("session_id must be an integer between 1 and %d", (1<<53)-1)
	}
	return validateObservationArgs(args)
}

func (t writeStdinTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	return t.ExecuteWithProgress(ctx, args, nil)
}

func (t writeStdinTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) toolapi.Result {
	if err := t.Validate(args); err != nil {
		return support.Error(err.Error())
	}
	chars := support.StringArg(args, "chars", "")
	def, min, max := 5000, 5000, 300000
	if chars != "" {
		def, min, max = 250, 250, 30000
	}
	yield := support.IntArg(args, "yield_time_ms", def)
	if yield < min {
		yield = min
	}
	if yield > max {
		yield = max
	}
	snapshot, err := t.processes.Interact(ctx, int64(support.IntArg(args, "session_id", 0)), chars, time.Duration(yield)*time.Millisecond, support.IntArg(args, "max_output_tokens", 10000), processEmit(emit))
	return processResult(snapshot, err)
}

func processEmit(emit func(any)) func(process.Update) {
	if emit == nil {
		return nil
	}
	return func(update process.Update) { emit(update) }
}

func processResult(snapshot process.Snapshot, err error) toolapi.Result {
	b, encodeErr := json.Marshal(snapshot)
	if encodeErr != nil {
		return support.Error(encodeErr.Error())
	}
	result := support.Text(string(b))
	result.Details = snapshot
	if err != nil {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "process_interaction", FaultDomain: "harness"}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Diagnostic = telemetry.ToolDiagnostic{Status: "cancelled", Kind: "observer_cancelled", FaultDomain: "cancellation"}
		}
		result.Content = append(result.Content, support.Error(err.Error()).Content...)
	} else if snapshot.ExitCode != nil && *snapshot.ExitCode != 0 {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "completed", Kind: "command_nonzero", FaultDomain: "external_command"}
	} else if snapshot.Error != "" {
		result.IsError = true
		result.Diagnostic = telemetry.ToolDiagnostic{Status: "failed", Kind: "process_io", FaultDomain: "harness"}
	}
	return result
}
