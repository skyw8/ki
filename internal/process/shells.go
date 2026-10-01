package process

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

type Shell struct {
	login             *bool
	interactive       bool
	kind              shellKind
	path              string
	powerShellEdition powerShellEdition
	setShellEnv       bool
	// pathDirs are extension-contributed directories prepended to PATH after
	// ki's own bundled tools directory.
	pathDirs []string
}

func (s Shell) available() bool { return s.path != "" }

func (s Shell) args(command string) []string {
	if s.kind == shellPowerShell {
		// PowerShell does not reliably propagate a native program's exit code
		// from -Command. Capture it immediately so git/npm failures remain tool
		// errors; cmdlet-only commands fall back to PowerShell's $? state.
		command += "\n; $__ki_exit = if ($null -ne $LASTEXITCODE) { $LASTEXITCODE } elseif ($?) { 0 } else { 1 }\n; exit $__ki_exit"
		args := []string{}
		if s.login == nil || !*s.login {
			args = append(args, "-NoProfile")
		}
		if !s.interactive {
			args = append(args, "-NonInteractive")
		}
		return append(args, "-Command", command)
	}
	flag := "-lc"
	if s.login != nil && !*s.login {
		flag = "-c"
	}
	return []string{flag, command}
}

func (s Shell) env() []string {
	// Why: shell commands can launch networked tools such as npm or curl, so
	// pass the same proxy environment explicitly across every process boundary.
	env := processenv.WithProxyEnvironment(processenv.ChildEnvironment())
	dir, toolsErr := search.ToolsDir()
	if toolsErr == nil {
		env = withBundledSearchTools(env, dir, s.pathDirs, s.kind)
	} else if len(s.pathDirs) > 0 {
		// Without a bundled tools directory there is no shim to source, so the
		// extension directories are only exported on the child environment.
		env = prependPath(env, s.pathDirs)
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

// ExtensionPathEnv carries the extension PATH directories to the BASH_ENV shim.
// The shim cannot bake them in: it is one shared file in the tools cache, while
// the directory list changes with the enabled extension set.
const ExtensionPathEnv = "KI_EXTENSION_PATH_DIRS"

// pathListSeparator is fixed to the POSIX separator because the only consumer
// that reads a joined list is the sh shim, even when ki runs on Windows.
const pathListSeparator = ":"

// withBundledSearchTools prepends ki's embedded rg/fd directory and then the
// extension-contributed directories to PATH. Bash additionally gets a BASH_ENV
// shim: bash -lc sources /etc/profile and the user's profile before BASH_ENV,
// and those files can overwrite PATH after the child environment is already
// fixed, so exporting PATH alone is not reliable. The shim re-prepends the
// bundled directory (from its own location) and every extension directory
// (from ExtensionPathEnv).
func withBundledSearchTools(env []string, dir string, extra []string, kind shellKind) []string {
	env = prependPath(env, append([]string{dir}, extra...))
	if len(extra) > 0 {
		env = setEnvValue(env, ExtensionPathEnv, strings.Join(extra, pathListSeparator))
	}
	if kind != shellBash {
		return env
	}
	shim := shellPath(filepath.Join(dir, search.ToolsShimName))
	if original := envValue(env, "BASH_ENV"); original != "" && filepath.Clean(original) != filepath.Clean(shim) {
		env = setEnvValue(env, "KI_ORIG_BASH_ENV", original)
	}
	return setEnvValue(env, "BASH_ENV", shim)
}

// prependPath puts dirs in front of PATH in order, so dirs[0] stays first.
func prependPath(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	prefix := strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator)
	for i, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			env[i] = key + "=" + prefix + value
			return env
		}
	}
	return append(env, "PATH="+strings.TrimSuffix(prefix, string(os.PathListSeparator)))
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
	bash       Shell
	powerShell *Shell
}

// BashAvailable reports whether exec_command can select discovered Bash.
func (s ShellRuntime) BashAvailable() bool { return s.bash.available() }

// PowerShellEnabled reports whether exec_command has a PowerShell default.
func (s ShellRuntime) PowerShellEnabled() bool { return s.powerShell != nil }

type shellDiscovery struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	exists   func(string) bool
}

// DiscoverShellRuntime resolves command interpreters once for the server.
// Missing optional interpreters are represented as unavailable specs so the
// execution can fail explicitly without preventing server startup.
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
		return ShellRuntime{bash: Shell{kind: shellBash, path: findUnixBash(d)}}
	}

	runtime := ShellRuntime{
		bash: Shell{kind: shellBash, path: findWindowsBash(d), setShellEnv: true},
	}
	if path, lookErr := d.lookPath("pwsh"); lookErr == nil && path != "" {
		runtime.powerShell = &Shell{kind: shellPowerShell, path: path, powerShellEdition: powerShellCore}
	} else if path, lookErr = d.lookPath("powershell"); lookErr == nil && path != "" {
		runtime.powerShell = &Shell{kind: shellPowerShell, path: path, powerShellEdition: powerShellDesktop}
	} else {
		// Match Claude Code's graceful behavior: the Windows-only tool remains
		// visible and explains that PowerShell is unavailable when invoked.
		runtime.powerShell = &Shell{kind: shellPowerShell}
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

func DefaultShellRuntime() ShellRuntime {
	return DiscoverShellRuntime()
}

func (s ShellRuntime) Empty() bool { return s.bash.kind == "" }

// WithPathDirs returns a session/turn configuration without changing shared discovery.
func (s ShellRuntime) WithPathDirs(dirs []string) ShellRuntime {
	s.bash.pathDirs = dirs
	if s.powerShell != nil {
		shell := *s.powerShell
		shell.pathDirs = dirs
		s.powerShell = &shell
	}
	return s
}
func (s Shell) Path() string                     { return s.path }
func (s Shell) PowerShell() bool                 { return s.kind == shellPowerShell }
func (s Shell) DesktopPowerShell() bool          { return s.powerShellEdition == powerShellDesktop }
func (s Shell) WithPathDirs(dirs []string) Shell { s.pathDirs = dirs; return s }

// WithoutPowerShell selects Bash for POSIX command callers.
func (s ShellRuntime) WithoutPowerShell() ShellRuntime { s.powerShell = nil; return s }
