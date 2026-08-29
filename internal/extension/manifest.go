package extension

import (
	"encoding/json"
	"fmt"
	"io"
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
	maxI18nFile    = 256 * 1024
	runtimeNone    = "none"
	runtimeRPC     = "rpc"
)

var nameRe = regexp.MustCompile(namePattern)
var localeRe = regexp.MustCompile(`^[A-Za-z0-9]+(?:[-_][A-Za-z0-9]+)*$`)

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
	Config       ConfigSpec                       `json:"config"`
	I18n         I18nSpec                         `json:"i18n,omitzero"`
	Runtime      RuntimeSpec                      `json:"runtime"`
}

// I18nSpec declares static, extension-owned translation files. The host reads
// these files as data and exposes the validated bundle to WebUI clients; it
// never merges extension keys into the host dictionary.
type I18nSpec struct {
	DefaultLocale string            `json:"defaultLocale,omitempty"`
	Resources     map[string]string `json:"resources,omitempty"`
}

// I18nCatalog is the validated translation bundle exposed in the extension
// catalog. Resource values are flat keys so an extension can use dots in its
// own namespace without making the host understand its message hierarchy.
type I18nCatalog struct {
	DefaultLocale string                       `json:"defaultLocale,omitempty"`
	Resources     map[string]map[string]string `json:"resources,omitempty"`
}

// ConfigSpec declares the JSON-schema-like form rendered by the generic
// settings UI. The host stores values separately and never exposes secrets.
type ConfigSpec struct {
	Schema   map[string]any `json:"schema,omitempty"`
	Defaults map[string]any `json:"defaults,omitempty"`
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
	Enabled      bool                             `json:"enabled"`
	Capabilities []string                         `json:"capabilities"`
	Providers    []provider.ExtensionProviderSpec `json:"providers,omitempty"`
	Config       ConfigSpec                       `json:"config,omitzero"`
	I18n         *I18nCatalog                     `json:"i18n,omitempty"`
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
func Discover(home string, toggle session.Toggle) Discovery {
	byName := map[string]Descriptor{}
	loadDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			root := filepath.Join(dir, e.Name())
			d, ok := loadPackage(root)
			if !ok {
				continue
			}
			d.Enabled = toggle.Allowed(d.Name)
			byName[d.Name] = d
		}
	}
	if home != "" {
		loadDir(filepath.Join(home, "extensions"))
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

func loadPackage(root string) (Descriptor, bool) {
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
		d := Descriptor{Name: filepath.Base(abs), Path: abs, Error: "invalid extension.json"}
		return d, true
	}
	d := Descriptor{
		Name:         m.Name,
		Version:      m.Version,
		Description:  m.Description,
		Path:         abs,
		Capabilities: m.Capabilities,
		Providers:    m.Providers,
		Config:       m.Config,
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
	d.I18n = loadI18n(abs, m.I18n)
	return d, true
}

func validateManifest(root string, m Manifest) error {
	for _, capability := range m.Capabilities {
		if !knownKinds[Kind(capability)] {
			return fmt.Errorf("%w %q", errUnknownCapability, capability)
		}
	}
	kind := m.Runtime.Kind
	if kind == "" {
		kind = runtimeNone
	}
	if kind != runtimeNone && kind != runtimeRPC {
		return fmt.Errorf("%w %q", errUnknownRuntimeKind, m.Runtime.Kind)
	}
	needsCode := needsCodeRuntime(m.Capabilities)
	if kind == runtimeRPC {
		if m.Runtime.Command == "" {
			return errRuntimeCommandRequired
		}
		if !needsCode && !hasKind(m.Capabilities, CapCommand) {
			return errRuntimeRPCRequiresCapability
		}
	}
	if needsCode && kind != runtimeRPC {
		return errCodeCapabilitiesRequireRPC
	}
	if hasKind(m.Capabilities, CapProvider) {
		if len(m.Providers) == 0 {
			return errProviderCapabilityNeedsSpecs
		}
		for _, spec := range m.Providers {
			if err := provider.ValidateExtensionProviderSpec(spec); err != nil {
				return fmt.Errorf("validate provider: %w", err)
			}
		}
	} else if len(m.Providers) > 0 {
		return errProvidersNeedCapability
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
	if m.I18n.DefaultLocale != "" && !localeRe.MatchString(m.I18n.DefaultLocale) {
		return fmt.Errorf("%w %q", errInvalidI18nDefaultLocale, m.I18n.DefaultLocale)
	}
	for locale, rel := range m.I18n.Resources {
		if !localeRe.MatchString(locale) {
			return fmt.Errorf("%w %q", errInvalidI18nLocale, locale)
		}
		if err := withinRoot(root, rel); err != nil {
			return fmt.Errorf("i18n resource %q: %w", locale, err)
		}
	}
	return nil
}

func loadI18n(root string, spec I18nSpec) *I18nCatalog {
	if len(spec.Resources) == 0 {
		return nil
	}
	locales := make([]string, 0, len(spec.Resources))
	for locale := range spec.Resources {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	catalog := &I18nCatalog{DefaultLocale: spec.DefaultLocale, Resources: map[string]map[string]string{}}
	for _, locale := range locales {
		path := filepath.Join(root, filepath.Clean(spec.Resources[locale]))
		//nolint:gosec // the manifest path was checked by validateManifest.
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxI18nFile+1))
		_ = file.Close()
		// Why: translation assets are optional presentation data. A broken or
		// oversized locale must not prevent an otherwise usable extension from
		// starting; WebUI will use the declared fallback/locale key instead.
		if readErr != nil || len(raw) > maxI18nFile || !utf8.Valid(raw) {
			continue
		}
		var messages map[string]string
		if err := json.Unmarshal(raw, &messages); err != nil || messages == nil {
			continue
		}
		clean := make(map[string]string, len(messages))
		for key, value := range messages {
			if strings.TrimSpace(key) == "" || !utf8.ValidString(value) {
				continue
			}
			clean[key] = value
		}
		catalog.Resources[locale] = clean
	}
	if len(catalog.Resources) == 0 {
		return nil
	}
	if catalog.DefaultLocale == "" {
		if _, ok := catalog.Resources["en"]; ok {
			catalog.DefaultLocale = "en"
		} else {
			for _, locale := range locales {
				if _, ok := catalog.Resources[locale]; ok {
					catalog.DefaultLocale = locale
					break
				}
			}
		}
	}
	return catalog
}

func withinRoot(root, rel string) error {
	if rel == "" {
		return errEmptyPath
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%w: %s", errPathMustBeRelative, rel)
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") {
		return fmt.Errorf("%w: %s", errPathEscapesPackage, rel)
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
		return fmt.Errorf("%w: %s", errPathEscapesPackage, rel)
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

// HasRuntime reports whether this descriptor declares an enabled RPC runtime.
func (d Descriptor) HasRuntime() bool { return d.wantsSidecar() }

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
