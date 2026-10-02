package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ki/internal/state"
)

const MCPConfigVersion = 1

// MCPSnapshot is the merged configuration and origin of each effective server.
type MCPSnapshot struct {
	Servers      map[string]MCPServer
	Sources      map[string]string
	FilesPresent bool
}

// MCPPaths returns the global and project configuration locations.
func MCPPaths(home, cwd string) (global, project string) {
	if home == "" {
		home = HomeDir()
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	global = filepath.Join(home, "mcp.json")
	if cwd != "" {
		project = filepath.Join(cwd, ".ki", "mcp.json")
	}
	return global, project
}

// LoadMCP reads optional global and project documents, replacing whole servers.
func LoadMCP(home, cwd string) (map[string]MCPServer, error) {
	snapshot, err := LoadMCPSnapshot(home, cwd)
	return snapshot.Servers, err
}

// LoadMCPWithSources returns effective servers and their global/project origins.
func LoadMCPWithSources(home, cwd string) (map[string]MCPServer, map[string]string, error) {
	snapshot, err := LoadMCPSnapshot(home, cwd)
	return snapshot.Servers, snapshot.Sources, err
}

// LoadMCPSnapshot preserves file presence and winning origins for clients.
func LoadMCPSnapshot(home, cwd string) (MCPSnapshot, error) {
	snapshot := MCPSnapshot{
		Servers: make(map[string]MCPServer),
		Sources: make(map[string]string),
	}
	global, project := MCPPaths(home, cwd)
	for _, file := range []struct{ path, source string }{{global, "global"}, {project, "project"}} {
		if file.path == "" {
			continue
		}
		raw, _, err := state.ReadFile(file.path, MCPConfigVersion, nil)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return MCPSnapshot{}, err
		}
		snapshot.FilesPresent = true
		servers, err := decodeMCP(raw)
		if err != nil {
			return MCPSnapshot{}, fmt.Errorf("read MCP config %s: %w", file.path, err)
		}
		// A server is a single transport capability. Merging individual fields
		// could retain global credentials or mix stdio options into HTTP.
		for name, server := range servers {
			snapshot.Servers[name] = server
			snapshot.Sources[name] = file.source
		}
	}
	return snapshot, nil
}

func decodeMCP(raw []byte) (map[string]MCPServer, error) {
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return nil, err
	}
	var document struct {
		Version    int                  `json:"version"`
		MCPServers map[string]MCPServer `json:"mcpServers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one JSON document")
	}
	if document.Version != MCPConfigVersion {
		return nil, fmt.Errorf("version must be %d", MCPConfigVersion)
	}
	if document.MCPServers == nil {
		return nil, fmt.Errorf("mcpServers must be a non-null object")
	}
	// encoding/json accepts null for scalar values and case-insensitive field
	// names. Configuration must not silently turn either into default policy.
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	for key := range shape {
		if key != "version" && key != "mcpServers" {
			return nil, fmt.Errorf("unknown field %q", key)
		}
	}
	var serverShapes map[string]map[string]json.RawMessage
	if err := json.Unmarshal(shape["mcpServers"], &serverShapes); err != nil {
		return nil, err
	}
	for name, fields := range serverShapes {
		if fields == nil {
			return nil, fmt.Errorf("mcpServers.%s must be a non-null object", name)
		}
		for key, value := range fields {
			switch key {
			case "command", "args", "env", "envVars", "cwd", "url", "httpHeaders",
				"bearerTokenEnv", "enabled", "required", "startupTimeoutSeconds",
				"toolTimeoutSeconds", "enabledTools", "disabledTools":
			default:
				return nil, fmt.Errorf("mcpServers.%s: unknown field %q", name, key)
			}
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return nil, err
			}
			if hasJSONNull(decoded) {
				return nil, fmt.Errorf("mcpServers.%s.%s must not contain null", name, key)
			}
		}
	}
	if err := ValidateMCPServers(document.MCPServers); err != nil {
		return nil, err
	}
	return document.MCPServers, nil
}

func uniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("expected JSON object key")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON key %q", name)
			}
			seen[name] = true
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		for decoder.More() {
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

func hasJSONNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range value {
			if hasJSONNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if hasJSONNull(item) {
				return true
			}
		}
	}
	return false
}
