package toggles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ki/internal/session"
)

// File is {KI_HOME}/toggles.json.
type File struct {
	Skills session.Toggle `json:"skills"`
	MCP    session.Toggle `json:"mcp"`
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
