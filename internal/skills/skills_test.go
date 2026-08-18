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
	got := Discover(home, t.TempDir(), "s1", session.Toggle{})
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("got %+v", got)
	}
	got = Discover(home, t.TempDir(), "s1", session.Toggle{Disabled: []string{"foo"}})
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
	got := List(home, cwd, "s1")
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
	got := List(home, t.TempDir(), "s1")
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
	got := Discover(home, t.TempDir(), "s1", session.Toggle{})
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("nested file leaked: %+v", got)
	}
}

// TestCacheScopedPerSession pins the session-scoped cache contract: within
// one session a (home, cwd) pair is scanned once and pinned; a new session
// in the same workspace re-scans; Invalidate re-reads only the target
// session. Mirrors prompt.TestAgentsCacheScopedPerSession.
func TestCacheScopedPerSession(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeSkill := func(name, desc string) {
		dir := filepath.Join(home, "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("foo", "first")

	s1 := "session-1"
	got := List(home, cwd, s1)
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("initial: %+v", got)
	}

	// New skill appears on disk; the same session must not pick it up.
	writeSkill("bar", "added later")
	got = List(home, cwd, s1)
	if len(got) != 1 || got[0].Name != "foo" {
		t.Fatalf("same-session cache not pinned: %+v", got)
	}

	// A new session in the same workspace re-scans disk.
	s2 := "session-2"
	got = List(home, cwd, s2)
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["foo"] || !names["bar"] {
		t.Fatalf("new session must re-scan: %+v", got)
	}

	// Invalidate forces only that session to re-scan.
	writeSkill("baz", "after invalidate")
	Invalidate(home, cwd, s1)
	got = List(home, cwd, s1)
	names = map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["foo"] || !names["bar"] || !names["baz"] {
		t.Fatalf("after invalidate: %+v", got)
	}
	if got = List(home, cwd, s2); len(got) != 2 {
		t.Fatalf("other session cache dropped: %+v", got)
	}

	// A different cwd has its own cache entry: it sees the home-global
	// foo/bar/baz plus its own project skill qux.
	otherCwd := t.TempDir()
	proj := filepath.Join(otherCwd, ".ki", "skills", "qux")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "SKILL.md"), []byte("---\nname: qux\ndescription: other cwd\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = List(home, otherCwd, s1)
	names = map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["foo"] || !names["bar"] || !names["baz"] || !names["qux"] {
		t.Fatalf("other cwd scan: %+v", got)
	}

	// Targeted Invalidate(home, cwd, s1) must not touch the other entry.
	Invalidate(home, cwd, s1)
	got = List(home, otherCwd, s1)
	names = map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["foo"] || !names["bar"] || !names["baz"] || !names["qux"] {
		t.Fatalf("other cwd cache dropped: %+v", got)
	}
}
