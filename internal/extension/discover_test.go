package extension

import (
	"os"
	"path/filepath"
	"testing"

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

func TestDiscoverGlobalOnlyAndDisabledCatalog(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "alpha", `{
		"name":"alpha","capabilities":["prompt.append"],
		"prompt":{"append":["APPEND.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(home, "extensions", "alpha", "APPEND.md"), []byte("ALPHA"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePkg(t, filepath.Join(cwd, "extensions"), "beta", `{
		"name":"beta","capabilities":["prompt.append"],
		"prompt":{"append":["APPEND.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(cwd, "extensions", "beta", "APPEND.md"), []byte("BETA"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Name != "alpha" || !got.All[0].Enabled {
		t.Fatalf("default on: %+v", got.All)
	}
	layers := PromptLayers(got.Enabled)
	if len(layers) != 1 || layers[0].Text != "ALPHA" {
		t.Fatalf("layers %+v", layers)
	}
	got = Discover(home, session.Toggle{Disabled: []string{"alpha"}})
	layers = PromptLayers(got.Enabled)
	if len(layers) != 0 {
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

func TestDiscoverIgnoresProjectExtension(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "dup", `{"name":"dup","capabilities":["skill"],"skills":["skills"]}`)
	writePkg(t, filepath.Join(cwd, "extensions"), "dup", `{"name":"dup","capabilities":["skill"],"skills":["skills"]}`)
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Name != "dup" {
		t.Fatalf("%+v", got.All)
	}
}

func TestLifecycleCapabilityLoads(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "onlytool", `{
		"name":"onlytool","capabilities":["lifecycle"],
		"runtime":{"kind":"rpc","command":"bin/x"}
	}`)
	d := Discover(home, session.Toggle{})
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
	got := Discover(home, session.Toggle{})
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
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Error == "" {
		t.Fatalf("provider without sidecar should fail: %+v", got.All)
	}
}

func TestEnabledChainOrderMatchesDiscover(t *testing.T) {
	// Global catalog and chain order are both name-sorted.
	home := t.TempDir()
	cwd := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "zeta", `{
		"name":"zeta","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(home, "extensions", "zeta", "A.md"), []byte("Z"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePkg(t, filepath.Join(cwd, "extensions"), "alpha", `{
		"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}
	}`)
	if err := os.WriteFile(filepath.Join(cwd, "extensions", "alpha", "A.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Name != "zeta" {
		t.Fatalf("All should be name-sorted: %+v", namesOf(got.All))
	}
	if len(got.Enabled) != 1 || got.Enabled[0].Name != "zeta" {
		t.Fatalf("Discover.Enabled want zeta: %+v", namesOf(got.Enabled))
	}
	// Server feeds Prepare with Enabled(snapshot.Extensions) where Extensions is All.
	ordered := Enabled(got.All, session.Toggle{})
	if len(ordered) != 1 || ordered[0].Name != "zeta" {
		t.Fatalf("Enabled(All) must match Discover chain, got %v", namesOf(ordered))
	}
	for i := range ordered {
		if ordered[i].Name != got.Enabled[i].Name {
			t.Fatalf("Enabled vs Discover.Enabled mismatch at %d: %+v vs %+v", i, ordered[i], got.Enabled[i])
		}
	}
	layers := PromptLayers(ordered)
	if len(layers) != 1 || layers[0].Text != "Z" {
		t.Fatalf("prompt chain order %+v", layers)
	}
}

func namesOf(ds []Descriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}
