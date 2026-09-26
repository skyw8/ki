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
	// Why: the fake discovery matches paths it is handed, so the POSIX fixture
	// has to be expressed in the host's separator form to model a POSIX host.
	bashPath := filepath.FromSlash("/usr/local/bin/bash")
	got := discoverShellRuntime(fakeDiscovery("linux", nil, map[string]string{ //nolint:gosec // fake shell paths are test fixtures, not credentials
		"bash": bashPath, "pwsh": filepath.FromSlash("/usr/bin/pwsh"),
	}, map[string]bool{bashPath: true}))
	if got.bash.path != bashPath || got.powerShell != nil {
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
	values := envToMap(withBundledSearchTools([]string{"PATH=/usr/bin:/bin", "HOME=/home/x"}, dir, nil, shellBash))

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
		[]string{"PATH=/usr/bin", "BASH_ENV=/home/x/.user-env.sh"}, dir, nil, shellBash))

	if got, want := values["KI_ORIG_BASH_ENV"], "/home/x/.user-env.sh"; got != want {
		t.Fatalf("KI_ORIG_BASH_ENV = %q, want %q", got, want)
	}
	if values["BASH_ENV"] == "/home/x/.user-env.sh" {
		t.Fatal("existing BASH_ENV was not replaced by the shim")
	}
}

func TestBundledSearchToolsDoesNotSetBashEnvForPowerShell(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	values := envToMap(withBundledSearchTools([]string{"PATH=C:\\Windows"}, dir, nil, shellPowerShell))

	if _, ok := values["BASH_ENV"]; ok {
		t.Fatalf("PowerShell env should not set BASH_ENV: %q", values["BASH_ENV"])
	}
}

// TestBundledSearchToolsPrependExtensionDirs pins the PATH contract: ki's own
// tools directory stays first so an extension can never shadow rg/fd, extension
// directories follow it in order, and the shim receives them separately because
// login profiles can rewrite PATH after the child environment is fixed.
func TestBundledSearchToolsPrependExtensionDirs(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	extA := filepath.Join(string(filepath.Separator)+"home", "x", "extensions", "alpha", "node_modules", ".bin")
	extB := filepath.Join(string(filepath.Separator)+"home", "x", "extensions", "beta", "bin")
	values := envToMap(withBundledSearchTools([]string{"PATH=/usr/bin:/bin"}, dir, []string{extA, extB}, shellBash))

	wantPath := strings.Join([]string{dir, extA, extB, "/usr/bin:/bin"}, string(os.PathListSeparator))
	if got := values["PATH"]; got != wantPath {
		t.Fatalf("PATH = %q, want %q", got, wantPath)
	}
	if got, want := values[ExtensionPathEnv], extA+pathListSeparator+extB; got != want {
		t.Fatalf("%s = %q, want %q", ExtensionPathEnv, got, want)
	}
	if _, ok := values["BASH_ENV"]; !ok {
		t.Fatal("bash env must still source the shim")
	}
}

func TestBundledSearchToolsWithoutExtensionsKeepsPathUnchanged(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"opt", "ki-tools")
	env := withBundledSearchTools([]string{"PATH=/usr/bin:/bin"}, dir, nil, shellBash)
	values := envToMap(env)
	if _, ok := values[ExtensionPathEnv]; ok {
		t.Fatalf("no extension dirs must not export %s", ExtensionPathEnv)
	}
	if got, want := values["PATH"], dir+string(os.PathListSeparator)+"/usr/bin:/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	// A shell spec without extension dirs must produce exactly the legacy
	// environment: proxy passthrough plus the bundled tools directory.
	spec := envToMap((shellSpec{kind: shellBash, path: "/bin/bash", pathDirs: nil}).env())
	if _, ok := spec[ExtensionPathEnv]; ok {
		t.Fatalf("shell env exported %s without extension dirs", ExtensionPathEnv)
	}
}

// TestBashResolvesExtensionCliDespiteProfile guards the same failure mode as
// the bundled-tools test for extension-contributed directories: the login
// profile resets PATH before BASH_ENV runs, so the shim has to re-add them.
func TestBashResolvesExtensionCliDespiteProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX login-profile PATH reset scenario")
	}
	runtimeShell := DiscoverShellRuntime()
	if !runtimeShell.BashAvailable() {
		t.Skip("bash unavailable")
	}
	extDir := t.TempDir()
	script := filepath.Join(extDir, "zi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho extension-cli-marker\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("export PATH=/usr/bin:/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bash := runtimeShell.bash
	bash.pathDirs = []string{extDir}
	cmd := exec.Command(bash.path, bash.args("zi")...) //nolint:gosec // test invokes the discovered system shell intentionally
	cmd.Env = setEnvValue(bash.env(), "HOME", home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "extension-cli-marker") {
		t.Fatalf("extension CLI did not resolve through PATH:\n%s", out)
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
