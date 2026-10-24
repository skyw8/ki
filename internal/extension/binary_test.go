package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"ki/internal/session"
)

var bundledGoNames = []string{"codex-oauth", "deep-web-search", "goal", "telegram-bot"}

func TestBundledGoBinariesWithoutSource(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	output := filepath.Join(home, "extensions")
	command := exec.Command("go", "run", "./scripts/build-extensions.go", "-out", output, "-only", strings.Join(bundledGoNames, ","))
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build distributable Go extensions: %v\n%s", err, data)
	}
	discovery := Discover(home, session.Toggle{})
	if len(discovery.All) != len(bundledGoNames) {
		t.Fatalf("packages=%d want=%d: %+v", len(discovery.All), len(bundledGoNames), discovery.All)
	}
	for _, d := range discovery.All {
		t.Run(d.Name, func(t *testing.T) { assertBinaryPackage(t, d); assertPackagedInitialize(t, root, home, d) })
	}
}
func assertBinaryPackage(t *testing.T, d Descriptor) {
	t.Helper()
	if d.Error != "" {
		t.Fatal(d.Error)
	}
	if !reflect.DeepEqual(d.manifest.Runtime.Install, []string{"go", "run", "./install/main.go"}) || d.manifest.Runtime.InstallWhen != "missing" || d.manifest.Runtime.Command != "bin/"+d.Name {
		t.Fatalf("runtime must launch its binary directly: %+v", d.manifest.Runtime)
	}
	assets := map[string]bool{"extension.json": true}
	for _, path := range d.manifest.I18n.Resources {
		assets[filepath.FromSlash(path)] = true
	}
	for _, path := range d.manifest.Prompt.Append {
		assets[filepath.FromSlash(path)] = true
	}
	binary := filepath.Join("bin", d.Name)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	assets[binary] = true
	seen := map[string]bool{}
	err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(d.root, path)
		if err != nil {
			return err
		}
		if !assets[relative] {
			t.Errorf("package contains unneeded launch data/source: %s", relative)
		}
		if !entry.Type().IsRegular() {
			t.Errorf("package contains non-regular file: %s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for asset := range assets {
		if !seen[asset] {
			t.Errorf("missing packaged asset %s", asset)
		}
	}
	assertI18nParity(t, d)
}
func assertPackagedInitialize(t *testing.T, root, home string, d Descriptor) {
	t.Helper()
	emptyPath := t.TempDir()
	d.manifest.Runtime.Env = map[string]string{"PATH": emptyPath, "HOME": t.TempDir(), "USERPROFILE": t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	client, err := startRPC(ctx, d, "", home, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("source-free initialize with empty PATH: %v", err)
	}
	defer client.close()
	reg := client.registration
	if len(client.undeclared) != 0 {
		t.Errorf("registration violates declared capabilities: %v", client.undeclared)
	}
	switch d.Name {
	case "codex-oauth":
		assertJSONEqual(t, reg.Providers, d.Providers)
		if len(reg.Tools) != 0 || len(reg.Commands) != 0 {
			t.Fatal(reg)
		}
	case "goal":
		assertEmbeddedTools(t, root, d.Name, "tools.json", reg.Tools)
		assertJSONEqual(t, reg.Commands, []CommandSpec{{Name: "goal", Description: "Run a goal to completion", ArgumentHint: "<objective>", Completions: []string{"pause", "resume", "clear", "edit", "status"}}})
		assertJSONEqual(t, reg.Subscriptions, []Subscription{{Event: "before_agent_start", Mode: "sync"}, {Event: "agent_settled", Mode: "async"}})
	case "deep-web-search":
		assertEmbeddedTools(t, root, d.Name, "tools.json", reg.Tools)
		if len(reg.Commands) != 0 || len(reg.Subscriptions) != 0 {
			t.Fatal(reg)
		}
	case "telegram-bot":
		if len(reg.Tools) != 0 || len(reg.Commands) != 0 {
			t.Fatal(reg)
		}
		want := []Subscription{}
		for _, event := range []string{"message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "agent_settled", "run_aborted", "queue_changed"} {
			want = append(want, Subscription{Event: event, Mode: "async"})
		}
		assertJSONEqual(t, reg.Subscriptions, want)
	}
	if err := client.call(ctx, "shutdown", map[string]any{}, nil); err != nil {
		t.Errorf("source-free shutdown: %v", err)
	}
	assertPipedInitialize(t, home, d)
}
func assertPipedInitialize(t *testing.T, home string, d Descriptor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cwd := t.TempDir()
	params := map[string]any{"sessionId": "", "cwd": cwd, "home": home, "extensionRoot": d.root, "capabilities": d.Capabilities, "scope": map[string]any{"global": true, "provider": true}, "providers": d.Providers}
	input, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "piped-initialize", "method": "initialize", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, resolveRuntimeCommand(d.root, d.manifest.Runtime.Command), d.manifest.Runtime.Args...)
	command.Dir = cwd
	command.Env = sidecarEnv(d, "", home, cwd)
	command.Stdin = bytes.NewReader(append(input, '\n'))
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("piped source-free initialize: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(&output)
	for {
		var reply struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := decoder.Decode(&reply); err != nil {
			if err == io.EOF {
				t.Fatalf("piped initialize reply lost at EOF; stderr=%s", stderr.String())
			}
			t.Fatal("invalid piped initialize reply", err)
		}
		if reply.ID != "piped-initialize" {
			continue
		}
		if len(reply.Result) == 0 || len(reply.Error) != 0 {
			t.Fatalf("piped initialize failed: result=%s error=%s", reply.Result, reply.Error)
		}
		return
	}
}
func assertEmbeddedTools(t *testing.T, root, name, file string, actual []ToolSpec) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "extensions", name, file))
	if err != nil {
		t.Fatal(err)
	}
	var wanted []ToolSpec
	if err = json.Unmarshal(data, &wanted); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, actual, wanted)
}
func assertJSONEqual(t *testing.T, actual, wanted any) {
	t.Helper()
	normalize := func(v any) any {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var normalized any
		if err = json.Unmarshal(raw, &normalized); err != nil {
			t.Fatal(err)
		}
		return normalized
	}
	if !reflect.DeepEqual(normalize(actual), normalize(wanted)) {
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(wanted)
		t.Fatalf("got %s\nwant %s", a, b)
	}
}
