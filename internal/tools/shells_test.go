package tools

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ki/internal/search"
)

var errShellNotFound = errors.New("not found")

func fakeDiscovery(goos string, env, paths map[string]string, files map[string]bool) shellDiscovery {
	return shellDiscovery{
		goos:   goos,
		getenv: func(name string) string { return env[name] },
		lookPath: func(name string) (string, error) {
			if path := paths[name]; path != "" {
				return path, nil
			}
			return "", errShellNotFound
		},
		exists: func(path string) bool { return files[filepath.Clean(path)] },
	}
}

func TestDiscoverShellRuntimeNonWindowsDoesNotExposePowerShell(t *testing.T) {
	got := discoverShellRuntime(fakeDiscovery("linux", nil, map[string]string{ //nolint:gosec // fake shell paths are test fixtures, not credentials
		"bash": "/usr/local/bin/bash", "pwsh": "/usr/bin/pwsh",
	}, map[string]bool{"/usr/local/bin/bash": true}))
	if got.bash.path != "/usr/local/bin/bash" || got.powerShell != nil {
		t.Fatalf("runtime = %+v", got)
	}
}

func TestShellEnvironmentCarriesProxyVariables(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8443")
	t.Setenv("NO_PROXY", "localhost")

	env := (shellSpec{kind: shellBash, path: "/bin/bash"}).env()
	values := make(map[string]string)
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	for key, want := range map[string]string{
		"HTTP_PROXY":  "http://proxy.example:8080",
		"HTTPS_PROXY": "http://proxy.example:8443",
		"NO_PROXY":    "localhost",
	} {
		if values[key] != want {
			t.Fatalf("%s = %q, want %q", key, values[key], want)
		}
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

func envToMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	return values
}

func TestBundledSearchToolsPrependPathAndSetBashEnv(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	values := envToMap(withBundledSearchTools([]string{"PATH=/usr/bin:/bin", "HOME=/home/x"}, dir, shellBash))

	if got, want := values["PATH"], dir+string(os.PathListSeparator)+"/usr/bin:/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if got, want := values["BASH_ENV"], shellPath(filepath.Join(dir, search.ToolsShimName)); got != want {
		t.Fatalf("BASH_ENV = %q, want %q", got, want)
	}
	if _, ok := values["KI_ORIG_BASH_ENV"]; ok {
		t.Fatalf("unexpected KI_ORIG_BASH_ENV = %q", values["KI_ORIG_BASH_ENV"])
	}
}

func TestBundledSearchToolsChainsExistingBashEnv(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	values := envToMap(withBundledSearchTools(
		[]string{"PATH=/usr/bin", "BASH_ENV=/home/x/.user-env.sh"}, dir, shellBash))

	if got, want := values["KI_ORIG_BASH_ENV"], "/home/x/.user-env.sh"; got != want {
		t.Fatalf("KI_ORIG_BASH_ENV = %q, want %q", got, want)
	}
	if values["BASH_ENV"] == "/home/x/.user-env.sh" {
		t.Fatal("existing BASH_ENV was not replaced by the shim")
	}
}

func TestBundledSearchToolsDoesNotSetBashEnvForPowerShell(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	values := envToMap(withBundledSearchTools([]string{"PATH=C:\\Windows"}, dir, shellPowerShell))

	if _, ok := values["BASH_ENV"]; ok {
		t.Fatalf("PowerShell env should not set BASH_ENV: %q", values["BASH_ENV"])
	}
}

// TestBashResolvesBundledToolsDespiteProfile guards the reason the shim exists:
// bash -lc runs the login profile before BASH_ENV, and a profile that resets
// PATH would otherwise hide the embedded rg/fd.
func TestBashResolvesBundledToolsDespiteProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX login-profile PATH reset scenario")
	}
	runtimeShell := DiscoverShellRuntime()
	if !runtimeShell.BashAvailable() {
		t.Skip("bash unavailable")
	}
	toolsDir, err := search.ToolsDir()
	if err != nil {
		t.Skipf("embedded tools unavailable: %v", err)
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("export PATH=/usr/bin:/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(runtimeShell.bash.path, runtimeShell.bash.args("rg --version >/dev/null && fd --version >/dev/null && command -v rg && command -v fd")...) //nolint:gosec // test invokes the discovered system shell intentionally
	cmd.Env = setEnvValue(runtimeShell.bash.env(), "HOME", home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), filepath.Base(toolsDir)) {
		t.Fatalf("bash resolved rg/fd outside the bundled tools dir:\n%s", out)
	}
}
