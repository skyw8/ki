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
	pathDirs          []string
	snapshotPath      string
}

func (s Shell) available() bool { return s.path != "" }

func (s Shell) args(command string) []string {
	if s.snapshotPath != "" {
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
		return []string{"--noprofile", "--norc", "-c", ". " + quote(s.snapshotPath) + " || exit; builtin eval -- " + quote(command), s.path}
	}
	if s.kind == shellPowerShell {
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
	env := processenv.WithProxyEnvironment(processenv.ChildEnvironment())
	for _, value := range [][2]string{
		{"PAGER", ""}, {"GIT_PAGER", ""}, {"GH_PAGER", ""},
		{"NO_COLOR", "1"}, {"TERM", "dumb"}, {"COLORTERM", ""},
	} {
		env = setEnvValue(env, value[0], value[1])
	}
	dir, toolsErr := search.ToolsDir()
	if toolsErr == nil {
		env = withBundledSearchTools(env, dir, s.pathDirs, s.kind)
	} else if len(s.pathDirs) > 0 {
		env = prependPath(env, s.pathDirs)
	}
	if s.snapshotPath != "" {
		env = setEnvValue(env, "BASH_ENV", "")
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

const ExtensionPathEnv = "KI_EXTENSION_PATH_DIRS"

const pathListSeparator = ":"

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

func shellPath(path string) string {
	if runtime.GOOS == "windows" {
		return filepath.ToSlash(path)
	}
	return path
}

type ShellRuntime struct {
	bash       Shell
	powerShell *Shell
}

func (s ShellRuntime) BashAvailable() bool { return s.bash.available() }

func (s ShellRuntime) PowerShellEnabled() bool { return s.powerShell != nil }

type shellDiscovery struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	exists   func(string) bool
}

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

func (s ShellRuntime) WithoutPowerShell() ShellRuntime { s.powerShell = nil; return s }
