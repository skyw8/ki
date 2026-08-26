package extension

import (
	"os"
	"path/filepath"
	"testing"

	"ki/internal/mcp"
	"ki/internal/session"
)

func writePkg(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverDefaultEnabledAndDisabledOmitsMerge(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "alpha", `{
		"name":"alpha","capabilities":["prompt.append"],
		"prompt":{"append":["APPEND.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(home, "extensions", "alpha", "APPEND.md"), []byte("ALPHA"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePkg(t, filepath.Join(cwd, ".ki", "extensions"), "beta", `{
		"name":"beta","capabilities":["prompt.append"],
		"prompt":{"append":["APPEND.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(cwd, ".ki", "extensions", "beta", "APPEND.md"), []byte("BETA"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, cwd, session.Toggle{})
	if len(got.All) != 2 || !got.All[0].Enabled {
		t.Fatalf("default on: %+v", got.All)
	}
	layers := PromptLayers(got.Enabled)
	if len(layers) != 2 || layers[0].Text != "ALPHA" || layers[1].Text != "BETA" {
		t.Fatalf("layers %+v", layers)
	}
	got = Discover(home, cwd, session.Toggle{Disabled: []string{"alpha"}})
	layers = PromptLayers(got.Enabled)
	if len(layers) != 1 || layers[0].ExtensionID != "beta" {
		t.Fatalf("disabled alpha: %+v", layers)
	}
	var listed bool
	for _, d := range got.All {
		if d.Name == "alpha" && !d.Enabled {
			listed = true
		}
	}
	if !listed {
		t.Fatal("disabled still listed")
	}
}

func TestDiscoverProjectReplacesGlobalName(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "dup", `{"name":"dup","capabilities":["skill"],"skills":["skills"]}`)
	writePkg(t, filepath.Join(cwd, ".ki", "extensions"), "dup", `{"name":"dup","capabilities":["skill"],"skills":["skills"]}`)
	got := Discover(home, cwd, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Scope != "project" {
		t.Fatalf("%+v", got.All)
	}
}

func TestMergeMCPUserWinsAndPrefixHelper(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "time", `{
		"name":"time","capabilities":["mcp"],
		"mcp":{"mcpServers":{"clock":{"command":"true"},"dup":{"command":"ext"}}}
	}`)
	base := mcp.File{MCPServers: map[string]mcp.ServerSpec{"dup": {Command: "user"}}, Sources: map[string]string{"dup": "home"}}
	d := Discover(home, t.TempDir(), session.Toggle{})
	merged := MergeMCP(base, d.Enabled)
	if merged.MCPServers["dup"].Command != "user" {
		t.Fatalf("user mcp should win: %+v", merged)
	}
	if merged.Sources["clock"] != "extension:time" {
		t.Fatalf("source %+v", merged.Sources)
	}
	prefixed := PrefixMCPTools([]mcp.ToolDefinition{{Name: "now"}}, "time")
	if len(prefixed) != 1 || prefixed[0].Name != "time/now" || prefixed[0].WireName != "now" {
		t.Fatalf("%+v", prefixed)
	}
}

func TestLifecycleCapabilityLoads(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "onlytool", `{
		"name":"onlytool","capabilities":["lifecycle"],
		"runtime":{"kind":"rpc","command":"bin/x"}
	}`)
	d := Discover(home, t.TempDir(), session.Toggle{})
	if len(d.Enabled) != 1 || !hasKind(d.Enabled[0].Capabilities, CapLifecycle) {
		t.Fatalf("%+v", d.Enabled)
	}
}

func TestDiscoverProviderCapabilityLoadsCatalog(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "codex", `{
		"name":"codex","capabilities":["provider"],
		"providers":[{"id":"codex","name":"Codex","api":"openai-codex-responses","baseUrl":"https://chatgpt.com/backend-api","auth":{"type":"oauth","subscription":true},"models":[{"id":"codex-mini","contextWindow":128000,"maxTokens":16384,"input":["text"]}]}],
		"runtime":{"kind":"rpc","command":"bin/codex"}
	}`)
	got := Discover(home, t.TempDir(), session.Toggle{})
	if len(got.Enabled) != 1 || got.Enabled[0].Error != "" {
		t.Fatalf("provider discovery=%+v", got.Enabled)
	}
	d := got.Enabled[0]
	if !hasKind(d.Capabilities, CapProvider) || len(d.Providers) != 1 || d.Providers[0].Auth.Type != "oauth" {
		t.Fatalf("provider descriptor=%+v", d)
	}
}

func TestDiscoverRejectsProviderWithoutSidecar(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "codex", `{
		"name":"codex","capabilities":["provider"],
		"providers":[{"id":"codex","name":"Codex","api":"openai-codex-responses","baseUrl":"https://chatgpt.com/backend-api","models":[{"id":"codex-mini","contextWindow":128000,"maxTokens":16384,"input":["text"]}]}]
	}`)
	got := Discover(home, t.TempDir(), session.Toggle{})
	if len(got.All) != 1 || got.All[0].Error == "" {
		t.Fatalf("provider without sidecar should fail: %+v", got.All)
	}
}

func TestEnabledChainOrderMatchesDiscover(t *testing.T) {
	// Name-sorted All would be alpha then zeta; chain order is home zeta then project alpha.
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "zeta", `{
		"name":"zeta","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(home, "extensions", "zeta", "A.md"), []byte("Z"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePkg(t, filepath.Join(cwd, ".ki", "extensions"), "alpha", `{
		"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(cwd, ".ki", "extensions", "alpha", "A.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, cwd, session.Toggle{})
	if len(got.All) != 2 || got.All[0].Name != "alpha" || got.All[1].Name != "zeta" {
		t.Fatalf("All should be name-sorted: %+v", namesOf(got.All))
	}
	if len(got.Enabled) != 2 || got.Enabled[0].Name != "zeta" || got.Enabled[0].Scope != "home" {
		t.Fatalf("Discover.Enabled want home zeta first: %+v", namesOf(got.Enabled))
	}
	if got.Enabled[1].Name != "alpha" || got.Enabled[1].Scope != "project" {
		t.Fatalf("Discover.Enabled want project alpha second: %+v", namesOf(got.Enabled))
	}
	// Server feeds Prepare with Enabled(snapshot.Extensions) where Extensions is All.
	ordered := Enabled(got.All, session.Toggle{})
	if len(ordered) != 2 || ordered[0].Name != "zeta" || ordered[1].Name != "alpha" {
		t.Fatalf("Enabled(All) must match Discover chain, got %v", namesOf(ordered))
	}
	for i := range ordered {
		if ordered[i].Name != got.Enabled[i].Name || ordered[i].Scope != got.Enabled[i].Scope {
			t.Fatalf("Enabled vs Discover.Enabled mismatch at %d: %+v vs %+v", i, ordered[i], got.Enabled[i])
		}
	}
	layers := PromptLayers(ordered)
	if len(layers) != 2 || layers[0].Text != "Z" || layers[1].Text != "A" {
		t.Fatalf("prompt chain order %+v", layers)
	}
}

func namesOf(ds []Descriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Scope + ":" + d.Name
	}
	return out
}
