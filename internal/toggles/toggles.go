package toggles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ki/internal/session"
)

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
	Skills     session.Toggle `json:"skills"`
	Tools      session.Toggle `json:"tools"`
	Extensions session.Toggle `json:"extensions"`
	Message    Message        `json:"message"`
}

func path(home string) string { return filepath.Join(home, "toggles.json") }

// Load reads toggles.json; a missing file is all-enabled.
func Load(home string) File {
	if home == "" {
		return File{}
	}
	b, err := os.ReadFile(path(home))
	if err != nil {
		return File{}
	}
	var f File
	if json.Unmarshal(b, &f) != nil {
		return File{}
	}
	return f
}

// Save writes toggles.json.
func Save(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("toggles dir: %w", err)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(home), append(b, '\n'), 0o600)
}
