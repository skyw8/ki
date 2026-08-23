//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestPowerShellExecutionAndCwdReset(t *testing.T) {
	cwd := t.TempDir()
	tool := powerShellTool{cwd: cwd, jobs: NewJobStore(), shell: installedPowerShell(t)}
	defer tool.jobs.Close()

	result := tool.Execute(context.Background(), map[string]any{"command": "Write-Output 'hello-ki'"})
	if result.IsError || !strings.Contains(result.Content[0].Text, "hello-ki") {
		t.Fatalf("stdout = %+v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{"command": "cmd.exe /c exit 7"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "code 7") {
		t.Fatalf("native exit = %+v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{"command": "Get-Item -LiteralPath '.ki-does-not-exist' -ErrorAction SilentlyContinue"})
	if !result.IsError {
		t.Fatalf("cmdlet failure = %+v", result)
	}

	_ = os.Mkdir(filepath.Join(cwd, "sub"), 0o700)
	_ = tool.Execute(context.Background(), map[string]any{"command": "Set-Location 'sub'; (Get-Location).Path"})
	result = tool.Execute(context.Background(), map[string]any{"command": "(Get-Location).Path"})
	if result.IsError || !strings.EqualFold(strings.TrimSpace(result.Content[0].Text), cwd) {
		t.Fatalf("cwd was retained: %+v want %s", result, cwd)
	}
}

func TestPowerShellBackgroundLifecycle(t *testing.T) {
	jobs := NewJobStore()
	defer jobs.Close()
	tool := powerShellTool{cwd: t.TempDir(), jobs: jobs, shell: installedPowerShell(t)}
	started := tool.Execute(context.Background(), map[string]any{
		"command": "Write-Output 'one'; Start-Sleep -Milliseconds 50; Write-Output 'two'", "run_in_background": true,
	})
	if started.IsError {
		t.Fatalf("start = %+v", started)
	}
	id := strings.Fields(strings.Split(started.Content[0].Text, "\n")[0])[3]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snapshot, err := jobs.Wait(ctx, id)
	if err != nil || snapshot.Status != TaskCompleted {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	output, _ := jobs.Output(id)
	if !strings.Contains(output, "one") || !strings.Contains(output, "two") {
		t.Fatalf("output = %q", output)
	}
}
