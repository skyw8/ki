package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MCPServer selects one official MCP transport and its raw-tool policy.
type MCPServer struct {
	Command               string            `json:"command,omitempty"`
	Args                  []string          `json:"args,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	EnvVars               []string          `json:"envVars,omitempty"`
	CWD                   string            `json:"cwd,omitempty"`
	URL                   string            `json:"url,omitempty"`
	HTTPHeaders           map[string]string `json:"httpHeaders,omitempty"`
	BearerTokenEnv        string            `json:"bearerTokenEnv,omitempty"`
	Enabled               *bool             `json:"enabled,omitempty"`
	Required              bool              `json:"required,omitempty"`
	StartupTimeoutSeconds int               `json:"startupTimeoutSeconds,omitempty"`
	ToolTimeoutSeconds    int               `json:"toolTimeoutSeconds,omitempty"`
	EnabledTools          []string          `json:"enabledTools,omitempty"`
	DisabledTools         []string          `json:"disabledTools,omitempty"`
}

// MarshalJSON preserves the difference between an omitted allowlist and [].
func (s MCPServer) MarshalJSON() ([]byte, error) {
	type plain MCPServer
	var enabledTools *[]string
	if s.EnabledTools != nil {
		enabledTools = &s.EnabledTools
	}
	return json.Marshal(struct {
		plain
		EnabledTools *[]string `json:"enabledTools,omitempty"`
	}{plain: plain(s), EnabledTools: enabledTools})
}

func (s MCPServer) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

func (s MCPServer) StartupTimeout() time.Duration {
	if s.StartupTimeoutSeconds == 0 {
		return 10 * time.Second
	}
	return time.Duration(s.StartupTimeoutSeconds) * time.Second
}

func (s MCPServer) ToolTimeout() time.Duration {
	if s.ToolTimeoutSeconds == 0 {
		return 60 * time.Second
	}
	return time.Duration(s.ToolTimeoutSeconds) * time.Second
}

// AllowsTool compares protocol names, never the normalized model-facing alias.
func (s MCPServer) AllowsTool(name string) bool {
	return (s.EnabledTools == nil || slices.Contains(s.EnabledTools, name)) &&
		!slices.Contains(s.DisabledTools, name)
}

// ValidateMCPServers rejects ambiguous transports and malformed local settings.
func ValidateMCPServers(servers map[string]MCPServer) error {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		s := servers[name]
		fail := func(reason string) error { return fmt.Errorf("mcpServers.%s: %s", name, reason) }
		if strings.TrimSpace(name) == "" {
			return fail("server name must not be empty")
		}
		if (s.Command == "") == (s.URL == "") {
			return fail("configure exactly one of command or url")
		}
		if s.Command != "" && strings.TrimSpace(s.Command) == "" {
			return fail("command must not be blank")
		}
		if s.CWD != "" && !filepath.IsAbs(s.CWD) {
			return fail("cwd must be a host-absolute path")
		}
		maxSeconds := int64(^uint64(0)>>1) / int64(time.Second)
		if s.StartupTimeoutSeconds < 0 || s.ToolTimeoutSeconds < 0 ||
			int64(s.StartupTimeoutSeconds) > maxSeconds || int64(s.ToolTimeoutSeconds) > maxSeconds {
			return fail("timeouts must be zero (default) or positive duration-safe seconds")
		}
		if s.URL != "" {
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
				u.User != nil || u.Fragment != "" {
				return fail("url must be an http(s) endpoint without userinfo or fragment")
			}
			if s.Command != "" || len(s.Args) != 0 || len(s.Env) != 0 || len(s.EnvVars) != 0 || s.CWD != "" {
				return fail("stdio options cannot be combined with url")
			}
		} else if len(s.HTTPHeaders) != 0 || s.BearerTokenEnv != "" {
			return fail("HTTP options require url")
		}
		for key, value := range s.HTTPHeaders {
			if key == "" || strings.ContainsAny(key, " \t\r\n:") || strings.ContainsAny(value, "\r\n") ||
				http.CanonicalHeaderKey(key) == "" {
				return fail("invalid HTTP header")
			}
		}
		for key := range s.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") {
				return fail("invalid environment variable name")
			}
			if strings.ContainsRune(s.Env[key], '\x00') {
				return fail("invalid environment variable value")
			}
		}
		for _, key := range s.EnvVars {
			if key == "" || strings.ContainsAny(key, "=\x00") {
				return fail("invalid envVars name")
			}
		}
		if strings.ContainsAny(s.BearerTokenEnv, "=\x00") {
			return fail("invalid bearerTokenEnv name")
		}
		for _, list := range [][]string{s.EnabledTools, s.DisabledTools} {
			for _, raw := range list {
				if strings.TrimSpace(raw) == "" || strings.ContainsRune(raw, '\x00') {
					return fail("tool filters must contain non-empty raw MCP names")
				}
			}
		}
	}
	return nil
}
