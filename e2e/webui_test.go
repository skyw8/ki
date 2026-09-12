package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWebUIPlaywright(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	webDir := filepath.Join(filepath.Dir(filepath.Dir(file)), "web")
	if _, err := os.Stat(filepath.Join(webDir, "node_modules", "@playwright", "test")); err != nil {
		t.Skip("web/node_modules/@playwright/test missing; cd web && bun install")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found in PATH; install bun to run the WebUI suite")
	}
	// Why: the runner starts one isolated server per work unit from this exact
	// binary, so every spec still exercises the Go-built SPA while the phone,
	// tablet, and desktop matrix runs concurrently instead of serially.
	bin := builtKI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "run", "test:e2e")
	cmd.Dir = webDir
	cmd.Env = webSuiteEnv(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, out)
	}
}

// webSuiteEnv isolates the Playwright runner from server settings inherited from
// the Go harness (which owns its own server and KI_HOME) and pins the binary
// under test.
func webSuiteEnv(bin string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "KI_HOME=") ||
			strings.HasPrefix(entry, "KI_FAKE=") ||
			strings.HasPrefix(entry, "KI_SERVER_ADDR=") ||
			strings.HasPrefix(entry, "KI_SERVE_ADDR=") ||
			strings.HasPrefix(entry, "KI_BASE_URL=") ||
			strings.HasPrefix(entry, "KI_SKIP_SERVER=") ||
			strings.HasPrefix(entry, "KI_BIN=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "KI_BIN="+bin, "KI_E2E_PROJECT=fake")
}
