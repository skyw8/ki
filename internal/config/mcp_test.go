package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMCPValidation(t *testing.T) {
	no := false
	valid := []MCPServer{
		{Command: "server"},
		{Command: "server", Args: []string{"--stdio"}, CWD: t.TempDir(), EnvVars: []string{"TOKEN"}},
		{URL: "https://example.org/mcp", HTTPHeaders: map[string]string{"X-Key": "value"}},
		{URL: "http://localhost/mcp", Enabled: &no, Required: true, ToolTimeoutSeconds: 2},
	}
	for _, s := range valid {
		if err := ValidateMCPServers(map[string]MCPServer{"test": s}); err != nil {
			t.Fatalf("valid %+v: %v", s, err)
		}
	}
	invalid := []MCPServer{
		{}, {Command: "server", URL: "https://example.org"}, {Command: " "},
		{URL: "file:///tmp/server"}, {URL: "https://u:p@example.org/mcp"},
		{URL: "https://example.org/mcp#fragment"}, {URL: "https://example.org", EnvVars: []string{"X"}},
		{Command: "server", CWD: filepath.Join("relative", "path")},
		{Command: "server", ToolTimeoutSeconds: -1}, {Command: "server", StartupTimeoutSeconds: -1},
		{Command: "server", Env: map[string]string{"BAD=NAME": "value"}},
		{Command: "server", EnvVars: []string{"BAD=NAME"}},
		{Command: "server", DisabledTools: []string{""}},
		{Command: "server", HTTPHeaders: map[string]string{"X-Key": "value"}},
		{URL: "https://example.org", HTTPHeaders: map[string]string{"X-Key": "x\r\nInjected: y"}},
	}
	for _, s := range invalid {
		if err := ValidateMCPServers(map[string]MCPServer{"test": s}); err == nil {
			t.Fatalf("invalid %+v accepted", s)
		}
	}
}

func TestMCPDefaultsAndRawFilters(t *testing.T) {
	s := MCPServer{}
	if !s.IsEnabled() || s.StartupTimeout() != 10*time.Second || s.ToolTimeout() != time.Minute {
		t.Fatalf("defaults %+v", s)
	}
	s.EnabledTools = []string{"raw-tool"}
	s.DisabledTools = []string{"disabled"}
	if !s.AllowsTool("raw-tool") || s.AllowsTool("raw_tool") || s.AllowsTool("disabled") {
		t.Fatal("filters did not preserve protocol names")
	}
	s.DisabledTools = []string{"raw-tool"}
	if s.AllowsTool("raw-tool") {
		t.Fatal("disabled must win over enabled")
	}
	s.EnabledTools = []string{}
	s.DisabledTools = nil
	if s.AllowsTool("anything") {
		t.Fatal("explicit empty allow-list must deny every tool")
	}
}
