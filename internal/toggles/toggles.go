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

const version = 4

const DefaultMaxThreads = 4
const MaxMaxThreads = 64

const BusySteer = "steer"

const BusyQueue = "queue"

const DefaultContextWindow = 500_000

type Message struct {
	Busy string `json:"busy,omitempty"`
	// ContextWindow is retained as an in-memory compatibility alias. New files store it under Agents.
	ContextWindow int `json:"-"`
}

func (m *Message) UnmarshalJSON(raw []byte) error {
	var stored struct {
		Busy          string `json:"busy"`
		ContextWindow int    `json:"contextWindow"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	m.Busy = stored.Busy
	m.ContextWindow = stored.ContextWindow
	return nil
}

type Agents struct {
	MaxThreads    int `json:"maxThreads,omitempty"`
	ContextWindow int `json:"contextWindow,omitempty"`
}

func (a Agents) MaxChildTurns() int {
	threads := a.MaxThreads
	if threads < 2 {
		threads = DefaultMaxThreads
	}
	return threads - 1
}

func (a Agents) ContextWindowLimit() int {
	if a.ContextWindow > 0 {
		return a.ContextWindow
	}
	return DefaultContextWindow
}

func (m Message) BusyDelivery() string {
	if m.Busy == BusyQueue {
		return BusyQueue
	}
	return BusySteer
}

func (m Message) ContextWindowLimit() int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	return DefaultContextWindow
}

type File struct {
	Version    int            `json:"version"`
	Skills     session.Toggle `json:"skills"`
	Tools      session.Toggle `json:"tools"`
	Extensions session.Toggle `json:"extensions"`
	MCP        session.Toggle `json:"mcp"`
	Message    Message        `json:"message"`
	Agents     Agents         `json:"agents"`
}

func path(home string) string { return filepath.Join(home, "toggles.json") }

func Load(home string) File {
	f := File{Version: version}
	if home == "" {
		return f
	}
	b, _, err := state.ReadFile(path(home), version, map[int]state.Migration{1: migrateToolNames, 2: removeCodeMode, 3: migrateAgents})
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil {
		return File{Version: version}
	}
	f.Version = version
	if f.Agents.MaxThreads < 2 {
		f.Agents.MaxThreads = DefaultMaxThreads
	}
	if f.Agents.ContextWindow <= 0 && f.Message.ContextWindow > 0 {
		f.Agents.ContextWindow = f.Message.ContextWindow
	}
	f.Message.ContextWindow = f.Agents.ContextWindow
	return f
}

func Save(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("toggles dir: %w", err)
	}
	f.Version = version
	if f.Agents.MaxThreads < 2 {
		f.Agents.MaxThreads = DefaultMaxThreads
	}
	if f.Agents.MaxThreads > MaxMaxThreads {
		f.Agents.MaxThreads = MaxMaxThreads
	}
	if f.Agents.ContextWindow <= 0 && f.Message.ContextWindow > 0 {
		f.Agents.ContextWindow = f.Message.ContextWindow
	}
	f.Message.ContextWindow = f.Agents.ContextWindow
	f.Tools.Only = canonicalNames(f.Tools.Only)
	f.Tools.Disabled = canonicalNames(f.Tools.Disabled)
	return state.WriteVersioned(path(home), version, f, 0o600)
}

func migrateToolNames(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if data := fields["tools"]; len(data) > 0 {
		var tools session.Toggle
		if err := json.Unmarshal(data, &tools); err != nil {
			return nil, err
		}
		tools.Only = migrateNames(tools.Only)
		tools.Disabled = migrateNames(tools.Disabled)
		encoded, err := json.Marshal(tools)
		if err != nil {
			return nil, err
		}
		fields["tools"] = encoded
	}
	fields["version"] = json.RawMessage("2")
	return json.Marshal(fields)
}

func removeCodeMode(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "code_mode")
	fields["version"] = json.RawMessage("3")
	return json.Marshal(fields)
}
func migrateAgents(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var message map[string]json.RawMessage
	if data := fields["message"]; len(data) > 0 {
		_ = json.Unmarshal(data, &message)
	}
	var agents map[string]json.RawMessage
	if data := fields["agents"]; len(data) > 0 {
		_ = json.Unmarshal(data, &agents)
	}
	if agents == nil {
		agents = map[string]json.RawMessage{}
	}
	if _, ok := agents["contextWindow"]; !ok {
		if value := message["contextWindow"]; len(value) > 0 {
			agents["contextWindow"] = value
		}
	}
	if _, ok := agents["maxThreads"]; !ok {
		agents["maxThreads"] = json.RawMessage(fmt.Sprintf("%d", DefaultMaxThreads))
	}
	encoded, err := json.Marshal(agents)
	if err != nil {
		return nil, err
	}
	fields["agents"] = encoded
	delete(message, "contextWindow")
	if len(message) > 0 {
		encoded, err = json.Marshal(message)
		if err != nil {
			return nil, err
		}
		fields["message"] = encoded
	} else {
		delete(fields, "message")
	}
	fields["version"] = json.RawMessage("4")
	return json.Marshal(fields)
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
