package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ki/internal/state"
)

func TestPrivateStateAtomicVersionAndNewerProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	app := &telegramApp{statePath: path, state: telegramState{Offsets: map[string]int64{"bot:1": 17}, Sessions: map[string]string{"chat": "session"}}}
	if err := app.persistStateLocked(); err != nil {
		t.Fatal(err)
	}
	raw, _, err := state.ReadFile(path, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, err := state.Version(raw)
	if err != nil || version != 1 {
		t.Fatal(string(raw), err)
	}
	saved, err := loadTelegramState(path)
	if err != nil || saved.Offsets["bot:1"] != 17 || saved.Sessions["chat"] != "session" || saved.Topics == nil || saved.Forums == nil {
		t.Fatal(saved, err)
	}
	newer := []byte(`{"version":999,"offsets":{"bot:1":100},"future":"keep"}`)
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTelegramState(path); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
	if err := app.persistStateLocked(); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(newer) {
		t.Fatal(string(actual), err)
	}
	config := filepath.Join(filepath.Dir(path), "config.json")
	if err := os.WriteFile(config, []byte(`{"version":999,"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTelegramConfig(config); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
}
