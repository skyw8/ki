package toggles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ki/internal/session"
	"ki/internal/state"
)

func TestNewerSchemaFallsBackAndSaveRefuses(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(path(home), []byte(`{"version":99,"skills":{"disabled":["alpha"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Load(home)
	if !f.Skills.Allowed("alpha") {
		t.Fatalf("newer schema must fall back to defaults, got %+v", f.Skills)
	}
	if err := Save(home, File{}); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatalf("save err = %v, want state.ErrNewerVersion", err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	f := Load(t.TempDir())
	if !f.Skills.Allowed("x") || !f.Extensions.Allowed("y") {
		t.Fatalf("%+v", f)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := File{
		Skills:     session.Toggle{Disabled: []string{"alpha"}},
		Tools:      session.Toggle{Disabled: []string{"Agent"}},
		Extensions: session.Toggle{Disabled: []string{"telegram-bot"}},
		Message:    Message{Busy: BusyQueue},
	}
	if err := Save(home, want); err != nil {
		t.Fatal(err)
	}
	got := Load(home)
	if !got.Skills.Allowed("beta") || got.Skills.Allowed("alpha") {
		t.Fatalf("skills %+v", got.Skills)
	}
	if got.Extensions.Allowed("telegram-bot") {
		t.Fatalf("extensions %+v", got.Extensions)
	}
	if got.Tools.Allowed("Agent") {
		t.Fatalf("tools %+v", got.Tools)
	}
	if got.Message.BusyDelivery() != BusyQueue {
		t.Fatalf("message %+v", got.Message)
	}
	if _, err := filepath.Rel(home, path(home)); err != nil {
		t.Fatal(err)
	}
}
