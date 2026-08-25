package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ki/internal/provider"
	"ki/internal/server"
)

func TestExtensionDeclarativeAppend(t *testing.T) {
	home, proj := isolate(t)
	dir := filepath.Join(home, "extensions", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), []byte("KEEP-OUT"), 0o600); err != nil {
		t.Fatal(err)
	}
	sf := startServe(t, home)
	status, created := serveJSON(t, sf, http.MethodPost, "/v1/sessions", map[string]any{"cwd": proj})
	if status != http.StatusOK {
		t.Fatalf("create %d %+v", status, created)
	}
	id, _ := created["id"].(string)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatalf("prompt %d", status)
	}
	waitAgentEndHTTP(t, sf, id)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, "KEEP-OUT") {
		t.Fatalf("append missing:\n%s", raw)
	}
}

func TestExtensionAppendInterceptAndDisable(t *testing.T) {
	home, proj := isolate(t)
	bin := buildSidecar(t)
	installProtected(t, home, bin)
	marker := filepath.Join(t.TempDir(), "spawned")
	sf := startServe(t, home)

	status, listed := serveJSON(t, sf, http.MethodGet, "/v1/extensions", nil)
	if status != http.StatusOK {
		t.Fatalf("GET extensions %d %+v", status, listed)
	}
	items, _ := listed["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items %+v", listed)
	}

	status, created := serveJSON(t, sf, http.MethodPost, "/v1/sessions", map[string]any{"cwd": proj})
	if status != http.StatusOK {
		t.Fatalf("create %d %+v", status, created)
	}
	id, _ := created["id"].(string)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "hello"})
	if status != http.StatusAccepted {
		t.Fatalf("prompt %d", status)
	}
	waitIdle(t, sf, id)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, "extension_instructions") || !strings.Contains(raw, "KEEP-OUT") {
		t.Fatalf("append missing from header:\n%s", raw)
	}

	envPath := filepath.Join(proj, ".env")
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": provider.WriteEnvToken})
	if status != http.StatusAccepted {
		t.Fatalf("write-env %d", status)
	}
	waitIdle(t, sf, id)
	if _, err := os.Stat(envPath); err == nil {
		t.Fatal(".env was written; intercept should block")
	}
	installSpawner(t, home, marker)
	status, patched := serveJSON(t, sf, http.MethodPatch, "/v1/extensions", map[string]any{"disabled": []string{"spawner"}})
	if status != http.StatusOK {
		t.Fatalf("disable %d %+v", status, patched)
	}
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "after-disable"})
	if status != http.StatusAccepted {
		t.Fatalf("disabled prompt %d", status)
	}
	waitIdle(t, sf, id)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("disabled extension sidecar spawned")
	}
	_, sess := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
	exts, _ := sess["availableExtensions"].([]any)
	if len(exts) == 0 {
		t.Fatal("availableExtensions missing")
	}
}

