package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"ki/internal/session"
)

// ServerSpec is one MCP server entry (MCP config spec).
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// ToolDefinition is the immutable, model-facing subset of an SDK tool.
// Raw preserves metadata that ki does not currently consume without keeping
// mutable SDK pointers in a resources snapshot.
type ToolDefinition struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  map[string]any  `json:"inputSchema"`
	OutputSchema any             `json:"outputSchema,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

type ServerStatus string

const (
	StatusUnloaded ServerStatus = "unloaded"
	StatusReady    ServerStatus = "ready"
	StatusFailed   ServerStatus = "failed"
	StatusStale    ServerStatus = "stale"
)

// ServerState is the session-scoped discovery result stored by resources.
// Live SDK sessions and transports deliberately remain in Manager.
type ServerState struct {
	Status          ServerStatus     `json:"status"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	Error           string           `json:"error,omitempty"`
	ProtocolVersion string           `json:"protocolVersion,omitempty"`
	ServerName      string           `json:"serverName,omitempty"`
	ServerVersion   string           `json:"serverVersion,omitempty"`
	Capabilities    json.RawMessage  `json:"capabilities,omitempty"`
	LoadedAt        time.Time        `json:"loadedAt,omitempty"`
	EventID         string           `json:"eventId,omitempty"`
}

var errInvalidServerSpec = errors.New("invalid MCP server configuration")

// ValidateServerSpec selects exactly one standard SDK transport.
func ValidateServerSpec(spec ServerSpec) error {
	hasCommand := spec.Command != ""
	hasURL := spec.URL != ""
	if hasCommand == hasURL {
		return fmt.Errorf("%w: set exactly one of command or url", errInvalidServerSpec)
	}
	if hasURL && (len(spec.Args) > 0 || len(spec.Env) > 0) {
		return fmt.Errorf("%w: args/env require command transport", errInvalidServerSpec)
	}
	if hasCommand && len(spec.Headers) > 0 {
		return fmt.Errorf("%w: headers require url transport", errInvalidServerSpec)
	}
	return nil
}

// File is the on-disk .mcp.json document.
type File struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
	Sources    map[string]string     `json:"-"`
}

// ServerInfo is one discovered MCP server without spawning it.
type ServerInfo struct {
	Name    string
	Command string
	Args    []string
	URL     string
	Source  string
	Enabled bool
}

// Load merges global then project (project wins on same name).
func Load(home, cwd string) File {
	out := File{MCPServers: map[string]ServerSpec{}, Sources: map[string]string{}}
	merge := func(path, source string) {
		//nolint:gosec // path is one of the bounded MCP discovery locations.
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var f File
		if json.Unmarshal(b, &f) != nil || f.MCPServers == nil {
			return
		}
		for k, v := range f.MCPServers {
			out.MCPServers[k] = v
			out.Sources[k] = source
		}
	}
	if home != "" {
		merge(filepath.Join(home, ".mcp.json"), "home")
	}
	if cwd != "" {
		merge(filepath.Join(cwd, ".ki", ".mcp.json"), "project")
	}
	return out
}

// List returns configured servers without spawning processes.
func List(file File, toggle session.Toggle) []ServerInfo {
	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ServerInfo, 0, len(names))
	for _, name := range names {
		spec := file.MCPServers[name]
		src := ""
		if file.Sources != nil {
			src = file.Sources[name]
		}
		out = append(out, ServerInfo{
			Name:    name,
			Command: spec.Command,
			Args:    spec.Args,
			URL:     spec.URL,
			Source:  src,
			Enabled: toggle.Allowed(name),
		})
	}
	return out
}

// FilterNames is used by tests to inspect merge without spawning.
func FilterNames(f File, toggle session.Toggle) []string {
	var n []string
	for name := range f.MCPServers {
		if toggle.Allowed(name) {
			n = append(n, name)
		}
	}
	return n
}
