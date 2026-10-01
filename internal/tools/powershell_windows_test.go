//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func installedPowerShell(t *testing.T) shellSpec {
	t.Helper()
	if path, err := exec.LookPath("pwsh"); err == nil {
		return shellSpec{kind: shellPowerShell, path: path, powerShellEdition: powerShellCore}
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return shellSpec{kind: shellPowerShell, path: path, powerShellEdition: powerShellDesktop}
	}
	t.Skip("PowerShell is not installed")
	return shellSpec{}
}

// resolveTestPath returns the long, symlink-free form of a test directory.
func resolveTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestPowerShellExecutionAndCwdReset(t *testing.T) {
	// Why: t.TempDir can hand out a short-name %TEMP% path (RUNNER~1) while
	// PowerShell reports the long form of its cwd; compare like for like.
	cwd := resolveTestPath(t, t.TempDir())
	shell := installedPowerShell(t)
	manager := NewShellProcessManager()
	defer manager.Close()
	tool := execCommandTool{cwd: cwd, processes: manager, shells: ShellRuntime{powerShell: &shell}}

	result := tool.Execute(context.Background(), map[string]any{"cmd": "Write-Output 'hello-ki'"})
	if result.IsError || !strings.Contains(result.Content[0].Text, "hello-ki") {
		t.Fatalf("stdout = %+v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{"cmd": "cmd.exe /c exit 7"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, `"exit_code":7`) {
		t.Fatalf("native exit = %+v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{"cmd": "Get-Item -LiteralPath '.ki-does-not-exist' -ErrorAction SilentlyContinue"})
	if !result.IsError {
		t.Fatalf("cmdlet failure = %+v", result)
	}

	_ = os.Mkdir(filepath.Join(cwd, "sub"), 0o700)
	_ = tool.Execute(context.Background(), map[string]any{"cmd": "Set-Location 'sub'; (Get-Location).Path"})
	result = tool.Execute(context.Background(), map[string]any{"cmd": "(Get-Location).Path"})
	if result.IsError || !strings.EqualFold(strings.TrimSpace(execSnapshot(t, result).Output), cwd) {
		t.Fatalf("cwd was retained: %+v want %s", result, cwd)
	}
}

func TestPowerShellYieldLifecycle(t *testing.T) {
	shell := installedPowerShell(t)
	manager := NewShellProcessManager()
	defer manager.Close()
	tool := execCommandTool{cwd: t.TempDir(), processes: manager, shells: ShellRuntime{powerShell: &shell}}
	first := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "Write-Output 'one'; Start-Sleep -Milliseconds 600; Write-Output 'two'", "yield_time_ms": 250}))
	result := (writeStdinTool{processes: manager}).Execute(t.Context(), map[string]any{"session_id": first.SessionID, "yield_time_ms": 5000})
	end := execSnapshot(t, result)
	if result.IsError || end.Status != "exited" || !strings.Contains(first.Output+end.Output, "one") || !strings.Contains(first.Output+end.Output, "two") {
		t.Fatalf("process: %+v %+v", first, result)
	}
}
