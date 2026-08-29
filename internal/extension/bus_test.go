package extension

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"ki/internal/session"
)

type mutexHost struct {
	m *Manager
}

func (mutexHost) CreateSession(SessionCreateRequest) (SessionCreateResult, error) {
	return SessionCreateResult{}, nil
}
func (mutexHost) NewSession(string, string) (SessionCreateResult, error) {
	return SessionCreateResult{}, nil
}
func (mutexHost) ReloadSession(string) error { return nil }
func (mutexHost) ListSessions(map[string]any) ([]SessionSnapshot, error) {
	return nil, nil
}
func (mutexHost) GetSession(string) (SessionSnapshot, error) { return SessionSnapshot{}, nil }

func (h mutexHost) Enqueue(string, string, EnqueueRequest) (EnqueueResult, error) {
	return EnqueueResult{}, nil
}
func (h mutexHost) Snapshot(string, string) (SessionSnapshot, error) { return SessionSnapshot{}, nil }
func (h mutexHost) AppendEntry(string, string, string, any) error    { return nil }
func (h mutexHost) AppendMessage(string, string, AppendMessageRequest) (AppendMessageResult, error) {
	return AppendMessageResult{}, nil
}
func (h mutexHost) Abort(string) error                             { return nil }
func (h mutexHost) Compact(string) error                           { return nil }
func (h mutexHost) PatchSession(string, string, string) error      { return nil }
func (h mutexHost) SetActiveTools(string, string, []string) error  { return nil }
func (h mutexHost) RegisterTools(string, string, []ToolSpec) error { return nil }
func (h mutexHost) UISetStatus(string, string, string, UIText, string) error {
	return nil
}
func (h mutexHost) UISetPanel(string, string, UIPanel) error               { return nil }
func (h mutexHost) UIClearPanel(string, string) error                      { return nil }
func (h mutexHost) GlobalUISetStatus(string, string, UIText, string) error { return nil }
func (h mutexHost) GlobalUISetPanel(string, UIPanel) error                 { return nil }
func (h mutexHost) GlobalUIClearPanel(string) error                        { return nil }
func (h mutexHost) UIConfirm(string, string, UIText, UIText) (bool, error) {
	return false, nil
}
func (h mutexHost) UISelect(string, string, UIText, []string) (string, error) {
	return "", nil
}
func (h mutexHost) BusEmit(sessionID, from, channel string, data any) (any, error) {
	return h.m.BusEmit(sessionID, from, channel, data)
}
func (h mutexHost) BusBroadcast(sessionID, from, channel string, data any) error {
	return h.m.BusBroadcast(sessionID, from, channel, data)
}

func TestBusBroadcastDoesNotWait(t *testing.T) {
	bin := buildMutexSidecar(t)
	home := t.TempDir()
	cwd := t.TempDir()
	m := NewManager(home, nil)
	m.SetHost(mutexHost{m: m})
	dir := filepath.Join(home, "extensions", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"name": "alpha", "capabilities": []string{"bus", "lifecycle"},
		"runtime": map[string]any{"kind": "rpc", "command": bin, "env": map[string]string{"KI_HOLD": "alpha"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, session.Toggle{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.Prepare(ctx, "sess", cwd, got.Enabled)
	defer m.Close()
	start := time.Now()
	if err := m.BusBroadcast("sess", "other", "workflow:mutex:v1", map[string]any{"busy": false}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("broadcast waited")
	}
}

func TestBusMutexHandshake(t *testing.T) {
	bin := buildMutexSidecar(t)
	home := t.TempDir()
	cwd := t.TempDir()
	m := NewManager(home, nil)
	host := mutexHost{m: m}
	m.SetHost(host)
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(home, "extensions", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"name": name, "capabilities": []string{"bus", "lifecycle"},
			"runtime": map[string]any{"kind": "rpc", "command": bin, "env": map[string]string{"KI_HOLD": name}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "extension.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := Discover(home, session.Toggle{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.Prepare(ctx, "sess", cwd, got.Enabled)
	defer m.Close()
	time.Sleep(50 * time.Millisecond)
	data, err := m.BusEmit("sess", "beta", "workflow:mutex:v1", map[string]any{"sessionId": "sess", "group": "agent-workflow", "busy": false})
	if err != nil {
		t.Fatal(err)
	}
	mdata, _ := data.(map[string]any)
	if mdata == nil {
		t.Fatalf("data %#v", data)
	}
	if busy, _ := mdata["busy"].(bool); !busy {
		t.Fatalf("expected busy true, got %#v", data)
	}
}

func buildMutexSidecar(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := `package main
import ("bufio"; "encoding/json"; "os")
type msg struct { JSONRPC string ` + "`json:\"jsonrpc\"`" + `; ID any ` + "`json:\"id,omitempty\"`" + `; Method string ` + "`json:\"method\"`" + `; Params json.RawMessage ` + "`json:\"params\"`" + ` }
func reply(id any, result any) { json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc":"2.0","id":id,"result":result}) }
func main() {
	hold := os.Getenv("KI_HOLD") == "alpha"
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte,0,64*1024), 8*1024*1024)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil { continue }
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"subscriptions":[]any{map[string]any{"event":"agent_end","mode":"async"}}})
		case "bus.event":
			var p struct { Channel string ` + "`json:\"channel\"`" + `; Data map[string]any ` + "`json:\"data\"`" + ` }
			_ = json.Unmarshal(m.Params, &p)
			if hold && p.Data != nil { p.Data["busy"] = true }
			reply(m.ID, map[string]any{"data": p.Data})
		default:
			if m.ID != nil { reply(m.ID, map[string]any{}) }
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module mutexsidecar\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "sidecar")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // builds the local mutex sidecar test fixture
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mutex sidecar: %v\n%s", err, out)
	}
	return bin
}
