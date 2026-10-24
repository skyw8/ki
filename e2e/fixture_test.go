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
	fixturesDir = sync.OnceValues(func() (string, error) {
		return os.MkdirTemp("", "ki-e2e-fixtures-")
	})

	fixtureMu   sync.Mutex
	fixtureBins = map[string]string{}
	fixtureErrs = map[string]error{}
)

func TestMain(m *testing.M) {
	code := m.Run()
	if dir, err := fixturesDir(); err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

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

func fixtureSource(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "extensions", name)
}
