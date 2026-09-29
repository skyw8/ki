package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateChainsForward(t *testing.T) {
	steps := map[int]Migration{
		1: func(raw []byte) ([]byte, error) { return []byte(`{"version":2,"b":1}`), nil },
		2: func(raw []byte) ([]byte, error) { return []byte(`{"version":3,"c":1}`), nil },
	}
	out, migrated, err := Migrate([]byte(`{"version":1,"a":1}`), 3, steps)
	if err != nil || !migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if v, _ := Version(out); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
}

func TestMigrateMissingOrCurrentIsUntouched(t *testing.T) {
	for _, raw := range []string{`{"a":1}`, `{"version":2,"a":1}`} {
		out, migrated, err := Migrate([]byte(raw), 2, nil)
		if err != nil || migrated || string(out) != raw {
			t.Fatalf("raw=%s out=%s migrated=%v err=%v", raw, out, migrated, err)
		}
	}
}

func TestMigrateRejectsNewer(t *testing.T) {
	_, _, err := Migrate([]byte(`{"version":99}`), 2, nil)
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("err = %v, want ErrNewerVersion", err)
	}
}

func TestMigrateMissingStep(t *testing.T) {
	_, _, err := Migrate([]byte(`{"version":1}`), 3, nil)
	if err == nil || errors.Is(err, ErrNewerVersion) {
		t.Fatalf("err = %v, want missing-step error", err)
	}
}

func TestReadFileMissing(t *testing.T) {
	_, _, err := ReadFile(filepath.Join(t.TempDir(), "nope.json"), 1, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestWriteVersionedRefusesNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(`{"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteVersioned(path, 2, map[string]int{"version": 2}, 0o600)
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("err = %v, want ErrNewerVersion", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"version":9}` {
		t.Fatalf("newer document was overwritten: %s", b)
	}
}

func TestWriteVersionedBacksUpLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteVersioned(path, 2, map[string]int{"version": 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("backups = %v err=%v", matches, err)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != `{"version":1,"old":true}` {
		t.Fatalf("backup content = %s", b)
	}
}
