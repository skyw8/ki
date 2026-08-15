package skills

import (
	"os"
	"path/filepath"
	"testing"

	"ki/internal/session"
)

func TestDiscoverHonorsToggle(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: foo\ndescription: bar\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, t.TempDir(), session.Toggle{})
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("got %+v", got)
	}
	got = Discover(home, t.TempDir(), session.Toggle{Disabled: []string{"foo"}})
	if len(got) != 0 {
		t.Fatalf("disabled: %+v", got)
	}
}
