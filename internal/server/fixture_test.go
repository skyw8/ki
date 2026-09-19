package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	// fixturesDir creates the shared fixture directory on first use; TestMain
	// removes it once the suite finishes.
	fixturesDir = sync.OnceValues(func() (string, error) {
		return os.MkdirTemp("", "ki-server-fixtures-")
	})

	fixtureMu   sync.Mutex
	fixtureBins = map[string]string{}
	fixtureErrs = map[string]error{}
)

// TestMain drops the shared fixture binaries once the suite finishes.
func TestMain(m *testing.M) {
	code := m.Run()
	if dir, err := fixturesDir(); err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

// fixtureExeSuffix is the platform executable suffix. Why: `go build -o name`
// writes exactly that name, and Windows cannot start a process image without
// .exe, so the sidecar fixtures must carry it.
func fixtureExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// buildFixture compiles a test sidecar once per process and reuses the binary.
// Why: linking the fixture dominates the extension tests that need one and they
// all build the same source; the binary is read-only at run time.
func buildFixture(t *testing.T, key, srcDir string) string {
	t.Helper()
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	if bin, ok := fixtureBins[key]; ok {
		if err := fixtureErrs[key]; err != nil {
			t.Fatal(err)
		}
		return bin
	}
	dir, err := fixturesDir()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, key+fixtureExeSuffix())
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // builds a local test fixture
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		fixtureErrs[key] = fmt.Errorf("build %s fixture: %w\n%s", key, err, out)
		t.Fatal(fixtureErrs[key])
	}
	fixtureBins[key] = bin
	return bin
}

// fixtureSource resolves e2e/testdata/extensions/<name> from this file.
func fixtureSource(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "e2e", "testdata", "extensions", name)
}

// copyFixture copies a shared fixture binary to dst. Why: a manifest that points
// at a path inside the extension root needs a private location, and rebuilding
// the same fixture per test is what this cache exists to avoid.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
}
