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
		Tools:      session.Toggle{Disabled: []string{"SpawnAgent", "spawn_agent"}},
		Extensions: session.Toggle{Disabled: []string{"telegram-bot"}},
		MCP:        session.Toggle{Disabled: []string{"Read", "read", "my-server"}},
		Message:    Message{Busy: BusyQueue},
		CodeMode:   CodeMode{Mode: "only"},
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
	if got.Tools.Allowed("spawn_agent") {
		t.Fatalf("tools %+v", got.Tools)
	}
	if len(got.Tools.Disabled) != 1 || got.Tools.Disabled[0] != "spawn_agent" {
		t.Fatalf("tools were not canonicalized: %+v", got.Tools)
	}
	if got.Message.BusyDelivery() != BusyQueue {
		t.Fatalf("message %+v", got.Message)
	}
	if got.CodeMode.Mode != "only" {
		t.Fatalf("code mode %+v", got.CodeMode)
	}
	if len(got.MCP.Disabled) != 3 || got.MCP.Disabled[0] != "Read" || got.MCP.Disabled[1] != "read" || got.MCP.Allowed("my-server") {
		t.Fatalf("raw MCP names must not use tool canonicalization: %+v", got.MCP)
	}
	if _, err := filepath.Rel(home, path(home)); err != nil {
		t.Fatal(err)
	}
}

func TestCodeModeEffectiveMode(t *testing.T) {
	for _, override := range []string{"", "invalid", "off", "mixed", "only"} {
		for _, fallback := range []string{"", "invalid", "off", "mixed", "only"} {
			want := "mixed"
			if fallback == "off" || fallback == "mixed" || fallback == "only" {
				want = fallback
			}
			if override == "off" || override == "mixed" || override == "only" {
				want = override
			}
			if got := (CodeMode{Mode: override}).EffectiveMode(fallback); got != want {
				t.Fatalf("override=%q fallback=%q: got %q, want %q", override, fallback, got, want)
			}
		}
	}
}
