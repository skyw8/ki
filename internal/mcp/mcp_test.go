package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"ki/internal/session"
)

func TestLoadProjectOverridesGlobalAndToggle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, ".mcp.json"), []byte(`{
	  "mcpServers": {
	    "github": {"command": "npx", "args": ["-y", "gh"]},
	    "old": {"command": "true"}
	  }
	}`), 0o644)
	_ = os.MkdirAll(filepath.Join(cwd, ".ki"), 0o755)
	_ = os.WriteFile(filepath.Join(cwd, ".ki", ".mcp.json"), []byte(`{
	  "mcpServers": {
	    "github": {"command": "echo", "args": ["hi"]}
	  }
	}`), 0o644)
	f := Load(home, cwd)
	if f.MCPServers["github"].Command != "echo" {
		t.Fatalf("project should win: %+v", f.MCPServers["github"])
	}
	if _, ok := f.MCPServers["old"]; !ok {
		t.Fatal("global-only server missing")
	}
	names := FilterNames(f, session.Toggle{Disabled: []string{"old"}})
	for _, n := range names {
		if n == "old" {
			t.Fatal("disabled still present")
		}
	}
	only := FilterNames(f, session.Toggle{Only: []string{"github"}})
	if len(only) != 1 || only[0] != "github" {
		t.Fatalf("only: %v", only)
	}
}
