package resources

import (
	"sync"
	"time"

	"ki/internal/extension"
	"ki/internal/skills"
	"ki/internal/toggles"
)

type Snapshot struct {
	Environment         Environment
	ContextFiles        []ContextFile
	AppendSystemPrompts []AppendSystemPrompt
	ExtensionPrompts    []extension.PromptLayer
	PathDirs            []string
	Skills              []skills.Skill
	Prompts             []PromptTemplate
	Extensions          []extension.Descriptor
	Revision            uint64
}

type Loader struct {
	home      string
	mu        sync.Mutex
	entries   map[string]Snapshot
	revisions map[string]uint64
}

func NewLoader(home string) *Loader {
	return &Loader{home: home, entries: map[string]Snapshot{}, revisions: map[string]uint64{}}
}

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
		Environment:         loadEnvironment(l.home, cwd, time.Now()),
		ContextFiles:        collectContextFiles(l.home, cwd),
		AppendSystemPrompts: loadAppendSystemPrompts(l.home, cwd),
		ExtensionPrompts:    extension.PromptLayers(found.Enabled),
		PathDirs:            extension.PathDirs(found.Enabled),
		Skills:              skills.Scan(l.home, cwd, extension.SkillRoots(found.Enabled)...),
		Prompts:             mergedPrompts,
		Extensions:          found.All,
	}
}

func (l *Loader) Invalidate(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, sessionID)
	l.revisions[sessionID]++
}

func (l *Loader) InvalidateAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = map[string]Snapshot{}
	for id := range l.revisions {
		l.revisions[id]++
	}
}

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
