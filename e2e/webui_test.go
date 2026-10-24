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
	bin := builtKI(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "run", "test")
	cmd.Dir = webDir
	cmd.Env = webSuiteEnv(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("web tests: %v\n%s", err, out)
	}
}

func webSuiteEnv(bin string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "KI_HOME=") ||
			strings.HasPrefix(entry, "KI_FAKE=") ||
			strings.HasPrefix(entry, "KI_SERVER_ADDR=") ||
			strings.HasPrefix(entry, "KI_SERVE_ADDR=") ||
			strings.HasPrefix(entry, "KI_BASE_URL=") ||
			strings.HasPrefix(entry, "KI_SKIP_SERVER=") ||
			strings.HasPrefix(entry, "KI_BIN=") ||
			strings.HasPrefix(entry, "KI_EXTENSIONS_SOURCE=") ||
			strings.HasPrefix(entry, "KI_EXTENSIONS_RELEASE=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "KI_BIN="+bin, "KI_E2E_PROJECT=fake")
}
