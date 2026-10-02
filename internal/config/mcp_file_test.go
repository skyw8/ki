package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ki/internal/state"
)

func TestLoadMCPSnapshotOriginsAndWholeServerOverride(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	global, project := MCPPaths(home, cwd)
	write := func(path string, servers map[string]MCPServer) {
		t.Helper()
		if err := state.WriteJSON(path, map[string]any{"version": 1, "mcpServers": servers}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(global, map[string]MCPServer{
		"Docs": {Command: "global-server", Args: []string{"--stdio"}, Env: map[string]string{"SECRET": "global"}},
		"docs": {Command: "case-distinct"},
		"base": {URL: "https://example.com/mcp", HTTPHeaders: map[string]string{"X-API-KEY": "value"}},
	})
	write(project, map[string]MCPServer{"Docs": {URL: "https://project.example/mcp", EnabledTools: []string{}}})
	got, err := LoadMCPSnapshot(home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	wantServers := map[string]MCPServer{
		"Docs": {URL: "https://project.example/mcp", EnabledTools: []string{}},
		"docs": {Command: "case-distinct"},
		"base": {URL: "https://example.com/mcp", HTTPHeaders: map[string]string{"X-API-KEY": "value"}},
	}
	wantSources := map[string]string{"Docs": "project", "docs": "global", "base": "global"}
	if !got.FilesPresent || !reflect.DeepEqual(got.Servers, wantServers) || !reflect.DeepEqual(got.Sources, wantSources) {
		t.Fatalf("snapshot=%#v, servers=%#v", got, got.Servers)
	}
	servers, sources, err := LoadMCPWithSources(home, cwd)
	if err != nil || !reflect.DeepEqual(servers, wantServers) || !reflect.DeepEqual(sources, wantSources) {
		t.Fatalf("wrapper servers=%#v sources=%#v error=%v", servers, sources, err)
	}
}

func TestLoadMCPMissingAndExplicitEmpty(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	got, err := LoadMCPSnapshot(home, cwd)
	if err != nil || got.FilesPresent || len(got.Servers) != 0 || len(got.Sources) != 0 {
		t.Fatalf("missing=%#v error=%v", got, err)
	}
	if err := state.WriteJSON(filepath.Join(home, "mcp.json"), map[string]any{"version": 1, "mcpServers": map[string]MCPServer{}}, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadMCPSnapshot(home, cwd)
	if err != nil || !got.FilesPresent || len(got.Servers) != 0 {
		t.Fatalf("empty=%#v error=%v", got, err)
	}
}

func TestLoadMCPRejectsMalformedWithoutMutation(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `[]`, `{"mcpServers":{}}`, `{"version":0,"mcpServers":{}}`,
		`{"version":null,"mcpServers":{}}`, `{"version":-1,"mcpServers":{}}`,
		`{"version":1.2,"mcpServers":{}}`, `{"version":1}`, `{"version":1,"mcpServers":null}`,
		`{"version":1,"mcpServers":[]} `, `{"version":1,"mcpServers":{}} {}`,
		`{"version":1,"mcpServers":{"docs":null}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","enabled":null}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","enabledTools":null}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","env":{"SECRET":null}}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","args":[null]}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","unknown":true}}}`,
		`{"version":1,"mcpServers":{"docs":{"Command":"server"}}}`,
		`{"version":1,"MCPServers":{}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","startup_timeout_seconds":10}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","url":"https://example.com/mcp"}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","toolTimeoutSeconds":-1}}}`,
		`{"version":1,"version":1,"mcpServers":{}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"first","command":"second"}}}`,
		`{"version":1,"mcpServers":{"docs":{"command":"server","env":{"SECRET":"first","SECRET":"second"}}}}`,
		`{"version":1,"mcpServers":`,
	} {
		t.Run(raw, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			path := filepath.Join(home, "mcp.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadMCP(home, cwd); err == nil {
				t.Fatal("invalid document accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != raw {
				t.Fatalf("document mutated: %q, error=%v", after, err)
			}
		})
	}
}

func TestLoadMCPNewerVersionFailsEvenIfProjectOverrides(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := state.WriteJSON(filepath.Join(home, "mcp.json"), map[string]any{"version": 2, "mcpServers": map[string]any{}}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(filepath.Join(cwd, ".ki", "mcp.json"), map[string]any{"version": 1, "mcpServers": map[string]any{}}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMCP(home, cwd); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatalf("error=%v, want ErrNewerVersion", err)
	}
}

func TestLoadRejectsLegacyMCPTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte("[mcp_servers.docs]\ncommand=\"server\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "mcp_servers") {
		t.Fatalf("legacy table error=%v", err)
	}
}

func TestMCPServerJSONAllowlistRoundTrip(t *testing.T) {
	for _, enabled := range [][]string{nil, {}, {"read"}} {
		s := MCPServer{Command: "server", EnabledTools: enabled}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		servers, err := decodeMCP([]byte(`{"version":1,"mcpServers":{"docs":` + string(raw) + `}}`))
		if err != nil || !reflect.DeepEqual(servers["docs"], s) {
			t.Fatalf("roundtrip %s, got=%#v error=%v", raw, servers, err)
		}
	}
}

func TestMCPServerCamelCaseFields(t *testing.T) {
	no := false
	servers := map[string]MCPServer{
		"local": {
			Command: "server", Args: []string{"--stdio"}, Env: map[string]string{"API_TOKEN": "token"},
			EnvVars: []string{"EXTRA_TOKEN"}, CWD: t.TempDir(), Enabled: &no, Required: true,
			StartupTimeoutSeconds: 42, ToolTimeoutSeconds: 123,
			EnabledTools: []string{"read"}, DisabledTools: []string{"delete"},
		},
		"remote": {
			URL: "https://example.com/mcp", HTTPHeaders: map[string]string{"X-API-Key": "value"},
			BearerTokenEnv: "MCP_TOKEN",
		},
	}
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("KI_HOME", home)
	if err := state.WriteJSON(filepath.Join(home, "mcp.json"), map[string]any{"version": 1, "mcpServers": servers}, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(cwd)
	if err != nil || !got.MCPFromFiles || !reflect.DeepEqual(got.MCPServers, servers) {
		t.Fatalf("load=%#v, fromFiles=%v error=%v", got.MCPServers, got.MCPFromFiles, err)
	}
}
