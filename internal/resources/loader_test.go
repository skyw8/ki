package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextFilesStopAtGitRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	nested := filepath.Join(root, "pkg")
	home := filepath.Join(base, "home")
	for _, dir := range []string{filepath.Join(root, ".git"), nested, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(base, "AGENTS.md"), "OUTSIDE")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT")
	writeFile(t, filepath.Join(nested, "CLAUDE.md"), "NESTED")
	writeFile(t, filepath.Join(home, "AGENTS.md"), "GLOBAL")

	snapshot := NewLoader(home).Scan(nested)
	var contents []string
	for _, file := range snapshot.ContextFiles {
		contents = append(contents, file.Content)
	}
	got := strings.Join(contents, ",")
	if got != "GLOBAL,ROOT,NESTED" {
		t.Fatalf("context order/boundary = %q", got)
	}
}

func TestAppendSystemPromptsAreAdditive(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cwd := filepath.Join(base, "repo")
	writeFile(t, filepath.Join(home, "prompt", "APPEND_SYSTEM.md"), "GLOBAL")

	loader := NewLoader(home)
	if got := appendText(loader.Scan(cwd)); got != "GLOBAL" {
		t.Fatalf("global append system prompt = %q, want GLOBAL", got)
	}

	writeFile(t, filepath.Join(cwd, ".ki", "prompt", "APPEND_SYSTEM.md"), "PROJECT")
	snapshot := loader.Scan(cwd)
	if got := appendText(snapshot); got != "GLOBAL\n\nPROJECT" {
		t.Fatalf("additive append system prompt = %q, want global then project", got)
	}
	if len(snapshot.AppendSystemPrompts) != 2 ||
		snapshot.AppendSystemPrompts[0].Source != AppendSourceGlobal ||
		snapshot.AppendSystemPrompts[1].Source != AppendSourceProject {
		t.Fatalf("append system prompt sources = %+v", snapshot.AppendSystemPrompts)
	}
}

func TestAppendSystemPromptIgnoresDirectories(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cwd := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(cwd, ".ki", "prompt"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "prompt", "APPEND_SYSTEM.md"), "GLOBAL")

	snapshot := NewLoader(home).Scan(cwd)
	if got := appendText(snapshot); got != "GLOBAL" || len(snapshot.AppendSystemPrompts) != 1 {
		t.Fatalf("directory prompt fallback = %q %+v", got, snapshot.AppendSystemPrompts)
	}
}

func appendText(snapshot Snapshot) string {
	var parts []string
	for _, layer := range snapshot.AppendSystemPrompts {
		parts = append(parts, layer.Text)
	}
	return strings.Join(parts, "\n\n")
}

func TestScanWithoutExtensionsMatchesBarePromptFields(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	snapshot := NewLoader(home).Scan(cwd)
	if len(snapshot.Extensions) != 0 || len(snapshot.ExtensionPrompts) != 0 || len(snapshot.PathDirs) != 0 {
		t.Fatalf("expected empty extensions: %+v", snapshot.Extensions)
	}
}

// TestScanIncludesExtensionPathDirs pins that extension PATH directories ride
// the resource snapshot, so the tools toggle/reload path is the only one that
// has to invalidate them.
func TestScanIncludesExtensionPathDirs(t *testing.T) {
	home := resolvedTempDir(t)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, "extensions", "pathx", "extension.json"),
		`{"name":"pathx","capabilities":["path"],"runtime":{"kind":"none","path":["bin"]}}`)
	if err := os.MkdirAll(filepath.Join(home, "extensions", "pathx", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := NewLoader(home).Scan(cwd).PathDirs; len(got) != 1 || got[0] != filepath.Join(home, "extensions", "pathx", "bin") {
		t.Fatalf("PathDirs = %v", got)
	}
	writeFile(t, filepath.Join(home, "toggles.json"), `{"extensions":{"disabled":["pathx"]}}`)
	if got := NewLoader(home).Scan(cwd).PathDirs; len(got) != 0 {
		t.Fatalf("disabled package contributed PATH dirs: %v", got)
	}
}

// resolvedTempDir returns a temp dir whose symlinks are already resolved, so
// expectations built from it match the extension roots the loader derives:
// t.TempDir is unresolved on macOS (/var -> /private/var) and Windows
// short-name %TEMP%.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestScanMergesExtensionPromptAndHonorsDisabled(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	dir := filepath.Join(home, "extensions", "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"alpha","capabilities":["prompt.append"],"prompt":{"append":["A.md"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.md"), []byte("FROM-EXT"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := NewLoader(home).Scan(cwd)
	if len(snap.ExtensionPrompts) != 1 || snap.ExtensionPrompts[0].Text != "FROM-EXT" {
		t.Fatalf("%+v", snap.ExtensionPrompts)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "toggles.json"), []byte(`{"extensions":{"disabled":["alpha"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap = NewLoader(home).Scan(cwd)
	if len(snap.ExtensionPrompts) != 0 {
		t.Fatalf("disabled still merged: %+v", snap.ExtensionPrompts)
	}
	if len(snap.Extensions) != 1 || snap.Extensions[0].Enabled {
		t.Fatalf("listed disabled: %+v", snap.Extensions)
	}
}

func TestLoaderPinsCompleteSnapshotBySessionID(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeResources(t, home, cwd, "one")
	loader := NewLoader(home)

	one := loader.Load("session-1", cwd)
	assertSnapshotVersion(t, one, "one")
	if one.Environment.CWD != filepath.ToSlash(cwd) {
		t.Fatalf("environment cwd = %q", one.Environment.CWD)
	}

	writeResources(t, home, cwd, "two")
	pinned := loader.Load("session-1", t.TempDir())
	assertSnapshotVersion(t, pinned, "one")
	if pinned.Environment != one.Environment {
		t.Fatalf("environment was recomputed: got %+v, want %+v", pinned.Environment, one.Environment)
	}
	assertSnapshotVersion(t, loader.Load("session-2", cwd), "two")

	writeResources(t, home, cwd, "three")
	loader.Invalidate("session-1")
	assertSnapshotVersion(t, loader.Load("session-1", cwd), "three")
	assertSnapshotVersion(t, loader.Load("session-2", cwd), "two")

	loader.InvalidateAll()
	assertSnapshotVersion(t, loader.Load("session-2", cwd), "three")
}

func TestScanIsUncached(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	loader := NewLoader(home)
	writeResources(t, home, cwd, "one")
	assertSnapshotVersion(t, loader.Scan(cwd), "one")
	writeResources(t, home, cwd, "two")
	assertSnapshotVersion(t, loader.Scan(cwd), "two")
}

func writeResources(t *testing.T, home, cwd, version string) {
	t.Helper()
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), version)
	writeFile(t, filepath.Join(home, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: "+version+"\n---\n")
	writeFile(t, filepath.Join(home, "prompts", "review.md"), "---\ndescription: "+version+"\n---\n"+version+"\n")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotVersion(t *testing.T, snapshot Snapshot, want string) {
	t.Helper()
	if len(snapshot.ContextFiles) != 1 || snapshot.ContextFiles[0].Content != want {
		t.Fatalf("context = %+v, want %q", snapshot.ContextFiles, want)
	}
	if len(snapshot.Skills) != 1 || snapshot.Skills[0].Description != want {
		t.Fatalf("skills = %+v, want %q", snapshot.Skills, want)
	}
	if len(snapshot.Prompts) != 1 || snapshot.Prompts[0].Description != want || strings.TrimSpace(snapshot.Prompts[0].Body) != want {
		t.Fatalf("prompts = %+v, want %q", snapshot.Prompts, want)
	}
}
