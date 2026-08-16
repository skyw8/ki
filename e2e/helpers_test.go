package e2e

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/cli"
	"ki/internal/server"
	"ki/internal/session"
)

const pdfMarker = "KI-PDF-MARKER-42"

var sessionIDRe = regexp.MustCompile(`(?:session_id:|session )\s*([0-9a-f]{32})`)

func isolate(t *testing.T) (home, proj string) {
	t.Helper()
	home = t.TempDir()
	proj = t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("KI_FAKE", "1")
	t.Setenv("KI_SERVER_ADDR", "")
	t.Chdir(proj)
	return home, proj
}

func runKI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	code = cli.Main(args)
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	so, _ := io.ReadAll(rOut)
	se, _ := io.ReadAll(rErr)
	_ = rOut.Close()
	_ = rErr.Close()
	return string(so), string(se), code
}

func mustSessionID(t *testing.T, stdout, stderr string) string {
	t.Helper()
	for _, s := range []string{stdout, stderr} {
		if m := sessionIDRe.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no session id\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	return ""
}

func sessionDir(t *testing.T, home, id string) string {
	t.Helper()
	dir, err := session.Find(filepath.Join(home, "sessions"), id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func readJSONL(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readSessionConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeRedPNG(t *testing.T, path string) {
	t.Helper()
	writeSolidPNG(t, path, color.RGBA{R: 200, G: 16, B: 16, A: 255})
}

func writeBluePNG(t *testing.T, path string) {
	t.Helper()
	writeSolidPNG(t, path, color.RGBA{R: 16, G: 16, B: 200, A: 255})
}

func writeSolidPNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeMarkerPDF(t *testing.T, path, marker string) {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 18 Tf 20 60 Td (%s) Tj ET\n", marker)
	pdf := fmt.Sprintf("%%PDF-1.1\n"+
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n"+
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n"+
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 144]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n"+
		"4 0 obj<</Length %d>>stream\n%s\nendstream\nendobj\n"+
		"5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n"+
		"trailer<</Root 1 0 R>>\n%%%%EOF\n", len(stream), stream)
	if err := os.WriteFile(path, []byte(pdf), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHomeTOML(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

var (
	kiBinOnce sync.Once
	kiBin     string
	kiBinErr  error
)

func builtKI(t *testing.T) string {
	t.Helper()
	kiBinOnce.Do(func() {
		goPath, err := exec.LookPath("go")
		if err != nil {
			for _, c := range []string{
				"/home/hgy/sdk/go/bin/go",
				"/usr/local/go/bin/go",
			} {
				if _, e := os.Stat(c); e == nil {
					goPath = c
					err = nil
					break
				}
			}
		}
		if err != nil || goPath == "" {
			kiBinErr = fmt.Errorf("go toolchain not found")
			return
		}
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			kiBinErr = fmt.Errorf("runtime.Caller failed")
			return
		}
		root := filepath.Dir(filepath.Dir(file))
		out := filepath.Join(os.TempDir(), "ki-e2e-bin")
		cmd := exec.Command(goPath, "build", "-o", out, "./cmd/ki")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(goPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
		if b, err := cmd.CombinedOutput(); err != nil {
			kiBinErr = fmt.Errorf("go build: %v\n%s", err, b)
			return
		}
		kiBin = out
	})
	if kiBinErr != nil {
		t.Fatal(kiBinErr)
	}
	return kiBin
}

func runBin(t *testing.T, home string, args ...string) (out string, code int) {
	t.Helper()
	cmd := exec.Command(builtKI(t), args...)
	cmd.Env = childEnv(home)
	b, err := cmd.CombinedOutput()
	out = string(b)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, ee.ExitCode()
		}
		t.Fatalf("run %v: %v\n%s", args, err, out)
	}
	return out, 0
}

func childEnv(home string) []string {
	env := os.Environ()
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "KI_HOME=") || strings.HasPrefix(e, "KI_FAKE=") || strings.HasPrefix(e, "KI_SERVER_ADDR=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "KI_HOME="+home, "KI_FAKE=1")
	return out
}

func liveChildEnv(home string) []string {
	env := os.Environ()
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "KI_HOME=") || strings.HasPrefix(e, "KI_FAKE=") || strings.HasPrefix(e, "KI_SERVER_ADDR=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "KI_HOME="+home, "KI_FAKE=")
	return out
}

func startServe(t *testing.T, home string) server.File {
	t.Helper()
	return startServeEnv(t, home, "", childEnv(home))
}

func startServeLive(t *testing.T, home, dir string) server.File {
	t.Helper()
	return startServeEnv(t, home, dir, liveChildEnv(home))
}

func startServeEnv(t *testing.T, home, dir string, env []string) server.File {
	t.Helper()
	cmd := exec.Command(builtKI(t), "serve", "--addr", "127.0.0.1:0")
	cmd.Env = env
	if dir != "" {
		cmd.Dir = dir
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sf, err := server.ReadServerFile(home)
		if err == nil && sf.Addr != "" {
			return sf
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server.json not written")
	return server.File{}
}
