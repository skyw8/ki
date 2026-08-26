package extension

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"ki/internal/provider"
	"ki/internal/session"
)

const (
	namePattern    = `^[a-z0-9][a-z0-9-]{0,62}$`
	maxPromptFile  = 64 * 1024
	maxPromptTotal = 256 * 1024
	runtimeNone    = "none"
	runtimeRPC     = "rpc"
)

var nameRe = regexp.MustCompile(namePattern)

// Manifest is extension.json.
type Manifest struct {
	Name         string                           `json:"name"`
	Version      string                           `json:"version"`
	Description  string                           `json:"description"`
	Capabilities []string                         `json:"capabilities"`
	FailClosed   bool                             `json:"failClosed"`
	Prompt       PromptSpec                       `json:"prompt"`
	Skills       []string                         `json:"skills"`
	Commands     []string                         `json:"commands"`
	Providers    []provider.ExtensionProviderSpec `json:"providers"`
	Runtime      RuntimeSpec                      `json:"runtime"`
}

// PromptSpec lists files to append as system-prompt layer 6.
type PromptSpec struct {
	Append []string `json:"append"`
}

// RuntimeSpec describes the optional JSON-RPC sidecar.
type RuntimeSpec struct {
	Kind    string            `json:"kind"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Install []string          `json:"install"`
	Env     map[string]string `json:"env"`
}

// Descriptor is one discovered package. It holds no live RPC handles.
type Descriptor struct {
	Name         string                           `json:"name"`
	Version      string                           `json:"version"`
	Description  string                           `json:"description"`
	Path         string                           `json:"path"`
	Scope        string                           `json:"source"`
	Enabled      bool                             `json:"enabled"`
	Capabilities []string                         `json:"capabilities"`
	Providers    []provider.ExtensionProviderSpec `json:"providers,omitempty"`
	Error        string                           `json:"error,omitempty"`
	FailClosed   bool                             `json:"-"`
	manifest     Manifest
	root         string
}

// PromptLayer is one extension append segment.
type PromptLayer struct {
	ExtensionID string
	Text        string
}

// Discovery is the identity-resolved scan of global packages.
type Discovery struct {
	All     []Descriptor
	Enabled []Descriptor // chain order: enabled global packages by name
}

// Discover reads extension.json from the global extension directory.
// toggle.Allowed filters Enabled; disabled packages stay in All with Enabled=false.
func Discover(home, _ string, toggle session.Toggle) Discovery {
	byName := map[string]Descriptor{}
	loadDir := func(dir, scope string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			root := filepath.Join(dir, e.Name())
			d, ok := loadPackage(root, scope)
			if !ok {
				continue
			}
			d.Enabled = toggle.Allowed(d.Name)
			byName[d.Name] = d
		}
	}
	if home != "" {
		loadDir(filepath.Join(home, "extensions"), "home")
	}
	all := make([]Descriptor, 0, len(byName))
	for _, d := range byName {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	// All and Enabled are both globally name-sorted. Keep separate slices so
	// callers can use All for catalog listing without re-filtering disabled packages.
	return Discovery{All: all, Enabled: chainOrder(all)}
}

// chainOrder is the lifecycle and prompt-append order: enabled global packages
// with no load error, sorted by name.
func chainOrder(in []Descriptor) []Descriptor {
	var globals []Descriptor
	for _, d := range in {
		if !d.Enabled || d.Error != "" {
			continue
		}
		globals = append(globals, d)
	}
	sort.Slice(globals, func(i, j int) bool { return globals[i].Name < globals[j].Name })
	out := make([]Descriptor, 0, len(globals))
	out = append(out, globals...)
	return out
}

func loadPackage(root, scope string) (Descriptor, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Descriptor{}, false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		abs = resolved
	}
	path := filepath.Join(abs, "extension.json")
	//nolint:gosec // path is under the configured extension roots.
	b, err := os.ReadFile(path)
	if err != nil {
		return Descriptor{}, false
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		d := Descriptor{Name: filepath.Base(abs), Path: abs, Scope: scope, Error: "invalid extension.json"}
		return d, true
	}
	d := Descriptor{
		Name:         m.Name,
		Version:      m.Version,
		Description:  m.Description,
		Path:         abs,
		Scope:        scope,
		Capabilities: m.Capabilities,
		Providers:    m.Providers,
		FailClosed:   m.FailClosed,
		manifest:     m,
		root:         abs,
	}
	if !nameRe.MatchString(m.Name) || strings.HasPrefix(m.Name, "ki.") {
		d.Error = "invalid extension name"
		return d, true
	}
	if err := validateManifest(abs, m); err != nil {
		d.Error = err.Error()
		return d, true
	}
	return d, true
}

func validateManifest(root string, m Manifest) error {
	for _, capability := range m.Capabilities {
		if !knownKinds[Kind(capability)] {
			return fmt.Errorf("unknown capability %q", capability)
		}
	}
	kind := m.Runtime.Kind
	if kind == "" {
		kind = runtimeNone
	}
	if kind != runtimeNone && kind != runtimeRPC {
		return fmt.Errorf("unknown runtime.kind %q", m.Runtime.Kind)
	}
	needsCode := needsCodeRuntime(m.Capabilities)
	if kind == runtimeRPC {
		if m.Runtime.Command == "" {
			return fmt.Errorf("runtime.command required")
		}
		if !needsCode && !hasKind(m.Capabilities, CapCommand) {
			return fmt.Errorf("runtime.kind=rpc requires tool, lifecycle, bus, or command")
		}
	}
	if needsCode && kind != runtimeRPC {
		return fmt.Errorf("code capabilities require runtime.kind=rpc")
	}
	if hasKind(m.Capabilities, CapProvider) {
		if len(m.Providers) == 0 {
			return fmt.Errorf("provider capability requires providers")
		}
		for _, spec := range m.Providers {
			if err := provider.ValidateExtensionProviderSpec(spec); err != nil {
				return err
			}
		}
	} else if len(m.Providers) > 0 {
		return fmt.Errorf("providers require provider capability")
	}
	for _, rel := range m.Prompt.Append {
		if err := withinRoot(root, rel); err != nil {
			return err
		}
	}
	for _, rel := range m.Skills {
		if err := withinRoot(root, rel); err != nil {
			return err
		}
	}
	for _, rel := range m.Commands {
		if err := withinRoot(root, rel); err != nil {
			return err
		}
	}
	return nil
}

func withinRoot(root, rel string) error {
	if rel == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("path must be relative: %s", rel)
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") {
		return fmt.Errorf("path escapes package: %s", rel)
	}
	joined := filepath.Join(root, clean)
	if resolved, err := filepath.EvalSymlinks(joined); err == nil {
		joined = resolved
	}
	rootResolved := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		rootResolved = r
	}
	relOut, err := filepath.Rel(rootResolved, joined)
	if err != nil || strings.HasPrefix(relOut, "..") {
		return fmt.Errorf("path escapes package: %s", rel)
	}
	return nil
}

func (d Descriptor) wantsSidecar() bool {
	if d.Error != "" || !d.Enabled {
		return false
	}
	kind := d.manifest.Runtime.Kind
	if kind == "" {
		kind = runtimeNone
	}
	return kind == runtimeRPC
}

func (d Descriptor) wantsSessionSidecar() bool {
	if !d.wantsSidecar() {
		return false
	}
	// A package may combine provider code with session-scoped tools or
	// lifecycle hooks. Give those capabilities their normal session process;
	// the provider manager starts a separate process-level instance.
	return hasKind(d.Capabilities, CapTool) || hasKind(d.Capabilities, CapLifecycle) ||
		hasKind(d.Capabilities, CapCommand) || hasKind(d.Capabilities, CapBus)
}

func (d Descriptor) promptText() string {
	if !hasKind(d.Capabilities, CapPromptAppend) || !d.Enabled || d.Error != "" {
		return ""
	}
	var b strings.Builder
	total := 0
	for _, rel := range d.manifest.Prompt.Append {
		p := filepath.Join(d.root, filepath.Clean(rel))
		//nolint:gosec // path checked by withinRoot at load.
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if len(raw) > maxPromptFile {
			raw = raw[:maxPromptFile]
		}
		if total+len(raw) > maxPromptTotal {
			raw = raw[:maxPromptTotal-total]
		}
		if !utf8.Valid(raw) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.Write(raw)
		total += len(raw)
		if total >= maxPromptTotal {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func (d Descriptor) skillRoots() []string {
	if !hasKind(d.Capabilities, CapSkill) || !d.Enabled || d.Error != "" {
		return nil
	}
	var out []string
	for _, rel := range d.manifest.Skills {
		out = append(out, filepath.Join(d.root, filepath.Clean(rel)))
	}
	if len(out) == 0 {
		out = append(out, filepath.Join(d.root, "skills"))
	}
	return out
}

func (d Descriptor) commandDirs() []string {
	if !hasKind(d.Capabilities, CapCommand) || !d.Enabled || d.Error != "" {
		return nil
	}
	var out []string
	for _, rel := range d.manifest.Commands {
		out = append(out, filepath.Join(d.root, filepath.Clean(rel)))
	}
	if len(out) == 0 {
		out = append(out, filepath.Join(d.root, "commands"))
	}
	return out
}
