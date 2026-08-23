package tools

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDiscovery(goos string, env, paths map[string]string, files map[string]bool) shellDiscovery {
	return shellDiscovery{
		goos:   goos,
		getenv: func(name string) string { return env[name] },
		lookPath: func(name string) (string, error) {
			if path := paths[name]; path != "" {
				return path, nil
			}
			return "", errors.New("not found")
		},
		exists: func(path string) bool { return files[filepath.Clean(path)] },
	}
}

func TestDiscoverShellRuntimeNonWindowsDoesNotExposePowerShell(t *testing.T) {
	got := discoverShellRuntime(fakeDiscovery("linux", nil, map[string]string{
		"bash": "/usr/local/bin/bash", "pwsh": "/usr/bin/pwsh",
	}, map[string]bool{"/usr/local/bin/bash": true}))
	if got.bash.path != "/usr/local/bin/bash" || got.powerShell != nil {
		t.Fatalf("runtime = %+v", got)
	}
}

func TestFindWindowsBashEnvironmentPriority(t *testing.T) {
	root := t.TempDir()
	kiPath := filepath.Join(root, "ki", "bash.exe")
	claudePath := filepath.Join(root, "claude", "bash.exe")
	d := fakeDiscovery("windows", map[string]string{
		"KI_GIT_BASH_PATH":          kiPath,
		"CLAUDE_CODE_GIT_BASH_PATH": claudePath,
	}, nil, map[string]bool{kiPath: true, claudePath: true})
	got := findWindowsBash(d)
	if got != kiPath {
		t.Fatalf("path = %q", got)
	}
}

func TestFindWindowsBashUsesClaudeCompatibilityVariable(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, "claude", "bash.exe")
	d := fakeDiscovery("windows", map[string]string{
		"CLAUDE_CODE_GIT_BASH_PATH": claudePath,
	}, nil, map[string]bool{claudePath: true})
	got := findWindowsBash(d)
	if got != claudePath {
		t.Fatalf("path = %q", got)
	}
}

func TestFindWindowsBashInvalidExplicitPathFallsBack(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing", "bash.exe")
	valid := filepath.Join(root, "valid", "bash.exe")
	d := fakeDiscovery("windows", map[string]string{
		"KI_GIT_BASH_PATH":          missing,
		"CLAUDE_CODE_GIT_BASH_PATH": valid,
	}, nil, map[string]bool{valid: true})
	if got := findWindowsBash(d); got != valid {
		t.Fatalf("path = %q want %q", got, valid)
	}
}

func TestFindWindowsBashFromProgramFiles(t *testing.T) {
	root := t.TempDir()
	bashPath := filepath.Join(root, "Git", "bin", "bash.exe")
	d := fakeDiscovery("windows", map[string]string{"ProgramFiles": root}, nil, map[string]bool{bashPath: true})
	if got := findWindowsBash(d); got != bashPath {
		t.Fatalf("path = %q want %q", got, bashPath)
	}
}

func TestFindWindowsBashFromPath(t *testing.T) {
	root := t.TempDir()
	bashPath := filepath.Join(root, "MSYS2", "bash.exe")
	d := fakeDiscovery("windows", nil, map[string]string{"bash.exe": bashPath}, map[string]bool{bashPath: true})
	if got := findWindowsBash(d); got != bashPath {
		t.Fatalf("path = %q want %q", got, bashPath)
	}
}

func TestFindWindowsBashMissingReturnsEmpty(t *testing.T) {
	if got := findWindowsBash(fakeDiscovery("windows", nil, nil, nil)); got != "" {
		t.Fatalf("path = %q", got)
	}
}

func TestDiscoverPowerShellPrefersPwshThenDesktop(t *testing.T) {
	root := t.TempDir()
	bashPath := filepath.Join(root, "bash.exe")
	baseEnv := map[string]string{"KI_GIT_BASH_PATH": bashPath}
	files := map[string]bool{bashPath: true}

	core := discoverShellRuntime(fakeDiscovery("windows", baseEnv, map[string]string{
		"pwsh": filepath.Join(root, "pwsh.exe"), "powershell": filepath.Join(root, "powershell.exe"),
	}, files))
	if core.powerShell == nil || core.powerShell.powerShellEdition != powerShellCore {
		t.Fatalf("core runtime = %+v", core)
	}
	desktop := discoverShellRuntime(fakeDiscovery("windows", baseEnv, map[string]string{
		"powershell": filepath.Join(root, "powershell.exe"),
	}, files))
	if desktop.powerShell == nil || desktop.powerShell.powerShellEdition != powerShellDesktop {
		t.Fatalf("desktop runtime = %+v", desktop)
	}
}

func TestDiscoverWindowsKeepsUnavailablePowerShellVisible(t *testing.T) {
	root := t.TempDir()
	bashPath := filepath.Join(root, "bash.exe")
	got := discoverShellRuntime(fakeDiscovery("windows", map[string]string{"KI_GIT_BASH_PATH": bashPath}, nil, map[string]bool{bashPath: true}))
	if got.powerShell == nil || got.powerShell.available() {
		t.Fatalf("runtime = %+v", got)
	}
}

func TestPowerShellArgumentsAndSleepDetection(t *testing.T) {
	args := (shellSpec{kind: shellPowerShell, path: "pwsh"}).args("Write-Output 'ok'")
	if len(args) != 4 || args[0] != "-NoProfile" || args[1] != "-NonInteractive" || args[2] != "-Command" {
		t.Fatalf("args = %#v", args)
	}
	if !strings.Contains(args[3], "$LASTEXITCODE") || !strings.Contains(args[3], "elseif ($?)") {
		t.Fatalf("exit wrapper = %s", args[3])
	}
	for _, command := range []string{"Start-Sleep 10", "sleep -Seconds 10", "  Start-Sleep -Milliseconds 10; Write-Output ok"} {
		if !isLeadingSleep(shellPowerShell, command) {
			t.Fatalf("did not detect %q", command)
		}
	}
	if isLeadingSleep(shellPowerShell, "Write-Output ok; Start-Sleep 10") {
		t.Fatal("later sleep was treated as a leading sleep")
	}
}
