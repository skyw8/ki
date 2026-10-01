package extension

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// This opt-in uses the checkout's existing Cargo caches instead of repeating the native SDK build.
func TestBundledRustMissingBinaryInstall(t *testing.T) {
	if os.Getenv("KI_ZVEC_GREP_INSTALL") != "1" {
		t.Skip("set KI_ZVEC_GREP_INSTALL=1 to exercise the native source installer")
	}
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repository, "extensions", "zvec-grep")
	d, ok := loadPackage(source)
	if !ok || d.Error != "" {
		t.Fatalf("load Rust source package: %+v", d)
	}
	d.Enabled = true
	if d.manifest.Runtime.InstallWhen != installMissing || len(d.manifest.Runtime.Install) == 0 {
		t.Fatalf("missing-binary installer not declared: %+v", d.manifest.Runtime)
	}
	bin := filepath.Join(source, "bin")
	backup, err := os.MkdirTemp(source, ".rust-install-test-")
	if err != nil {
		t.Fatal(err)
	}
	hadBin := false
	if _, err := os.Lstat(bin); err == nil {
		if err := os.Rename(bin, filepath.Join(backup, "bin")); err != nil {
			_ = os.RemoveAll(backup)
			t.Fatal(err)
		}
		hadBin = true
	} else if !os.IsNotExist(err) {
		_ = os.RemoveAll(backup)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(bin); err != nil {
			t.Error(err)
		}
		if hadBin {
			if err := os.Rename(filepath.Join(backup, "bin"), bin); err != nil {
				t.Error(err)
				return // Preserve the backup if restoration fails.
			}
		}
		if err := os.RemoveAll(backup); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	home := t.TempDir()
	client, err := startRPC(ctx, d, "", home, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("build missing Rust binary through host: %v", err)
	}
	if len(client.registration.Tools) != 1 || client.registration.Tools[0].Name != "zvec_grep_search" {
		client.close()
		t.Fatal(client.registration)
	}
	client.close()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binary, err := os.ReadFile(filepath.Join(bin, "zvec-grep"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.ReadFile(filepath.Join(bin, "zg"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(binary, alias) {
		t.Fatal("Rust installer published different launcher and CLI bytes")
	}
	t.Setenv("PATH", t.TempDir())
	// This helper clears PATH and checks host initialization, CLI alias, and a piped EOF reply.
	// Its successful second start proves the host skips Cargo/Go when the binary is present.
	assertPackagedInitialize(t, repository, home, d)
}
