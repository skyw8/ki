//go:build live

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	writeHomeTOML(t, home, ""+
		"[defaults]\n"+
		"provider = \"dashscope-cn\"\n"+
		"model = \"qwen3.7-plus\"\n"+
		"\n"+
		"[providers.dashscope-cn]\n"+
		"api_key = \""+key+"\"\n"+
		"base_url = \"https://dashscope.aliyuncs.com/compatible-mode/v1\"\n")
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
	b, err := os.ReadFile(filepath.Join(userHome, ".ki", "ki.toml"))
	if err != nil {
		t.Skip("no dashscope-cn key; set DASHSCOPE_CN_API_KEY or ~/.ki/ki.toml")
	}
	section := ""
	fallback := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if !strings.HasPrefix(line, "api_key") {
			continue
		}
		_, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v == "" {
			continue
		}
		if section == "providers.dashscope-cn" {
			return v
		}
		if section == "providers.dashscope" && fallback == "" {
			fallback = v
		}
	}
	if fallback != "" {
		return fallback
	}
	t.Skip("no dashscope-cn key; set DASHSCOPE_CN_API_KEY or ~/.ki/ki.toml")
	return ""
}
