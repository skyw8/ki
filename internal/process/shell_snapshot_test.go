package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func isolatedSnapshotShell(t testing.TB) (Shell, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX snapshot")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile")
	if err = os.WriteFile(profile, []byte("export KI_SNAPSHOT_VALUE=one\nki_private_var=private\nki_snapshot_fn() { printf 'function-ok-%s' \"$ki_private_var\"; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "isolated-bash")
	script := "#!/bin/sh\nif [ \"$1\" = \"-lc\" ]; then shift; exec " + bash + " --noprofile --norc -c \"$1\"; fi\nexec " + bash + " \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", profile)
	return Shell{kind: shellBash, path: wrapper}, dir, profile
}

func runSnapshotShell(t testing.TB, s Shell, cwd, command string) string {
	t.Helper()
	cmd := exec.Command(s.path, s.args(command)...)
	cmd.Env = s.env()
	cmd.Dir = cwd
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v %s", err, b)
	}
	return string(b)
}

func TestShellSnapshotCacheInvalidationFallbackAndReplay(t *testing.T) {
	shell, cwd, profile := isolatedSnapshotShell(t)
	m := NewManager()
	defer m.Close()
	prepared := m.prepareShellSnapshot(t.Context(), shell, cwd)
	if prepared.snapshotPath == "" {
		t.Fatal("snapshot unavailable")
	}
	if got := runSnapshotShell(t, prepared, cwd, "printf '%s:' \"$KI_SNAPSHOT_VALUE\"; ki_snapshot_fn"); got != "one:function-ok-private" {
		t.Fatalf("replay: %q", got)
	}
	cached := m.prepareShellSnapshot(t.Context(), shell, cwd)
	if cached.snapshotPath != prepared.snapshotPath {
		t.Fatal("cache miss")
	}
	if err := os.WriteFile(profile, []byte("export KI_SNAPSHOT_VALUE=two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := m.prepareShellSnapshot(t.Context(), shell, cwd)
	if changed.snapshotPath == prepared.snapshotPath || changed.snapshotPath == "" {
		t.Fatal("profile mutation reused snapshot")
	}
	if got := runSnapshotShell(t, changed, cwd, "printf %s \"$KI_SNAPSHOT_VALUE\""); got != "two" {
		t.Fatalf("changed: %q", got)
	}
	other := t.TempDir()
	moved := m.prepareShellSnapshot(t.Context(), shell, other)
	if moved.snapshotPath == changed.snapshotPath {
		t.Fatal("cwd reused")
	}
	m.InvalidateShellSnapshots()
	reloaded := m.prepareShellSnapshot(t.Context(), shell, cwd)
	if reloaded.snapshotPath == changed.snapshotPath {
		t.Fatal("reload reused")
	}
	m.snapshots.mu.Lock()
	for key, entry := range m.snapshots.entries {
		entry.expires = time.Time{}
		m.snapshots.entries[key] = entry
	}
	m.snapshots.mu.Unlock()
	expired := m.prepareShellSnapshot(t.Context(), shell, cwd)
	if expired.snapshotPath == reloaded.snapshotPath {
		t.Fatal("TTL reused")
	}
	if err := os.WriteFile(profile, []byte("printf profile-output\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if fallback := m.prepareShellSnapshot(t.Context(), shell, cwd); fallback.snapshotPath != "" {
		t.Fatal("profile output swallowed")
	}
	noLogin := false
	shell.login = &noLogin
	if got := m.prepareShellSnapshot(t.Context(), shell, cwd); got.snapshotPath != "" {
		t.Fatal("nonlogin snapshot")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := m.prepareShellSnapshot(ctx, shell, cwd); got.snapshotPath != "" {
		t.Fatal("cancelled snapshot")
	}
}

func BenchmarkShellSnapshotWarm(b *testing.B) {
	shell, cwd, profile := isolatedSnapshotShell(b)
	if err := os.WriteFile(profile, []byte("sleep 0.02\nexport KI_SNAPSHOT_VALUE=one\n"), 0600); err != nil {
		b.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	prepared := m.prepareShellSnapshot(context.Background(), shell, cwd)
	if prepared.snapshotPath == "" {
		b.Fatal("snapshot missing")
	}
	b.Run("login-profile", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			runSnapshotShell(b, shell, cwd, ":")
		}
	})
	b.Run("cached-replay", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ready := m.prepareShellSnapshot(context.Background(), shell, cwd)
			runSnapshotShell(b, ready, cwd, ":")
		}
	})
}
