package extension

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ki/internal/session"
)

func buildTestSidecar(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sidecar")
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "testdata", "extensions", "sidecar")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestStartRPCInitialize(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name:         "protected-paths",
		Path:         root,
		Enabled:      true,
		Capabilities: []string{"lifecycle"},
		root:         root,
		manifest: Manifest{
			Name:         "protected-paths",
			Capabilities: []string{"lifecycle"},
			Runtime:      RuntimeSpec{Kind: runtimeRPC, Command: bin},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if c.name != "protected-paths" {
		t.Fatalf("%s", c.name)
	}
	_ = os.Remove(bin)
}

func TestStartRPCDropsUndeclaredInitializeMembers(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name:         "onlyintercept",
		Path:         root,
		Enabled:      true,
		Capabilities: []string{"lifecycle"},
		root:         root,
		manifest: Manifest{
			Name:         "onlyintercept",
			Capabilities: []string{"lifecycle"},
			Runtime:      RuntimeSpec{Kind: runtimeRPC, Command: bin, Env: map[string]string{"KI_UNDECLARED": "1"}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if len(c.registration.Tools) != 0 || len(c.registration.Commands) != 0 {
		t.Fatalf("undeclared members kept: %+v", c.registration)
	}
	if len(c.undeclared) != 2 {
		t.Fatalf("undeclared %v", c.undeclared)
	}
}

func TestPrepareEmitsUndeclaredExtensionError(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name:         "onlyintercept",
		Path:         root,
		Enabled:      true,
		Capabilities: []string{"lifecycle"},
		root:         root,
		manifest: Manifest{
			Name:         "onlyintercept",
			Capabilities: []string{"lifecycle"},
			Runtime:      RuntimeSpec{Kind: runtimeRPC, Command: bin, Env: map[string]string{"KI_UNDECLARED": "1"}},
		},
	}
	var codes []string
	m := NewManager(t.TempDir(), func(_, name, capability, code, _ string) {
		codes = append(codes, name+":"+capability+":"+code)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools := m.Prepare(ctx, "sess", t.TempDir(), []Descriptor{d})
	defer m.CloseSession("sess")
	if len(tools) != 0 {
		t.Fatalf("tools %v", tools)
	}
	if len(codes) == 0 {
		t.Fatal("expected extension_error for undeclared initialize members")
	}
}

func TestPrepareOrderFollowsDiscoverEnabled(t *testing.T) {
	// Drive the same path server uses: Discover → Enabled(All) → Prepare → items.
	// Name-sorted All is alpha,zeta; chain (and intercept) is home zeta then project alpha.
	bin := buildTestSidecar(t)
	home := t.TempDir()
	cwd := t.TempDir()
	for _, spec := range []struct {
		root, name string
	}{
		{filepath.Join(home, "extensions"), "zeta"},
		{filepath.Join(cwd, ".ki", "extensions"), "alpha"},
	} {
		dir := filepath.Join(spec.root, spec.name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{
			"name":         spec.name,
			"capabilities": []string{"lifecycle"},
			"runtime":      map[string]any{"kind": "rpc", "command": bin},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "extension.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := Discover(home, cwd, session.Toggle{})
	if len(got.Enabled) != 2 || got.Enabled[0].Name != "zeta" || got.Enabled[1].Name != "alpha" {
		t.Fatalf("Discover.Enabled %v", namesOf(got.Enabled))
	}
	// Mirror server: snapshot.Extensions is All; Prepare gets Enabled(All, toggle).
	chain := Enabled(got.All, session.Toggle{})
	m := NewManager(home, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.Prepare(ctx, "sess", cwd, chain)
	defer m.CloseSession("sess")
	items := m.items("sess")
	if len(items) != 2 {
		t.Fatalf("items %d: %+v", len(items), items)
	}
	if items[0].name != "zeta" || items[1].name != "alpha" {
		t.Fatalf("Prepare/items order %s,%s want zeta,alpha (Discover chain, not name-sorted All)", items[0].name, items[1].name)
	}
}

func TestResolveRuntimeCommand(t *testing.T) {
	root := "/pkg"
	if got := resolveRuntimeCommand(root, "node"); got != "node" {
		t.Fatalf("PATH name %q", got)
	}
	if got := resolveRuntimeCommand(root, "npx"); got != "npx" {
		t.Fatalf("PATH name %q", got)
	}
	got := resolveRuntimeCommand(root, "bin/extension")
	want := filepath.Join(root, "bin", "extension")
	if got != want {
		t.Fatalf("package-relative %q want %q", got, want)
	}
	abs := filepath.Join(root, "sidecar")
	if got := resolveRuntimeCommand(root, abs); got != abs {
		t.Fatalf("absolute %q", got)
	}
}

func TestRuntimeInstallRunsBeforeStart(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	marker := filepath.Join(root, "installed")
	d := Descriptor{
		Name: "protected-paths", Path: root, Enabled: true,
		Capabilities: []string{"lifecycle"}, root: root,
		manifest: Manifest{
			Name: "protected-paths", Capabilities: []string{"lifecycle"},
			Runtime: RuntimeSpec{
				Kind: runtimeRPC, Command: bin,
				Install: []string{"sh", "-c", "echo yes > installed"},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "yes\n" && string(body) != "yes\r\n" {
		t.Fatalf("install marker %q", body)
	}
}

func TestRuntimeInstallFailureStopsSidecar(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name: "protected-paths", Path: root, Enabled: true,
		Capabilities: []string{"lifecycle"}, root: root,
		manifest: Manifest{
			Name: "protected-paths", Capabilities: []string{"lifecycle"},
			Runtime: RuntimeSpec{
				Kind: runtimeRPC, Command: bin,
				Install: []string{"false"},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil); err == nil {
		t.Fatal("expected install error")
	}
}
