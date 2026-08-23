package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/mcp"
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

func TestMCPUpdatesAreRevisionGuardedAndCopyOnWrite(t *testing.T) {
	loader := NewLoader(t.TempDir())
	snapshot := loader.Load("session", t.TempDir())
	updates := map[string]mcp.ServerState{"demo": {Status: mcp.StatusReady, Tools: []mcp.ToolDefinition{{Name: "one"}}}}
	updated, ok := loader.UpdateMCP("session", snapshot.Revision, updates)
	if !ok || updated.MCPServers["demo"].Tools[0].Name != "one" {
		t.Fatalf("update = %+v, ok=%v", updated, ok)
	}
	updates["demo"] = mcp.ServerState{Status: mcp.StatusFailed}
	if loader.Load("session", "").MCPServers["demo"].Status != mcp.StatusReady {
		t.Fatal("caller mutated cached MCP state")
	}
	loader.Invalidate("session")
	if _, ok := loader.UpdateMCP("session", snapshot.Revision, updates); ok {
		t.Fatal("stale revision update was accepted")
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
	writeFile(t, filepath.Join(home, ".mcp.json"), `{"mcpServers":{"demo":{"command":"`+version+`"}}}`)
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
	if snapshot.MCP.MCPServers["demo"].Command != want {
		t.Fatalf("mcp = %+v, want %q", snapshot.MCP, want)
	}
}
