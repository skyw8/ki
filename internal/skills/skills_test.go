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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: foo\ndescription: bar\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Filter(Scan(home, t.TempDir()), session.Toggle{})
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("got %+v", got)
	}
	got = Filter(Scan(home, t.TempDir()), session.Toggle{Disabled: []string{"foo"}})
	if len(got) != 0 {
		t.Fatalf("disabled: %+v", got)
	}
}

func TestListKeepsDisabledAndTagsSource(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	dir := filepath.Join(home, "skills", "foo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: foo\ndescription: bar\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(cwd, ".ki", "skills", "baz")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "SKILL.md"), []byte("---\nname: baz\ndescription: project\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Scan(home, cwd)
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["foo"].Source != "home" || byName["foo"].FilePath == "" {
		t.Fatalf("foo: %+v", got)
	}
	if byName["baz"].Source != "project" {
		t.Fatalf("baz: %+v", got)
	}
}

func TestListFollowsSkillDirSymlink(t *testing.T) {
	home := t.TempDir()
	realDir := t.TempDir()
	dir := filepath.Join(realDir, "linked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: linked\ndescription: via symlink\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(home, "skills", "linked")); err != nil {
		t.Fatal(err)
	}
	got := Scan(home, t.TempDir())
	found := false
	for _, s := range got {
		if s.Name == "linked" && s.Source == "home" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symlink skill missing: %+v", got)
	}
}

func TestDiscoverIgnoresNestedNonSkillFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "foo")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: foo\ndescription: real\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "evil.md"), []byte("---\nname: evil\ndescription: should not load\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Filter(Scan(home, t.TempDir()), session.Toggle{})
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("nested file leaked: %+v", got)
	}
}
