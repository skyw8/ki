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

// DeepSeek is the live provider. It serves all three wire protocols from one
// credential: the built-in `deepseek` provider speaks Completions, and the two
// overlay providers below point the same model at its Responses and Anthropic
// endpoints. `--model` then selects the protocol per session.
const (
	liveModel                 = "deepseek/deepseek-flash"
	liveModelResponses        = "deepseek-responses/deepseek-flash"
	liveModelAnthropic        = "deepseek-anthropic/deepseek-flash"
	liveDeepSeekBase          = "https://api.deepseek.com"
	liveDeepSeekAnthropicBase = liveDeepSeekBase + "/anthropic"
)

func TestLivePing(t *testing.T) {
	for _, ref := range []string{liveModel, liveModelResponses, liveModelAnthropic} {
		t.Run(strings.ReplaceAll(ref, "/", "_"), func(t *testing.T) {
			home, proj := isolateLive(t)
			out, errOut, code := runKI(t, "--cwd", proj, "--model", ref,
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
		})
	}
}

func TestLiveImageAndPDF(t *testing.T) {
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

func TestLiveTwoImages(t *testing.T) {
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
		t.Skip("web/node_modules/@playwright/test missing; cd web && bun install")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found in PATH; install bun to run the WebUI suite")
	}
	home, proj := isolateLive(t)
	if err := os.WriteFile(filepath.Join(proj, "pw-live.txt"), []byte("KI-LIVE-MARKER-77\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sf := startServeLive(t, home, proj)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "x", "playwright", "test", "--project=live")
	cmd.Dir = webDir
	cmd.Env = append(liveChildEnv(home),
		"KI_BASE_URL=http://"+sf.Addr,
		"KI_SKIP_SERVER=1",
		"KI_LIVE=1",
		// global-setup records this for specs that need the fixture directory;
		// the live tool spec reads pw-live.txt from it.
		"KI_PW_CWD="+proj,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("playwright live: %v\n%s", err, out)
	}
}

func isolateLive(t *testing.T) (home, proj string) {
	t.Helper()
	key := liveDeepSeekKey(t)
	home = t.TempDir()
	proj = t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("KI_FAKE", "")
	t.Setenv("KI_SERVER_ADDR", "")
	t.Setenv("DEEPSEEK_API_KEY", key)
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(modelsJSON(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials.json"), []byte(credentialsJSON(t, key)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
	return home, proj
}

// modelsJSON pins the default to DeepSeek Completions and adds the Responses and
// Anthropic overlays so one home can run every protocol.
func modelsJSON(t *testing.T) string {
	t.Helper()
	seed := map[string]any{
		"id": "deepseek-flash", "name": "DeepSeek V4.1 Flash",
		"contextWindow": 1000000, "maxTokens": 384000,
		"input": []string{"text", "image"}, "reasoning": true,
		"cost": map[string]any{"input": 0.3, "output": 1.2, "cacheRead": 0.006, "cacheWrite": 0},
	}
	overlay := func(name, api, base string) map[string]any {
		return map[string]any{
			"name": name, "api": api, "baseUrl": base,
			"models": []any{seed},
		}
	}
	doc := map[string]any{
		"version": 1,
		"default": map[string]any{"provider": "deepseek", "model": "deepseek-flash"},
		"providers": map[string]any{
			"deepseek-responses": overlay("DeepSeek Responses", "responses", liveDeepSeekBase),
			"deepseek-anthropic": overlay("DeepSeek Anthropic", "anthropic", liveDeepSeekAnthropicBase),
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func credentialsJSON(t *testing.T, key string) string {
	t.Helper()
	entry := map[string]any{"apiKey": key}
	doc := map[string]any{
		"version": 1,
		"providers": map[string]any{
			"deepseek":           entry,
			"deepseek-responses": entry,
			"deepseek-anthropic": entry,
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func liveDeepSeekKey(t *testing.T) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); v != "" {
		return v
	}
	if key := deepSeekKeyFromCredentials(t); key != "" {
		return key
	}
	t.Skip("no deepseek key; set DEEPSEEK_API_KEY or configure it in Ki settings")
	return ""
}

func deepSeekKeyFromCredentials(t *testing.T) string {
	t.Helper()
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	//nolint:gosec // this is the user's explicit private Ki configuration path.
	b, err := os.ReadFile(filepath.Join(userHome, ".ki", "credentials.json"))
	if err != nil {
		return ""
	}
	var credentials struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if json.Unmarshal(b, &credentials) != nil {
		return ""
	}
	return strings.TrimSpace(credentials.Providers["deepseek"].APIKey)
}
