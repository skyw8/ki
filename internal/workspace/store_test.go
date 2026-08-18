package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateNormalizeAndDuplicate(t *testing.T) {
	home := t.TempDir()
	s := Open(home, filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "proj")
	rec, created, err := s.Create(dir, "")
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if rec.Title != "proj" {
		t.Fatalf("title %q", rec.Title)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	again, created, err := s.Create(dir, "other")
	if err != nil || created || again.ID != rec.ID {
		t.Fatalf("dup: %+v created=%v err=%v", again, created, err)
	}
	if again.Title != "proj" {
		t.Fatal("duplicate must keep title")
	}
	file := filepath.Join(home, "notdir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(file, ""); err == nil {
		t.Fatal("file path should fail")
	}
}

func TestEnsureTempAndMatch(t *testing.T) {
	home := t.TempDir()
	s := Open(home, filepath.Join(home, "sessions"))
	a, err := s.EnsureTemp()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureTemp()
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Path == b.Path {
		t.Fatal("temp workspaces must be unique")
	}
	if !s.IsTemp(a) || !strings.Contains(a.Path, filepath.Join("workspace", "tmp+")) {
		t.Fatalf("temp path %s", a.Path)
	}
	got, ok := s.Match(a.Path)
	if !ok || got.ID != a.ID {
		t.Fatalf("match: %+v", got)
	}
}

func TestInsertBeforeAndSessions(t *testing.T) {
	home := t.TempDir()
	s := Open(home, filepath.Join(home, "sessions"))
	a, _, _ := s.Create(filepath.Join(home, "a"), "a")
	b, _, _ := s.Create(filepath.Join(home, "b"), "b")
	c, _, _ := s.Create(filepath.Join(home, "c"), "c")
	// create prepends: c, b, a
	if err := s.InsertBefore(a.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	ids := idsOf(s.List())
	if ids[0] != a.ID || ids[1] != c.ID {
		t.Fatalf("order %v", ids)
	}
	if err := s.AttachSession(a.ID, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachSession(a.ID, "s2"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(a.ID)
	if len(got.SessionIDs) != 2 || got.SessionIDs[0] != "s2" {
		t.Fatalf("attach prepend: %v", got.SessionIDs)
	}
	if err := s.InsertSessionBefore(a.ID, "s2", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(a.ID)
	if got.SessionIDs[1] != "s2" {
		t.Fatalf("move to end: %v", got.SessionIDs)
	}
	if err := s.DetachSession(a.ID, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle(b.ID, "bee"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(b.ID)
	if got.Title != "bee" {
		t.Fatal(got.Title)
	}
	ok, err := s.Delete(c.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok := s.Get(c.ID); ok {
		t.Fatal("deleted still present")
	}
}

func TestBootstrapOnce(t *testing.T) {
	home := t.TempDir()
	s := Open(home, filepath.Join(home, "sessions"))
	d1 := filepath.Join(home, "p1")
	d2 := filepath.Join(home, "p2")
	_ = os.MkdirAll(d1, 0o700)
	_ = os.MkdirAll(d2, 0o700)
	if err := s.Bootstrap([]string{d1, d2, d1}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.List()); n != 2 {
		t.Fatalf("n=%d", n)
	}
	if err := s.Bootstrap([]string{filepath.Join(home, "p3")}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.List()); n != 2 {
		t.Fatal("second bootstrap must not add")
	}
}

func TestSafeToRemoveDir(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := SafeToRemoveDir(home, home, sessions); err == nil {
		t.Fatal("home")
	}
	if err := SafeToRemoveDir("/", home, sessions); err == nil {
		t.Fatal("root")
	}
	ok := filepath.Join(home, "proj")
	_ = os.MkdirAll(ok, 0o700)
	if err := SafeToRemoveDir(ok, home, sessions); err != nil {
		t.Fatal(err)
	}
}

func idsOf(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}
