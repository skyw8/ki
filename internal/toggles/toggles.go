package toggles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ki/internal/session"
	"ki/internal/state"
)

// version is the schema version of toggles.json.
const version = 1

// BusySteer inserts a busy prompt into the current loop.Run.
const BusySteer = "steer"

// BusyQueue persists a busy prompt until the current run releases.
const BusyQueue = "queue"

// Message is the process-wide default for a prompt while a session is busy.
type Message struct {
	Busy string `json:"busy,omitempty"`
}

// BusyDelivery returns steer or queue. Empty defaults to steer.
func (m Message) BusyDelivery() string {
	if m.Busy == BusyQueue {
		return BusyQueue
	}
	return BusySteer
}

// File is {KI_HOME}/toggles.json.
type File struct {
	Version    int            `json:"version"`
	Skills     session.Toggle `json:"skills"`
	Tools      session.Toggle `json:"tools"`
	Extensions session.Toggle `json:"extensions"`
	Message    Message        `json:"message"`
}

func path(home string) string { return filepath.Join(home, "toggles.json") }

// Load reads toggles.json; a missing file is all-enabled. Toggles are
// best-effort: a document this build cannot read falls back to defaults rather
// than failing startup, but Save refuses to overwrite a newer one.
func Load(home string) File {
	f := File{Version: version}
	if home == "" {
		return f
	}
	b, _, err := state.ReadFile(path(home), version, nil)
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil {
		return File{Version: version}
	}
	f.Version = version
	return f
}

// Save writes toggles.json atomically, refusing to clobber a newer schema.
func Save(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("toggles dir: %w", err)
	}
	f.Version = version
	return state.WriteVersioned(path(home), version, f, 0o600)
}
