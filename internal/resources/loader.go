package resources

import (
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
}

// Loader caches complete snapshots by real session id. It belongs to one
// Server, so its configured home provides the cache namespace.
type Loader struct {
	home    string
	mu      sync.Mutex
	entries map[string]Snapshot
}

// NewLoader creates a server-scoped resource loader.
func NewLoader(home string) *Loader {
	return &Loader{home: home, entries: map[string]Snapshot{}}
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
	snapshot := l.scan(cwd)
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
	}
}

// Invalidate drops one session's snapshot.
func (l *Loader) Invalidate(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, sessionID)
}

// InvalidateAll drops every session snapshot. Subsequent access re-reads disk.
func (l *Loader) InvalidateAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = map[string]Snapshot{}
}
