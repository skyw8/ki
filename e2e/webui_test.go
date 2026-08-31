package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		t.Skip("web/node_modules/@playwright/test missing; cd web && npm install")
	}
	home, proj := isolate(t)
	sf := startServe(t, home)
	// Why: the exact phone/tablet/desktop matrix intentionally exercises every
	// major WebUI surface and takes slightly over two minutes on slower runners.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npx", "playwright", "test", "--project=fake")
	cmd.Dir = webDir
	cmd.Env = append(childEnv(home),
		"KI_BASE_URL=http://"+sf.Addr,
		"KI_SKIP_SERVER=1",
		"KI_PW_CWD="+proj,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, out)
	}
}
