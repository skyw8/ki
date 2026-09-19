package e2e

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
		return os.MkdirTemp("", "ki-e2e-fixtures-")
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

// buildFixture compiles a test sidecar once per process and reuses the binary.
// Why: several e2e tests drive the same sidecar fixture and linking it is the
// dominant cost; the binary is read-only at run time.
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
	bin := filepath.Join(dir, key+exeSuffix())
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
	return filepath.Join(filepath.Dir(file), "testdata", "extensions", name)
}
