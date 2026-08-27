package resources

import (
	"sync"
	"time"

	"ki/internal/extension"
	"ki/internal/skills"
	"ki/internal/toggles"
)

// Snapshot is the complete immutable resource view pinned to one session.
type Snapshot struct {
	Environment        Environment
	ContextFiles       []ContextFile
	AppendSystemPrompt string
	ExtensionPrompts   []extension.PromptLayer
	Skills             []skills.Skill
	Prompts            []PromptTemplate
	Extensions         []extension.Descriptor
	Revision           uint64
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
	tg := toggles.Load(l.home)
	found := extension.Discover(l.home, tg.Extensions)
	prompts := scanPromptTemplates(l.home, cwd)
	var extraDirs []struct {
		Path      string
		Extension string
	}
	for _, d := range extension.CommandDirs(found.Enabled) {
		extraDirs = append(extraDirs, struct {
			Path      string
			Extension string
		}{Path: d.Path, Extension: d.Extension})
	}
	extPrompts := loadExtensionPromptTemplates(extraDirs)
	byName := map[string]PromptTemplate{}
	for _, t := range prompts {
		byName[t.Name] = t
	}
	for _, t := range extPrompts {
		if _, exists := byName[t.Name]; exists {
			continue
		}
		byName[t.Name] = t
	}
	mergedPrompts := make([]PromptTemplate, 0, len(byName))
	for _, t := range byName {
		mergedPrompts = append(mergedPrompts, t)
	}
	return Snapshot{
		Environment:        loadEnvironment(l.home, cwd, time.Now()),
		ContextFiles:       collectContextFiles(l.home, cwd),
		AppendSystemPrompt: loadAppendSystemPrompt(l.home, cwd),
		ExtensionPrompts:   extension.PromptLayers(found.Enabled),
		Skills:             skills.Scan(l.home, cwd, extension.SkillRoots(found.Enabled)...),
		Prompts:            mergedPrompts,
		Extensions:         found.All,
	}
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
