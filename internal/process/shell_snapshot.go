package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type shellSnapshotCache struct {
	mu         sync.Mutex
	entries    map[string]shellSnapshotEntry
	dir        string
	generation uint64
	closed     bool
	files      int
}

type shellSnapshotEntry struct {
	path    string
	expires time.Time
	ready   chan struct{}
}

func (m *Manager) InvalidateShellSnapshots() {
	m.snapshots.mu.Lock()
	m.snapshots.generation++
	m.snapshots.entries = nil
	m.snapshots.mu.Unlock()
}

func (m *Manager) closeShellSnapshots() {
	m.snapshots.mu.Lock()
	defer m.snapshots.mu.Unlock()
	m.snapshots.closed = true
	if m.snapshots.dir != "" {
		_ = os.RemoveAll(m.snapshots.dir)
	}
	m.snapshots.entries = nil
}

func snapshotIdentity(shell Shell, cwd string, env []string) (string, error) {
	h := sha256.New()
	fmt.Fprintln(h, shell.path, cwd, strings.Join(env, "\x00"))
	home := envValue(env, "HOME")
	paths := []string{shell.path, "/etc/profile", "/etc/bash.bashrc", "/etc/bashrc", filepath.Join(home, ".bash_profile"), filepath.Join(home, ".bash_login"), filepath.Join(home, ".profile"), filepath.Join(home, ".bashrc"), envValue(env, "BASH_ENV"), envValue(env, "KI_ORIG_BASH_ENV")}
	for _, dir := range []string{"/etc/profile.d"} {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					paths = append(paths, filepath.Join(dir, entry.Name()))
				}
			}
		}
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			fmt.Fprintln(h, path, "missing")
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintln(h, path, info.Size(), info.ModTime().UnixNano(), info.Mode())
		if path != shell.path {
			if info.Size() > 1<<20 {
				return "", fmt.Errorf("profile too large")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			_, _ = h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) prepareShellSnapshot(ctx context.Context, shell Shell, cwd string) Shell {
	if ctx.Err() != nil || runtime.GOOS == "windows" || shell.kind != shellBash || (shell.login != nil && !*shell.login) {
		return shell
	}
	env := shell.env()
	key, err := snapshotIdentity(shell, cwd, env)
	if err != nil {
		return shell
	}
	cache := &m.snapshots
	cache.mu.Lock()
	if cache.closed {
		cache.mu.Unlock()
		return shell
	}
	if entry, ok := cache.entries[key]; ok && time.Now().Before(entry.expires) {
		cache.mu.Unlock()
		if entry.ready != nil {
			select {
			case <-entry.ready:
				return m.prepareShellSnapshot(ctx, shell, cwd)
			case <-ctx.Done():
				return shell
			}
		}
		if entry.path != "" {
			if info, err := os.Stat(entry.path); err == nil && info.Mode().IsRegular() {
				shell.snapshotPath = entry.path
			}
		}
		return shell
	}
	if cache.files >= 64 {
		cache.mu.Unlock()
		return shell
	}
	if cache.dir == "" {
		cache.dir, err = os.MkdirTemp("", "ki-shell-snapshot-")
		if err != nil {
			cache.mu.Unlock()
			return shell
		}
	}
	if len(cache.entries) >= 16 {
		cache.entries = nil
	}
	if cache.entries == nil {
		cache.entries = map[string]shellSnapshotEntry{}
	}
	file, err := os.CreateTemp(cache.dir, "snapshot-*.sh")
	if err != nil {
		cache.mu.Unlock()
		return shell
	}
	cache.files++
	path := file.Name()
	entry := shellSnapshotEntry{expires: time.Now().Add(30 * time.Second), ready: make(chan struct{})}
	cache.entries[key] = entry
	generation := cache.generation
	cache.mu.Unlock()
	defer func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		close(entry.ready)
		entry.ready = nil
		if !cache.closed && cache.generation == generation {
			cache.entries[key] = entry
		}
	}()
	captureCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(captureCtx, shell.path, "-lc", `ulimit -f 2048; {
for __ki_snapshot_name in $(compgen -v); do
 case "$__ki_snapshot_name" in BASH*|EUID|UID|PPID|SHELLOPTS|DIRSTACK|GROUPS|FUNCNAME|LINENO|PIPESTATUS|RANDOM|SECONDS|SHLVL|_|__ki_snapshot_*) continue;; esac
 __ki_snapshot_decl=$(declare -p "$__ki_snapshot_name" 2>/dev/null) || continue
 printf '%s\n' "$__ki_snapshot_decl"
done
declare -p BASH_ENV 2>/dev/null || true
declare -f; alias -p; printf "cd -- %q\n" "$PWD"; set +o; shopt -p; umask -p; true; } >&3`)
	command.Dir = cwd
	command.Env = env
	command.ExtraFiles = []*os.File{file}
	output := &snapshotDiagnostic{}
	command.Stdout = output
	command.Stderr = output
	command.WaitDelay = 200 * time.Millisecond
	detachCmd(command)
	command.Cancel = func() error { killCmd(command); return nil }
	err = command.Run()
	closeErr := file.Close()
	if err != nil || closeErr != nil || output.Len() != 0 {
		_ = os.Remove(path)
		return shell
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 1<<20 {
		_ = os.Remove(path)
		return shell
	}
	entry.path = path
	shell.snapshotPath = path
	return shell
}

type snapshotDiagnostic struct {
	mu    sync.Mutex
	bytes int
}

func (d *snapshotDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	d.bytes += len(p)
	d.mu.Unlock()
	return len(p), nil
}
func (d *snapshotDiagnostic) Len() int { d.mu.Lock(); defer d.mu.Unlock(); return d.bytes }
