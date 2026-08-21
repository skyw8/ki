package toggles

import (
	"path/filepath"
	"testing"

	"ki/internal/session"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	f := Load(t.TempDir())
	if !f.Skills.Allowed("x") || !f.MCP.Allowed("y") {
		t.Fatalf("%+v", f)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := File{
		Skills: session.Toggle{Disabled: []string{"alpha"}},
		MCP:    session.Toggle{Disabled: []string{"exa"}},
	}
	if err := Save(home, want); err != nil {
		t.Fatal(err)
	}
	got := Load(home)
	if !got.Skills.Allowed("beta") || got.Skills.Allowed("alpha") {
		t.Fatalf("skills %+v", got.Skills)
	}
	if got.MCP.Allowed("exa") {
		t.Fatalf("mcp %+v", got.MCP)
	}
	if _, err := filepath.Rel(home, path(home)); err != nil {
		t.Fatal(err)
	}
}
