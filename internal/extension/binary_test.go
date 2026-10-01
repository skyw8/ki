package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ki/internal/session"
	"ki/internal/state"
)

var bundledGoNames = []string{"codex-oauth", "deep-web-search", "freerouter", "goal", "telegram-bot"}

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
	if d.Name == "zvec-grep" {
		alias := filepath.Join("bin", "zg")
		if runtime.GOOS == "windows" {
			alias += ".exe"
		}
		assets[alias] = true
	}
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
	t.Helper() // A private empty PATH proves no source-language interpreter or compiler is used.
	emptyPath := t.TempDir()
	d.manifest.Runtime.Env = map[string]string{"PATH": emptyPath, "HOME": t.TempDir(), "USERPROFILE": t.TempDir()}
	if d.Name == "freerouter" {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		t.Cleanup(upstream.Close)
		if err := state.WriteVersioned(filepath.Join(d.root, "config.json"), 1, map[string]any{"baseUrl": upstream.URL, "listen": "127.0.0.1:0"}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	case "freerouter":
		if len(reg.Tools) != 0 || len(reg.Commands) != 0 || len(reg.Subscriptions) != 0 || reg.Fallback || len(d.Providers) != 1 || d.Providers[0].ID != "free-router" {
			t.Fatal(reg, d.Providers)
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
	case "zvec-grep":
		if len(reg.Tools) != 1 || reg.Tools[0].Name != "zvec_grep_search" {
			t.Fatal(reg)
		}
		if reg.Tools[0].Parameters["type"] != "object" || reg.Tools[0].Parameters["properties"] == nil {
			t.Fatal("missing native search schema", reg.Tools[0])
		}
		names := []string{}
		for _, cmd := range reg.Commands {
			names = append(names, cmd.Name)
		}
		slices.Sort(names)
		assertJSONEqual(t, names, []string{"zg-index", "zg-remove", "zg-status"})
		if got := d.PromptAppendFiles(); len(got) != 1 || got[0] != "prompt/APPEND.md" {
			t.Fatal(got)
		}
		if dirs := PathDirs([]Descriptor{d}); len(dirs) != 1 || dirs[0] != filepath.Join(d.root, "bin") {
			t.Fatal(dirs)
		}
		alias := filepath.Join(d.root, "bin", "zg")
		if runtime.GOOS == "windows" {
			alias += ".exe"
		}
		cli := exec.CommandContext(ctx, alias, "--help")
		cli.Dir = t.TempDir()
		cli.Env = sidecarEnv(d, "", home, cli.Dir)
		if data, err := cli.CombinedOutput(); err != nil || !strings.Contains(strings.ToLower(string(data)), "usage:") {
			t.Fatalf("packaged zg alias: %v\n%s", err, data)
		}
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
	// Closing stdin after one request must still flush its reply before exit.
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

// Native C++/Rust builds are kept out of the ordinary Go suite. The dedicated
// native CI job supplies the already-built executable and exercises the same
// host launch contract after copying only distributable assets into a new home.
func TestBundledRustBinaryWithoutSource(t *testing.T) {
	compiled := os.Getenv("KI_ZVEC_GREP_RUNTIME")
	if compiled == "" {
		t.Skip("native Rust suite supplies KI_ZVEC_GREP_RUNTIME")
	}
	compiled, err := filepath.Abs(compiled)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	destination := filepath.Join(home, "extensions", "zvec-grep")
	source := filepath.Join(root, "extensions", "zvec-grep")
	data, err := os.ReadFile(filepath.Join(source, "extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	assets := []string{"extension.json"}
	for _, file := range manifest.I18n.Resources {
		assets = append(assets, file)
	}
	assets = append(assets, manifest.Prompt.Append...)
	for _, file := range assets {
		sourceFile := filepath.Join(source, filepath.FromSlash(file))
		data, err := os.ReadFile(sourceFile)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(destination, filepath.FromSlash(file))
		if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binaryData, err := os.ReadFile(compiled)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zvec-grep", "zg"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		file := filepath.Join(destination, "bin", name)
		if err = os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, binaryData, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	discovery := Discover(home, session.Toggle{})
	if len(discovery.All) != 1 {
		t.Fatal(discovery)
	}
	d := discovery.All[0]
	assertBinaryPackage(t, d)
	assertPackagedInitialize(t, root, home, d)
}

func TestBundledGoSourceFallbackOutsideRepository(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	output := filepath.Join(home, "extensions")
	command := exec.Command("go", "run", "./scripts/build-extensions.go", "-source", "-out", output, "-only", strings.Join(bundledGoNames, ","))
	command.Dir = root
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("stage standalone source packages: %v\n%s", err, data)
	}
	discovery := Discover(home, session.Toggle{})
	if len(discovery.All) != len(bundledGoNames) {
		t.Fatal(discovery)
	}
	for _, d := range discovery.All {
		t.Run(d.Name, func(t *testing.T) {
			if d.Error != "" {
				t.Fatal(d.Error)
			}
			for _, file := range []string{"go.mod", "go.sum", "main.go", "install/main.go", "_ki/go.mod", "_ki/pkg/extensionbuild/install.go"} {
				if _, err := os.Stat(filepath.Join(d.root, filepath.FromSlash(file))); err != nil {
					t.Fatal(file, err)
				}
			}
			if err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				if strings.HasSuffix(entry.Name(), "_test.go") || entry.Name() == "config.json" || entry.Name() == "cache.json" || strings.HasSuffix(entry.Name(), ".exe") {
					t.Errorf("source package leaked runtime/test artifact: %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if d.Name == "freerouter" {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":[]}`))
				}))
				defer upstream.Close()
				if err := state.WriteVersioned(filepath.Join(d.root, "config.json"), 1, map[string]any{"baseUrl": upstream.URL, "listen": "127.0.0.1:0"}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			d.manifest.Runtime.Env = map[string]string{"GOWORK": "off"}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			client, err := startRPC(ctx, d, "", home, t.TempDir(), nil)
			if err != nil {
				t.Fatalf("install and initialize standalone source package: %v", err)
			}
			if err := client.call(ctx, "shutdown", map[string]any{}, nil); err != nil {
				t.Fatal(err)
			}
			client.close()
			binary := resolveRuntimeCommand(d.root, d.manifest.Runtime.Command)
			if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(binary), ".exe") {
				binary += ".exe"
			}
			if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
				t.Fatal("missing built executable", binary, err)
			}
			// Once compiled, the same source package must launch with no toolchain too.
			assertPackagedInitialize(t, root, home, d)
		})
	}
}