func TestExtensionAbortKillsSidecarGrandchild(t *testing.T) {
	home, proj := isolate(t)
	bin := buildSidecar(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	installProtected(t, home, bin, map[string]string{"KI_GRANDCHILD_PID_FILE": pidFile})
	sf := startServe(t, home)
	status, created := serveJSON(t, sf, http.MethodPost, "/v1/sessions", map[string]any{"cwd": proj})
	if status != http.StatusOK {
		t.Fatalf("create %d", status)
	}
	id, _ := created["id"].(string)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": provider.SleepInterceptToken})
	if status != http.StatusAccepted {
		t.Fatalf("sleep prompt %d", status)
	}
	waitSessionRunning(t, sf, id, true)
	pid := waitPIDFile(t, pidFile)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/abort", nil)
	if status != http.StatusOK {
		t.Fatalf("abort %d", status)
	}
	waitSessionRunning(t, sf, id, false)
	if runtime.GOOS == "windows" {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sidecar grandchild pid %d still alive after abort", pid)
}

func TestExtensionExecutableSlash(t *testing.T) {
	home, proj := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "prompts", "ship.md"), []byte("USER-SHIP-TEMPLATE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := buildSidecar(t)
	marker := filepath.Join(t.TempDir(), "invoke")
	installSlash(t, home, bin, map[string]string{"KI_INVOKE_MARKER": marker})
	sf := startServe(t, home)
	status, created := serveJSON(t, sf, http.MethodPost, "/v1/sessions", map[string]any{"cwd": proj})
	if status != http.StatusOK {
		t.Fatalf("create %d", status)
	}
	id, _ := created["id"].(string)

	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "/ship"})
	if status != http.StatusAccepted {
		t.Fatalf("user template /ship %d", status)
	}
	waitIdle(t, sf, id)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, "USER-SHIP-TEMPLATE") {
		t.Fatalf("user prompt should win:\n%s", raw)
	}
	if strings.Contains(raw, "SHIP-PROMPT") {
		t.Fatal("extension handler stole /ship from user prompts/*.md")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command.invoke ran for user template /ship")
	}

	status, handled := serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "/extnotice"})
	if status != http.StatusOK {
		t.Fatalf("notice %d %+v", status, handled)
	}
	if handled["handled"] != true || handled["notice"] != "SHIP-NOTICE" {
		t.Fatalf("notice %+v", handled)
	}
	_, sess := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
	if on, _ := sess["running"].(bool); on {
		t.Fatal("handled notice occupied the session")
	}

	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "/extprompt"})
	if status != http.StatusAccepted {
		t.Fatalf("prompt occupy %d", status)
	}
	waitIdle(t, sf, id)
	raw = readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, "SHIP-PROMPT") {
		t.Fatalf("prompt occupy missing:\n%s", raw)
	}

	_ = os.Remove(marker)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": provider.HoldToken})
	if status != http.StatusAccepted {
		t.Fatalf("hold %d", status)
	}
	waitSessionRunning(t, sf, id, true)
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "/extnotice"})
	if status != http.StatusConflict {
		t.Fatalf("busy slash want 409 got %d", status)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command.invoke ran while busy")
	}
	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/abort", nil)
	if status != http.StatusOK {
		t.Fatalf("abort %d", status)
	}
	waitSessionRunning(t, sf, id, false)
}

func waitIdle(t *testing.T, sf server.File, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, got := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
		if on, _ := got["running"].(bool); !on {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s still running", id)
}

func waitAgentEndHTTP(t *testing.T, sf server.File, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+sf.Addr+"/v1/sessions/"+id+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+sf.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev)
		if ev.Type == "agent_end" {
			return
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("events ended without agent_end")
}

func buildSidecar(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "extensions", "sidecar")
	bin := filepath.Join(t.TempDir(), "sidecar")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sidecar: %v\n%s", err, out)
	}
	return bin
}

func installProtected(t *testing.T, home, bin string, env ...map[string]string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", "protected-paths")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var extra map[string]string
	if len(env) > 0 {
		extra = env[0]
	}
	manifest := `{
  "name": "protected-paths",
  "capabilities": ["prompt.append", "lifecycle"],
  "prompt": {"append": ["APPEND.md"]},
  "runtime": {"kind": "rpc", "command": "` + strings.ReplaceAll(bin, `\`, `\\`) + `"` + runtimeEnvJSON(extra) + `}
}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "APPEND.md"), []byte("KEEP-OUT"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func installSlash(t *testing.T, home, bin string, env map[string]string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", "slashy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "slashy",
  "capabilities": ["command"],
  "runtime": {"kind": "rpc", "command": "` + strings.ReplaceAll(bin, `\`, `\\`) + `"` + runtimeEnvJSON(env) + `}
}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runtimeEnvJSON(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	b, _ := json.Marshal(env)
	return `,"env":` + string(b)
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(b)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("grandchild pid file missing")
	return 0
}

func installSpawner(t *testing.T, home, marker string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", "spawner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := buildSidecar(t)
	manifest := `{
  "name": "spawner",
  "capabilities": ["lifecycle"],
  "runtime": {"kind": "rpc", "command": "` + strings.ReplaceAll(bin, `\`, `\\`) + `", "env": {"KI_MARKER": "` + strings.ReplaceAll(marker, `\`, `\\`) + `"}}
}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}
