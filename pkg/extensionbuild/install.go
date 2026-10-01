package extensionbuild

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

func Main(name string) {
	root := os.Getenv("KI_EXTENSION_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}
	if err := Install(context.Background(), root, name, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
func installed(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0)
}
func Install(ctx context.Context, root, name string, output io.Writer) error {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid extension name %q", name)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	binary := filepath.Join(root, "bin", executable(name))
	if installed(binary) {
		return nil
	}
	if name == "zvec-grep" {
		if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err != nil {
			return fmt.Errorf("cannot install missing executable: Rust sources are missing (Cargo.toml); use a binary package or the complete source package")
		}
	} else if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		return fmt.Errorf("cannot install missing executable: Go sources are missing (main.go); use a binary package or the complete source package")
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(root, "bin", ".install.lock"))
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("wait for extension build: %w", err)
	}
	if !locked {
		return fmt.Errorf("wait for extension build: %w", ctx.Err())
	}
	defer lock.Unlock()
	// A second first launch may have finished while this process waited.
	if installed(binary) {
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(binary), "."+name+"-build-")
	if err != nil {
		return err
	}
	pending := temp.Name()
	_ = temp.Close()
	defer os.Remove(pending)
	if name == "zvec-grep" {
		command := exec.CommandContext(ctx, "cargo", "build", "--release", "--locked", "--manifest-path", filepath.Join(root, "Cargo.toml"))
		command.Dir = root
		command.Env = environment(os.Environ(), "CARGO_TARGET_DIR", filepath.Join(root, "target"))
		command.Stdout, command.Stderr = output, output
		if err := command.Run(); err != nil {
			return fmt.Errorf("build Rust extension (requires Cargo/Rust 1.98.0, CMake, C++, and libclang): %w", err)
		}
		if err := copyExecutable(filepath.Join(root, "target", "release", executable(name)), pending); err != nil {
			return err
		}
		alias := filepath.Join(root, "bin", executable("zg"))
		// Publish the CLI alias before the main executable, so a successful install
		// always has both entry points with identical self-contained launcher bytes.
		aliasTemp, err := os.CreateTemp(filepath.Dir(alias), ".zg-build-")
		if err != nil {
			return err
		}
		aliasPending := aliasTemp.Name()
		_ = aliasTemp.Close()
		defer os.Remove(aliasPending)
		if err := copyExecutable(pending, aliasPending); err != nil {
			return err
		}
		if err := replaceFile(aliasPending, alias); err != nil {
			return fmt.Errorf("publish zg: %w", err)
		}
	} else {
		command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", pending, ".")
		command.Dir = root
		command.Env = environment(environment(environment(os.Environ(), "CGO_ENABLED", "0"), "GOOS", runtime.GOOS), "GOARCH", runtime.GOARCH)
		command.Stdout, command.Stderr = output, output
		if err := command.Run(); err != nil {
			return fmt.Errorf("build Go extension (requires Go and the complete source package): %w", err)
		}
	}
	if err := os.Chmod(pending, 0755); err != nil {
		return err
	}
	if err := replaceFile(pending, binary); err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	return nil
}
func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(0755)
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func environment(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		existing, _, _ := strings.Cut(item, "=")
		if !strings.EqualFold(existing, key) {
			out = append(out, item)
		}
	}
	return append(out, key+"="+value)
}
