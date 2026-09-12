package extension

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
	fixtureDirOnce sync.Once
	fixtureDir     string
	fixtureDirErr  error

	fixtureMu   sync.Mutex
	fixtureBins = map[string]string{}
	fixtureErrs = map[string]error{}
)

// TestMain drops the shared fixture binaries once the suite finishes.
func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureDir != "" {
		_ = os.RemoveAll(fixtureDir)
	}
	os.Exit(code)
}

func fixturesDir() (string, error) {
	fixtureDirOnce.Do(func() {
		fixtureDir, fixtureDirErr = os.MkdirTemp("", "ki-extension-fixtures-")
	})
	return fixtureDir, fixtureDirErr
}

// buildFixture compiles a test sidecar once per process and reuses the binary.
// Why: linking the fixture dominates these tests and a dozen of them build the
// very same one; the binary is read-only at run time, so sharing it is invisible.
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
	bin := filepath.Join(dir, key)
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

// copyFixture copies a shared fixture binary into a per-test directory.
// Why: a test that deletes its sidecar to exercise a failure path must not
// remove the binary the other tests share.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return dst
}
