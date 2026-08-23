package resources

import (
	"encoding/json"
	"sync"
	"time"

	"ki/internal/mcp"
	"ki/internal/skills"
)

// Snapshot is the complete immutable resource view pinned to one session.
type Snapshot struct {
	Environment  Environment
	ContextFiles []ContextFile
	Skills       []skills.Skill
	Prompts      []PromptTemplate
	MCP          mcp.File
	MCPServers   map[string]mcp.ServerState
	Revision     uint64
}

// Loader caches complete snapshots by real session id. It belongs to one
// Server, so its configured home provides the cache namespace.
type Loader struct {
	home      string
	mu        sync.Mutex
	entries   map[string]Snapshot
	revisions map[string]uint64
}

// NewLoader creates a server-scoped resource loader.
func NewLoader(home string) *Loader {
	return &Loader{home: home, entries: map[string]Snapshot{}, revisions: map[string]uint64{}}
}

// Load returns the snapshot pinned to sessionID, discovering every resource
// together on the first call. The lock intentionally covers discovery: reload
// cannot interleave and publish a mixed old/new snapshot.
func (l *Loader) Load(sessionID, cwd string) Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if snapshot, ok := l.entries[sessionID]; ok {
		return snapshot
	}
	l.revisions[sessionID]++
	snapshot := l.scan(cwd)
	snapshot.Revision = l.revisions[sessionID]
	l.entries[sessionID] = snapshot
	return snapshot
}

// Scan discovers a current snapshot without caching it. Settings views use
// this because they are workspace-scoped and do not have a real session id.
func (l *Loader) Scan(cwd string) Snapshot {
	return l.scan(cwd)
}

func (l *Loader) scan(cwd string) Snapshot {
	return Snapshot{
		Environment:  loadEnvironment(l.home, cwd, time.Now()),
		ContextFiles: collectContextFiles(l.home, cwd),
		Skills:       skills.Scan(l.home, cwd),
		Prompts:      scanPromptTemplates(l.home, cwd),
		MCP:          mcp.Load(l.home, cwd),
		MCPServers:   map[string]mcp.ServerState{},
	}
}

// UpdateMCP atomically publishes discovery results for one snapshot revision.
// A reload increments the revision, so late SDK handshakes cannot repopulate a
// newly invalidated session with stale tools.
func (l *Loader) UpdateMCP(sessionID string, revision uint64, updates map[string]mcp.ServerState) (Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot, ok := l.entries[sessionID]
	if !ok || snapshot.Revision != revision {
		return Snapshot{}, false
	}
	states := make(map[string]mcp.ServerState, len(snapshot.MCPServers)+len(updates))
	for name, state := range snapshot.MCPServers {
		states[name] = cloneMCPState(state)
	}
	for name, state := range updates {
		if previous, exists := states[name]; exists && previous.Status == mcp.StatusStale && state.Status == mcp.StatusReady {
			state.Status = mcp.StatusStale
			state.Error = previous.Error
			state.EventID = previous.EventID
		}
		states[name] = cloneMCPState(state)
	}
	snapshot.MCPServers = states
	l.entries[sessionID] = snapshot
	return snapshot, true
}

// MarkMCPStatus updates one live server without changing its cached tools.
func (l *Loader) MarkMCPStatus(sessionID, name string, status mcp.ServerStatus, message, eventID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot, ok := l.entries[sessionID]
	if !ok {
		return false
	}
	states := make(map[string]mcp.ServerState, len(snapshot.MCPServers)+1)
	for key, state := range snapshot.MCPServers {
		states[key] = cloneMCPState(state)
	}
	state := states[name]
	state.Status = status
	state.Error = message
	state.EventID = eventID
	states[name] = state
	snapshot.MCPServers = states
	l.entries[sessionID] = snapshot
	return true
}

func cloneMCPState(state mcp.ServerState) mcp.ServerState {
	tools := make([]mcp.ToolDefinition, len(state.Tools))
	for i, tool := range state.Tools {
		raw, err := json.Marshal(tool)
		if err == nil && json.Unmarshal(raw, &tools[i]) == nil {
			continue
		}
		tools[i] = tool
	}
	state.Tools = tools
	state.Capabilities = append([]byte(nil), state.Capabilities...)
	return state
}

// Invalidate drops one session's snapshot.
func (l *Loader) Invalidate(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, sessionID)
	l.revisions[sessionID]++
}

// InvalidateAll drops every session snapshot. Subsequent access re-reads disk.
func (l *Loader) InvalidateAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = map[string]Snapshot{}
	for id := range l.revisions {
		l.revisions[id]++
	}
}

// InvalidateAllExcept preserves snapshots owned by active runs. Their reload
// is queued by the server and applied after the fixed request header finishes.
func (l *Loader) InvalidateAllExcept(keep map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.entries {
		if keep[id] {
			continue
		}
		delete(l.entries, id)
		l.revisions[id]++
	}
}
