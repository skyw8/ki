//go:build live

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const liveModel = "dashscope-cn/qwen3.7-plus"

func TestLiveQwenPing(t *testing.T) {
	home, proj := isolateLive(t)
	out, errOut, code := runKI(t, "--cwd", proj, "--model", liveModel,
		"Reply with exactly the single word pong and nothing else.")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	got := strings.ToLower(out)
	if !strings.Contains(got, "pong") {
		t.Fatalf("expected pong, got:\n%s", out)
	}
	id := mustSessionID(t, out, errOut)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, `"role":"assistant"`) {
		t.Fatalf("jsonl:\n%s", raw)
	}
}

func TestLiveQwenImageAndPDF(t *testing.T) {
	home, proj := isolateLive(t)
	img := filepath.Join(proj, "red.png")
	pdf := filepath.Join(proj, "marker.pdf")
	writeRedPNG(t, img)
	writeMarkerPDF(t, pdf, pdfMarker)

	prompt := strings.Join([]string{
		"You must use the Read tool. Do not guess.",
		"1. Read the image file " + img + " and report the dominant color as one English word.",
		"2. Read the PDF file " + pdf + " and quote the marker token you find.",
		"Final answer on its own line: COLOR=<word> MARKER=<token>",
	}, "\n")

	out, errOut, code := runKI(t, "--cwd", proj, "--model", liveModel, prompt)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "red") && !strings.Contains(low, "crimson") && !strings.Contains(low, "scarlet") {
		t.Fatalf("image color not recognized:\n%s\nstderr:\n%s", out, errOut)
	}
	if !strings.Contains(out, pdfMarker) {
		t.Fatalf("pdf marker missing:\n%s\nstderr:\n%s", out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, `"toolName":"Read"`) && !strings.Contains(raw, `"name":"Read"`) {
		t.Fatalf("expected Read tool use in jsonl:\n%s", raw)
	}
}

func TestLiveQwenTwoImages(t *testing.T) {
	home, proj := isolateLive(t)
	red := filepath.Join(proj, "red.png")
	blue := filepath.Join(proj, "blue.png")
	writeRedPNG(t, red)
	writeBluePNG(t, blue)

	prompt := strings.Join([]string{
		"You must use the Read tool on BOTH files. Prefer two Read calls in one assistant turn (in parallel).",
		"Do not guess colors from filenames.",
		"1. " + red,
		"2. " + blue,
		"Final answer on its own line: RED_FILE=<color> BLUE_FILE=<color>",
	}, "\n")

	out, errOut, code := runKI(t, "--cwd", proj, "--model", liveModel, prompt)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "red") && !strings.Contains(low, "crimson") && !strings.Contains(low, "scarlet") {
		t.Fatalf("red image not recognized:\n%s\nstderr:\n%s", out, errOut)
	}
	if !strings.Contains(low, "blue") && !strings.Contains(low, "azure") && !strings.Contains(low, "navy") {
		t.Fatalf("blue image not recognized:\n%s\nstderr:\n%s", out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	raw := readJSONL(t, sessionDir(t, home, id))
	if strings.Count(strings.ToLower(raw), `"name":"read"`)+strings.Count(raw, `"toolName":"Read"`) < 1 {
		t.Fatalf("expected Read in jsonl:\n%s", raw)
	}
}

func TestLiveWebUIPlaywright(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	webDir := filepath.Join(filepath.Dir(filepath.Dir(file)), "web")
	if _, err := os.Stat(filepath.Join(webDir, "node_modules", "@playwright", "test")); err != nil {
		t.Skip("web/node_modules/@playwright/test missing; cd web && npm install")
	}
	home, proj := isolateLive(t)
	if err := os.WriteFile(filepath.Join(proj, "pw-live.txt"), []byte("KI-LIVE-MARKER-77\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sf := startServeLive(t, home, proj)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npx", "playwright", "test", "--project=live")
	cmd.Dir = webDir
	cmd.Env = append(liveChildEnv(home),
		"KI_BASE_URL=http://"+sf.Addr,
		"KI_SKIP_SERVER=1",
		"KI_LIVE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("playwright live: %v\n%s", err, out)
	}
}

func isolateLive(t *testing.T) (home, proj string) {
	t.Helper()
	key := liveDashScopeKey(t)
	home = t.TempDir()
	proj = t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("KI_FAKE", "")
	t.Setenv("KI_SERVER_ADDR", "")
	t.Setenv("DASHSCOPE_CN_API_KEY", key)
	t.Setenv("DASHSCOPE_API_KEY", key)
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(`{"version":1,"default":{"provider":"dashscope-cn","model":"qwen3.7-plus"},"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
	return home, proj
}

func liveDashScopeKey(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"DASHSCOPE_CN_API_KEY", "DASHSCOPE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	//nolint:gosec // this is the user's explicit private Ki configuration path.
	b, err := os.ReadFile(filepath.Join(userHome, ".ki", "credentials.json"))
	if err != nil {
		t.Skip("no dashscope-cn key; set DASHSCOPE_CN_API_KEY or configure it in Ki settings")
	}
	var credentials struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if json.Unmarshal(b, &credentials) == nil {
		for _, id := range []string{"dashscope-cn", "dashscope"} {
			if key := strings.TrimSpace(credentials.Providers[id].APIKey); key != "" {
				return key
			}
		}
	}
	t.Skip("no dashscope-cn key; set DASHSCOPE_CN_API_KEY or configure it in Ki settings")
	return ""
}
