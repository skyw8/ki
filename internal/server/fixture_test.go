package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

var (
	fixturesDir = sync.OnceValues(func() (string, error) {
		return os.MkdirTemp("", "ki-server-fixtures-")
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

func fixtureExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
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

func fixtureSource(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "e2e", "testdata", "extensions", name)
}

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

var agentTestSequence atomic.Uint64

func nextAgentTestName() string { return fmt.Sprintf("child_%d", agentTestSequence.Add(1)) }
