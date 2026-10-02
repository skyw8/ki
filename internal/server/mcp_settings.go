package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"ki/internal/config"
	mcpruntime "ki/internal/mcp"
	"ki/internal/session"
)

// MCPServerInfo is a credential-free projection of next-occupy configuration.
type MCPServerInfo struct {
	Name              string `json:"name"`
	Source            string `json:"source"`
	Transport         string `json:"transport"`
	Enabled           bool   `json:"enabled"`
	ConfiguredEnabled bool   `json:"configuredEnabled"`
	Tools             *int   `json:"tools,omitempty"`
}

const maxMCPManagers = 32

type mcpManagerEntry struct {
	manager *mcpruntime.Manager
	refs    int
	used    uint64
}

func (s *Server) toolSettingsCWD(r *http.Request) (string, int, error) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	if id := strings.TrimSpace(r.URL.Query().Get("sessionId")); id != "" {
		sess, err := s.open(id)
		if err != nil {
			return "", http.StatusNotFound, err
		}
		cwd = sess.Header.CWD
		_ = sess.Close()
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", http.StatusInternalServerError, err
		}
	}
	return cwd, http.StatusOK, nil
}

func mergeMCPDisabled(previous, requested []string, editable map[string]bool) []string {
	// A global settings document is edited through workspace-local catalogs.
	// Replacing the entire list would silently re-enable another workspace's
	// servers, including servers temporarily absent or disabled in their file.
	names := make([]string, 0, len(previous)+len(requested))
	for _, name := range previous {
		if !editable[name] {
			names = append(names, name)
		}
	}
	return append(names, requested...)
}

func (s *Server) resolveMCP(cwd string) (config.MCPSnapshot, error) {
	snapshot, err := config.LoadMCPSnapshot(s.cfg.Home, cwd)
	if err != nil {
		return snapshot, err
	}
	if !snapshot.FilesPresent && !s.cfg.MCPFromFiles {
		// Injected configurations are useful to embedders and protocol tests;
		// an explicitly empty JSON document must still replace this fallback.
		snapshot.Servers = s.cfg.MCPServers
		snapshot.Sources = make(map[string]string, len(snapshot.Servers))
		for name := range snapshot.Servers {
			snapshot.Sources[name] = "runtime"
		}
	}
	servers := make(map[string]config.MCPServer, len(snapshot.Servers))
	for name, server := range snapshot.Servers {
		// A session's program starts in that workspace, never the daemon cwd.
		if server.Command != "" && server.CWD == "" {
			server.CWD = cwd
		}
		servers[name] = server
	}
	snapshot.Servers = servers
	return snapshot, nil
}

func allowedMCP(snapshot config.MCPSnapshot, toggle session.Toggle) map[string]config.MCPServer {
	servers := make(map[string]config.MCPServer)
	for name, server := range snapshot.Servers {
		if server.IsEnabled() && toggle.Allowed(name) {
			servers[name] = server
		}
	}
	return servers
}

func mcpConfigKey(servers map[string]config.MCPServer) string {
	raw, _ := json.Marshal(servers)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// acquireMCP pins a frozen configuration through the whole occupy. Idle
// snapshots may be evicted, but updating settings never closes active clients.
func (s *Server) acquireMCP(snapshot config.MCPSnapshot, toggle session.Toggle) (*mcpruntime.Manager, func(), error) {
	servers := allowedMCP(snapshot, toggle)
	if len(servers) == 0 {
		return nil, func() {}, nil
	}
	key := mcpConfigKey(servers)
	s.mcpMu.Lock()
	if s.mcpClosed {
		s.mcpMu.Unlock()
		return nil, func() {}, errRuntimeClosed
	}
	if s.mcpManagers == nil {
		s.mcpManagers = make(map[string]*mcpManagerEntry)
	}
	var retired *mcpruntime.Manager
	entry := s.mcpManagers[key]
	if entry == nil {
		if len(s.mcpManagers) >= maxMCPManagers {
			var oldestKey string
			var oldest *mcpManagerEntry
			for candidateKey, candidate := range s.mcpManagers {
				if candidate.refs == 0 && (oldest == nil || candidate.used < oldest.used) {
					oldestKey, oldest = candidateKey, candidate
				}
			}
			if oldest == nil {
				s.mcpMu.Unlock()
				return nil, func() {}, errors.New("MCP configuration capacity reached")
			}
			delete(s.mcpManagers, oldestKey)
			retired = oldest.manager
		}
		entry = &mcpManagerEntry{manager: mcpruntime.NewManager(servers)}
		s.mcpManagers[key] = entry
	}
	s.mcpClock++
	entry.used, entry.refs = s.mcpClock, entry.refs+1
	s.mcpMu.Unlock()
	// SDK close/discovery must not hold the cache lock: either can wait for
	// peer I/O, while unrelated occupies and read-only settings must progress.
	if retired != nil {
		_ = retired.Close()
	}
	return entry.manager, func() {
		s.mcpMu.Lock()
		entry.refs--
		s.mcpMu.Unlock()
	}, nil
}

func (s *Server) closeMCPManagers() error {
	s.mcpMu.Lock()
	s.mcpClosed = true
	managers := s.mcpManagers
	s.mcpManagers = nil
	s.mcpMu.Unlock()
	var errs []error
	for _, entry := range managers {
		errs = append(errs, entry.manager.Close())
	}
	return errors.Join(errs...)
}

func (s *Server) mcpInfos(snapshot config.MCPSnapshot, toggle session.Toggle) []MCPServerInfo {
	servers := allowedMCP(snapshot, toggle)
	key := mcpConfigKey(servers)
	s.mcpMu.Lock()
	entry := s.mcpManagers[key]
	s.mcpMu.Unlock()
	counts := map[string]int{}
	if entry != nil {
		for _, tool := range entry.manager.Catalog() {
			if source, ok := tool.(interface{ SourceInfo() (string, string) }); ok {
				name, _ := source.SourceInfo()
				counts[name]++
			}
		}
	}
	names := make([]string, 0, len(snapshot.Servers))
	for name := range snapshot.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]MCPServerInfo, 0, len(names))
	for _, name := range names {
		server := snapshot.Servers[name]
		transport := "http"
		if server.Command != "" {
			transport = "stdio"
		}
		info := MCPServerInfo{
			Name: name, Source: snapshot.Sources[name], Transport: transport,
			ConfiguredEnabled: server.IsEnabled(), Enabled: server.IsEnabled() && toggle.Allowed(name),
		}
		if count, ok := counts[name]; ok {
			info.Tools = &count
		}
		items = append(items, info)
	}
	return items
}

func validateMCPDisabled(raw json.RawMessage) ([]string, error) {
	var names []string
	if string(raw) == "null" || json.Unmarshal(raw, &names) != nil || names == nil {
		return nil, errors.New("mcpDisabled must be an array of server names")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" || strings.ContainsRune(name, '\x00') || seen[name] {
			return nil, errors.New("mcpDisabled requires nonblank, unique server names")
		}
		seen[name] = true
	}
	return names, nil
}
