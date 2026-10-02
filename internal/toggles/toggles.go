package toggles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ki/internal/session"
	"ki/internal/state"
	toolapi "ki/internal/tool"
)

// version is the schema version of toggles.json.
const version = 2

// BusySteer inserts a busy prompt into the current loop.Run.
const BusySteer = "steer"

// BusyQueue persists a busy prompt until the current run releases.
const BusyQueue = "queue"

// Message is the process-wide default for a prompt while a session is busy.
type Message struct {
	Busy string `json:"busy,omitempty"`
}

// CodeMode is an optional global override of the configured tool exposure mode.
type CodeMode struct {
	Mode string `json:"mode,omitempty"`
}

// EffectiveMode falls back to TOML when no valid settings override is stored.
func (c CodeMode) EffectiveMode(fallback string) string {
	switch c.Mode {
	case "off", "mixed", "only":
		return c.Mode
	}
	switch fallback {
	case "off", "mixed", "only":
		return fallback
	default:
		return "mixed"
	}
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
	MCP        session.Toggle `json:"mcp"`
	Message    Message        `json:"message"`
	CodeMode   CodeMode       `json:"code_mode"`
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
	b, _, err := state.ReadFile(path(home), version, map[int]state.Migration{1: migrateToolNames})
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
	f.Tools.Only = canonicalNames(f.Tools.Only)
	f.Tools.Disabled = canonicalNames(f.Tools.Disabled)
	return state.WriteVersioned(path(home), version, f, 0o600)
}

func migrateToolNames(raw []byte) ([]byte, error) {
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	f.Version = version
	f.Tools.Only = migrateNames(f.Tools.Only)
	f.Tools.Disabled = migrateNames(f.Tools.Disabled)
	return json.Marshal(f)
}
func migrateNames(names []string) []string {
	out := []string{}
	seen := map[string]bool{}
	mapping := map[string][]string{
		"Bash": {"exec_command", "write_stdin"}, "PowerShell": {"exec_command", "write_stdin"},
		"Agent": {"spawn_agent", "followup_task"}, "SendMessage": {"send_message", "followup_task"},
		"TaskOutput": {"write_stdin", "wait_agent", "list_agents"}, "TaskStop": {"write_stdin", "interrupt_agent"},
	}
	for _, name := range names {
		targets := mapping[name]
		if targets == nil {
			canonical, err := toolapi.Canonical(name)
			if err != nil {
				targets = []string{name}
			} else {
				targets = []string{canonical}
			}
		}
		for _, target := range targets {
			if !seen[target] {
				seen[target] = true
				out = append(out, target)
			}
		}
	}
	return out
}

func canonicalNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		canonical, err := toolapi.Canonical(name)
		if err == nil {
			name = canonical
		}
		if !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	return out
}
