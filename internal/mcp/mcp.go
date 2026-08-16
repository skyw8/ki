package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ki/internal/session"
)

// ServerSpec is one MCP server entry (MCP config spec).
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
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
			URL:     httpURL(spec),
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

// httpURL is the remote endpoint if this spec is HTTP, not stdio.
// Existing installs wrote `npx -y mcp-remote https://…`; treating that as HTTP
// avoids a new mcp-remote process (OAuth discovery + local tunnel) on every use.
func httpURL(spec ServerSpec) string {
	if u := strings.TrimSpace(spec.URL); u != "" {
		return u
	}
	t := strings.ToLower(strings.TrimSpace(spec.Type))
	if t == "http" || t == "sse" || t == "streamable-http" {
		return strings.TrimSpace(spec.URL)
	}
	for i, a := range spec.Args {
		if a == "mcp-remote" && i+1 < len(spec.Args) && strings.HasPrefix(spec.Args[i+1], "http") {
			return spec.Args[i+1]
		}
	}
	return ""
}

// specKey includes command/url so editing .mcp.json does not reuse a stale client or schema.
func specKey(name string, spec ServerSpec) string {
	return strings.Join([]string{name, spec.Command, strings.Join(spec.Args, "\x1f"), httpURL(spec)}, "\x00")
}
