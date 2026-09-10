package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ki/internal/processenv"
	"ki/internal/search"
)

type shellKind string

const (
	shellBash       shellKind = "bash"
	shellPowerShell shellKind = "powershell"
)

type powerShellEdition string

const (
	powerShellCore    powerShellEdition = "core"
	powerShellDesktop powerShellEdition = "desktop"
)

type shellSpec struct {
	kind              shellKind
	path              string
	powerShellEdition powerShellEdition
	setShellEnv       bool
}

func (s shellSpec) available() bool { return s.path != "" }

func (s shellSpec) args(command string) []string {
	if s.kind == shellPowerShell {
		// PowerShell does not reliably propagate a native program's exit code
		// from -Command. Capture it immediately so git/npm failures remain tool
		// errors; cmdlet-only commands fall back to PowerShell's $? state.
		command += "\n; $__ki_exit = if ($null -ne $LASTEXITCODE) { $LASTEXITCODE } elseif ($?) { 0 } else { 1 }\n; exit $__ki_exit"
		return []string{"-NoProfile", "-NonInteractive", "-Command", command}
	}
	return []string{"-lc", command}
}

func (s shellSpec) env() []string {
	// Why: shell commands can launch networked tools such as npm or curl, so
	// pass the same proxy environment explicitly across every process boundary.
	env := processenv.WithProxyEnvironment(processenv.ChildEnvironment())
	if dir, err := search.ToolsDir(); err == nil {
		env = withBundledSearchTools(env, dir, s.kind)
	}
	if !s.setShellEnv {
		return env
	}
	for i, item := range env {
		if strings.EqualFold(strings.SplitN(item, "=", 2)[0], "SHELL") {
			env[i] = "SHELL=" + s.path
			return env
		}
	}
	return append(env, "SHELL="+s.path)
}

// withBundledSearchTools prepends ki's embedded rg/fd directory to PATH. Bash
// additionally gets a BASH_ENV shim: bash -lc sources /etc/profile and the
// user's profile before BASH_ENV, and those files can overwrite PATH after the
// child environment is already fixed, so exporting PATH alone is not reliable.
func withBundledSearchTools(env []string, dir string, kind shellKind) []string {
	env = prependPath(env, dir)
	if kind != shellBash {
		return env
	}
	shim := shellPath(filepath.Join(dir, search.ToolsShimName))
	if original := envValue(env, "BASH_ENV"); original != "" && filepath.Clean(original) != filepath.Clean(shim) {
		env = setEnvValue(env, "KI_ORIG_BASH_ENV", original)
	}
	return setEnvValue(env, "BASH_ENV", shim)
}

func prependPath(env []string, dir string) []string {
	for i, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			env[i] = key + "=" + dir + string(os.PathListSeparator) + value
			return env
		}
	}
	return append(env, "PATH="+dir)
}

func envValue(env []string, key string) string {
	for _, item := range env {
		k, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(k, key) {
			return value
		}
	}
	return ""
}

func setEnvValue(env []string, key, value string) []string {
	for i, item := range env {
		k, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(k, key) {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

// shellPath renders a host path in a form the shell reads unambiguously. Git
// Bash on Windows accepts both separators, but forward slashes avoid escaping.
func shellPath(path string) string {
	if runtime.GOOS == "windows" {
		return filepath.ToSlash(path)
	}
	return path
}

// ShellRuntime is the process-wide set of command interpreters discovered at
// server startup. Its fields stay private so callers cannot construct an
// invalid platform combination; Set only needs to carry the resolved value.
type ShellRuntime struct {
	bash       shellSpec
	powerShell *shellSpec
}

// BashAvailable reports whether Bash and Bash-dependent tools should be exposed.
func (s ShellRuntime) BashAvailable() bool { return s.bash.available() }

// PowerShellEnabled reports whether the Windows-only PowerShell tool should be exposed.
func (s ShellRuntime) PowerShellEnabled() bool { return s.powerShell != nil }

type shellDiscovery struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	exists   func(string) bool
}

// DiscoverShellRuntime resolves command interpreters once for the server.
// Missing optional interpreters are represented as unavailable specs so the
// corresponding model tools can be omitted instead of failing server startup.
func DiscoverShellRuntime() ShellRuntime {
	d := shellDiscovery{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		exists: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir()
		},
	}
	return discoverShellRuntime(d)
}

func discoverShellRuntime(d shellDiscovery) ShellRuntime {
	if d.goos != "windows" {
		return ShellRuntime{bash: shellSpec{kind: shellBash, path: findUnixBash(d)}}
	}

	runtime := ShellRuntime{
		bash: shellSpec{kind: shellBash, path: findWindowsBash(d), setShellEnv: true},
	}
	if path, lookErr := d.lookPath("pwsh"); lookErr == nil && path != "" {
		runtime.powerShell = &shellSpec{kind: shellPowerShell, path: path, powerShellEdition: powerShellCore}
	} else if path, lookErr = d.lookPath("powershell"); lookErr == nil && path != "" {
		runtime.powerShell = &shellSpec{kind: shellPowerShell, path: path, powerShellEdition: powerShellDesktop}
	} else {
		// Match Claude Code's graceful behavior: the Windows-only tool remains
		// visible and explains that PowerShell is unavailable when invoked.
		runtime.powerShell = &shellSpec{kind: shellPowerShell}
	}
	return runtime
}

func findWindowsBash(d shellDiscovery) string {
	for _, name := range []string{"KI_GIT_BASH_PATH", "CLAUDE_CODE_GIT_BASH_PATH"} {
		if configured := strings.TrimSpace(d.getenv(name)); configured != "" {
			path, err := filepath.Abs(configured)
			if err == nil && d.exists(path) {
				return path
			}
		}
	}

	// Match pi's normal Windows installation probes before accepting another
	// Bash implementation such as MSYS2, Cygwin, or WSL from PATH.
	for _, root := range []string{d.getenv("ProgramFiles"), d.getenv("ProgramFiles(x86)")} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		candidate := filepath.Join(root, "Git", "bin", "bash.exe")
		if d.exists(candidate) {
			absolute, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return absolute
			}
			return candidate
		}
	}

	for _, name := range []string{"bash.exe", "bash"} {
		if path, err := d.lookPath(name); err == nil && path != "" && d.exists(path) {
			absolute, absErr := filepath.Abs(path)
			if absErr == nil {
				return absolute
			}
			return path
		}
	}
	return ""
}

func findUnixBash(d shellDiscovery) string {
	if d.exists("/bin/bash") {
		return "/bin/bash"
	}
	if path, err := d.lookPath("bash"); err == nil && path != "" && d.exists(path) {
		return path
	}
	return ""
}

func fallbackShellRuntime() ShellRuntime {
	return DiscoverShellRuntime()
}
