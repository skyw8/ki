package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	args = append([]string{"run"}, args...)
	return runCommand(t, args...)
}

func runCommand(t *testing.T, args ...string) (stdout, stderr string, code int) {
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
	//nolint:gosec // dir is a session directory returned by the test runtime.
	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readSessionConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	//nolint:gosec // dir is a session directory returned by the test runtime.
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

var (
	kiBinOnce        sync.Once
	kiBin            string
	errKiBin         error
	errGoToolchain   = errors.New("go toolchain not found")
	errRuntimeCaller = errors.New("runtime.Caller failed")
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
			errKiBin = errGoToolchain
			return
		}
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			errKiBin = errRuntimeCaller
			return
		}
		root := filepath.Dir(filepath.Dir(file))
		out := filepath.Join(os.TempDir(), "ki-e2e-bin")
		//nolint:gosec // goPath is resolved from the local Go toolchain for this e2e build.
		cmd := exec.CommandContext(t.Context(), goPath, "build", "-o", out, "./cmd/ki")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(goPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
		if b, err := cmd.CombinedOutput(); err != nil {
			errKiBin = fmt.Errorf("go build: %w\n%s", err, b)
			return
		}
		kiBin = out
	})
	if errKiBin != nil {
		t.Fatal(errKiBin)
	}
	return kiBin
}

func runBin(t *testing.T, home string, args ...string) (out string, code int) {
	t.Helper()
	// The executable is the test-built Ki binary; args are controlled by the test.
	//nolint:gosec // this subprocess is intentionally the e2e system under test
	cmd := exec.CommandContext(t.Context(), builtKI(t), args...)
	cmd.Env = childEnv(home)
	b, err := cmd.CombinedOutput()
	out = string(b)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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

func startServe(t *testing.T, home string) server.File {
	t.Helper()
	return startServeEnv(t, home, "", childEnv(home))
}

func startServeEnv(t *testing.T, home, dir string, env []string) server.File {
	t.Helper()
	// The executable is the test-built Ki binary and the address is fixed below.
	//nolint:gosec // this subprocess is intentionally the e2e system under test
	cmd := exec.CommandContext(t.Context(), builtKI(t), "serve", "--addr", "127.0.0.1:0")
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

func serveJSON(t *testing.T, sf server.File, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+sf.Addr+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+sf.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return res.StatusCode, out
}

func waitSessionRunning(t *testing.T, sf server.File, id string, running bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, got := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
		if status == http.StatusOK {
			if on, _ := got["running"].(bool); on == running {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s running=%v not reached", id, running)
}
