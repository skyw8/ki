package tools

import (
	"context"
	"fmt"

	"ki/internal/loop"
)

type powerShellTool struct {
	cwd   string
	jobs  *JobStore
	shell shellSpec
}

func (powerShellTool) Name() string        { return "PowerShell" }
func (powerShellTool) Description() string { return "Run PowerShell command" }
func (powerShellTool) Snippet() string     { return "Execute Windows PowerShell commands" }
func (t powerShellTool) Prompt() string {
	edition := `PowerShell edition: unknown — assume Windows PowerShell 5.1 for compatibility.
- Do not use &&, ||, ternary ?:, null-coalescing ??, or null-conditional ?..
- Chain conditionally with: A; if ($?) { B }. Chain unconditionally with: A; B.`
	switch t.shell.powerShellEdition {
	case powerShellCore:
		edition = `PowerShell edition: PowerShell 7+ (pwsh).
- Pipeline chain operators && and || are available. Prefer A && B when B requires A to succeed.
- Ternary, null-coalescing, and null-conditional operators are available.
- The default file encoding is UTF-8 without BOM.`
	case powerShellDesktop:
		edition = `PowerShell edition: Windows PowerShell 5.1 (powershell.exe).
- Pipeline chain operators && and || are not available. Use A; if ($?) { B } for conditional chaining.
- Ternary, null-coalescing, and null-conditional operators are not available.
- Out-File and Set-Content default to UTF-16 LE; pass -Encoding utf8 for files used by other tools.`
	}
	return fmt.Sprintf(`Executes a given PowerShell command and returns its output.

Each call starts in the session cwd. Set-Location only affects the current call and is not remembered. The process runs with -NoProfile and -NonInteractive.

IMPORTANT: Use the dedicated Read, Write, Edit, Grep, and Glob tools for file operations and searching instead of Get-Content, Set-Content, Select-String, or recursive Get-ChildItem.

%s

PowerShell syntax:
- Variables use the $ prefix and environment variables use $env:NAME.
- The escape character is backtick, not backslash.
- Pipelines pass objects. Use Select-Object, Where-Object, and ForEach-Object.
- Quote paths containing spaces. Invoke an executable path containing spaces with &: & "C:\Program Files\App\app.exe".
- Registry paths use HKLM:\ and HKCU:\ PSDrive prefixes.
- Never use Read-Host, Get-Credential, Out-GridView, pause, or commands that open an interactive editor.
- For literal multiline native arguments, use a single-quoted here-string whose closing '@ starts at column 0.

You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). The default is 120000ms / 2 minutes. A long-running foreground command may continue in the background when this waiting timeout expires; use TaskOutput to inspect it and TaskStop to terminate it.

Use run_in_background when the result is not needed immediately. Avoid unnecessary Start-Sleep commands and do not poll a background task; TaskOutput can wait for completion.

When issuing multiple commands:
- Make multiple PowerShell calls for independent commands.
- Chain dependent commands in one call using syntax supported by the edition above.
- Do not use newlines to separate commands except inside quoted strings or here-strings.
- Do not prefix commands with Set-Location; the working directory is already the session cwd.`, edition)
}
func (powerShellTool) Parameters() map[string]any {
	return shellParameters("The PowerShell command to execute")
}
func (t powerShellTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}
func (t powerShellTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.execute(ctx, args, nil)
}
func (t powerShellTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	return t.execute(ctx, args, emit)
}
func (t powerShellTool) execute(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	return executeShell(ctx, args, emit, t.shell, t.cwd, t.jobs)
}
