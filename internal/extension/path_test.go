package extension

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/session"
)

func writePathPkg(t *testing.T, extensionsDir, name, dirs string) string {
	t.Helper()
	writePkg(t, extensionsDir, name, `{
		"name":"`+name+`","capabilities":["path"],
		"runtime":{"kind":"none","path":[`+dirs+`]}
	}`)
	return filepath.Join(extensionsDir, name)
}

func TestDiscoverRejectsPathDirsWithoutCapability(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "alpha", `{
		"name":"alpha","capabilities":["skill"],
		"runtime":{"path":["bin"]}
	}`)
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Error == "" {
		t.Fatalf("path dirs without capability must fail the manifest: %+v", got.All)
	}
	if len(got.Enabled) != 0 || len(PathDirs(got.Enabled)) != 0 {
		t.Fatalf("package with a manifest error reached the chain: %+v", got.Enabled)
	}
}

func TestDiscoverRejectsEscapingPathDirs(t *testing.T) {
	for _, tc := range []struct{ name, dirs string }{
		{"absolute", `"/usr/bin"`},
		{"parent", `"..\/bin"`},
		{"empty", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writePathPkg(t, filepath.Join(home, "extensions"), "alpha", tc.dirs)
			got := Discover(home, session.Toggle{})
			if len(got.All) != 1 || got.All[0].Error == "" {
				t.Fatalf("expected manifest error for runtime.path %s: %+v", tc.dirs, got.All)
			}
			if dirs := PathDirs(got.Enabled); len(dirs) != 0 {
				t.Fatalf("escaping dir leaked into PATH: %v", dirs)
			}
		})
	}
}

// TestPathDirsListsExistingDirsInChainOrder pins the resolution rules: only
// enabled packages with the capability contribute, directories are ordered by
// package name, duplicate declarations collapse, and missing directories are
// skipped because runtime.install may create them after this point.
func TestPathDirsListsExistingDirsInChainOrder(t *testing.T) {
	home := t.TempDir()
	extensionsDir := filepath.Join(home, "extensions")
	alphaDir := writePathPkg(t, extensionsDir, "alpha", `"bin","bin/"`)
	betaDir := writePathPkg(t, extensionsDir, "beta", `"bin"`)
	if err := os.MkdirAll(filepath.Join(alphaDir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	// beta's directory does not exist yet, so only alpha contributes.
	got := Discover(home, session.Toggle{})
	dirs := PathDirs(got.Enabled)
	if len(dirs) != 1 || dirs[0] != filepath.Join(alphaDir, "bin") {
		t.Fatalf("PathDirs = %v, want only alpha/bin", dirs)
	}

	// node_modules/.bin appears only after runtime.install: the next turn sees
	// it without any reload.
	installed := filepath.Join(betaDir, "bin")
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	dirs = PathDirs(Discover(home, session.Toggle{}).Enabled)
	want := []string{filepath.Join(alphaDir, "bin"), installed}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("PathDirs = %v, want %v", dirs, want)
	}

	disabled := Discover(home, session.Toggle{Disabled: []string{"alpha"}})
	if dirs := PathDirs(disabled.Enabled); len(dirs) != 1 || dirs[0] != installed {
		t.Fatalf("disabled package still contributed: %v", dirs)
	}
}
