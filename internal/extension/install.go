package extension

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// InstallCatalogVersion is the schema version of the embedded install catalog.
const InstallCatalogVersion = 1

// DefaultExtensionSource is the release base URL used when neither ki.toml nor
// the environment overrides it. The installer appends "/<release>/<asset>".
const DefaultExtensionSource = "https://github.com/skyw8/ki/releases"

const (
	// maxArchiveBytes bounds a downloaded extension archive. The largest
	// bundled package (zvec-grep) is under 100 MiB, so this leaves room without
	// letting a wrong URL stream unbounded data into KI_HOME.
	maxArchiveBytes = 512 << 20
	// maxExtractedBytes bounds the unpacked package for the same reason.
	maxExtractedBytes = 1 << 30
	maxArchiveEntry   = 256 << 20
	maxArchiveEntries = 8192
	// checksumFile is the release asset listing every published digest. The
	// installer refuses to unpack anything it cannot verify against it.
	checksumFile = "checksums.txt"
)

// InstallEntry is one bundled extension offered by the embedded catalog. It
// carries no URL: the asset name is derived from the running platform and the
// release tag, so a catalog never has to be regenerated per release.
type InstallEntry struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

// InstallCatalog is installcatalog.json.
type InstallCatalog struct {
	Version    int            `json:"version"`
	Extensions []InstallEntry `json:"extensions"`
}

//go:embed installcatalog.json
var installCatalogFS embed.FS

var builtinInstallCatalog = mustInstallCatalog()

func mustInstallCatalog() InstallCatalog {
	b, err := installCatalogFS.ReadFile("installcatalog.json")
	if err != nil {
		panic(err)
	}
	var c InstallCatalog
	if err := json.Unmarshal(b, &c); err != nil {
		panic(fmt.Errorf("decode embedded extension catalog: %w", err))
	}
	if c.Version != InstallCatalogVersion {
		panic(fmt.Errorf("extension catalog version %d, want %d", c.Version, InstallCatalogVersion))
	}
	seen := map[string]bool{}
	for _, entry := range c.Extensions {
		if !nameRe.MatchString(entry.Name) || seen[entry.Name] {
			panic(fmt.Errorf("invalid or duplicate extension catalog entry %q", entry.Name))
		}
		seen[entry.Name] = true
	}
	return c
}

// BuiltinInstallCatalog returns a fresh copy of the embedded catalog.
func BuiltinInstallCatalog() InstallCatalog {
	out := InstallCatalog{Version: builtinInstallCatalog.Version, Extensions: slices.Clone(builtinInstallCatalog.Extensions)}
	for i := range out.Extensions {
		out.Extensions[i].Capabilities = slices.Clone(out.Extensions[i].Capabilities)
	}
	return out
}

// Available is one installable extension as the settings UI sees it. It merges
// the embedded catalog with what is already on disk; listing never touches the
// network.
type Available struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Asset        string   `json:"asset"`
	Installed    bool     `json:"installed"`
	// InstalledVersion is the manifest version currently on disk, if any.
	InstalledVersion string `json:"installedVersion,omitempty"`
	// UpdateAvailable means a package is installed with a different manifest
	// version than the catalog offers.
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
}

// Install stages reported while a download installs.
const (
	StageDownload = "download"
	StageExtract  = "extract"
)

// Installer downloads release assets into {KI_HOME}/extensions.
type Installer struct {
	Home string
	// Source is the release base URL. Release is appended as one path segment.
	Source  string
	Release string
	GOOS    string
	GOARCH  string
	Client  *http.Client
	// Catalog defaults to the embedded catalog.
	Catalog InstallCatalog
	// OnStage reports coarse progress (StageDownload, StageExtract). It is
	// called synchronously from Install and may be nil.
	OnStage func(stage string)
}

func (i Installer) stage(stage string) {
	if i.OnStage != nil {
		i.OnStage(stage)
	}
}

// AssetName is the release asset for one extension on this platform.
func (i Installer) AssetName(name string) string {
	goos := i.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := i.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return fmt.Sprintf("%s-%s-%s.zip", name, goos, goarch)
}

// AssetURL is the full download URL for one extension.
func (i Installer) AssetURL(name string) string {
	base := strings.TrimRight(i.Source, "/")
	if base == "" {
		base = DefaultExtensionSource
	}
	return base + "/" + i.Release + "/" + i.AssetName(name)
}

// ChecksumURL is the release-wide digest list.
func (i Installer) ChecksumURL() string {
	base := strings.TrimRight(i.Source, "/")
	if base == "" {
		base = DefaultExtensionSource
	}
	return base + "/" + i.Release + "/" + checksumFile
}

func (i Installer) root() string { return filepath.Join(i.Home, "extensions") }

// checkRoot refuses to install when {KI_HOME}/extensions is a link. A developer
// commonly links it at a checkout (as the repository does for its own
// `extensions/`); installing through that link would overwrite sources with a
// release build instead of installing into Ki's home.
func (i Installer) checkRoot() error {
	info, err := os.Lstat(i.root())
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(i.root(), 0o755)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Why explicit: a linked root is a development checkout, and writing a
		// release build into it would replace sources the user is editing.
		return fmt.Errorf("%w: %s (link it away from a checkout or unset KI_HOME/extensions)", errLinkedExtensionsRoot, i.root())
	}
	if !info.IsDir() {
		return errInstallTargetNotDir
	}
	return nil
}

// stagingDir returns a hidden directory inside {KI_HOME}/extensions. Keeping
// staging on the same filesystem makes publishing a plain rename, and the
// leading dot keeps discovery from treating a half-written package as a
// package.
func (i Installer) stagingDir(name string) (string, error) {
	return os.MkdirTemp(i.root(), ".stage-"+name+"-*")
}

// CleanStaging removes abandoned staging directories from an interrupted
// install (a crash or a killed server). Only entries older than an hour are
// removed so a concurrent install in another process keeps its work. Discovery
// already ignores these dot-directories; this only stops them accumulating.
func CleanStaging(home string) error {
	root := filepath.Join(home, "extensions")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".stage-") && !strings.HasPrefix(name, ".replaced-") && !strings.HasPrefix(name, ".download-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
	return nil
}

func (i Installer) catalog() InstallCatalog {
	if len(i.Catalog.Extensions) > 0 {
		return i.Catalog
	}
	return BuiltinInstallCatalog()
}

func (i Installer) entry(name string) (InstallEntry, bool) {
	for _, entry := range i.catalog().Extensions {
		if entry.Name == name {
			return entry, true
		}
	}
	return InstallEntry{}, false
}

// List merges the catalog with installed packages without any network access.
func (i Installer) List() []Available {
	root := i.root()
	out := make([]Available, 0, len(i.catalog().Extensions))
	for _, entry := range i.catalog().Extensions {
		item := Available{
			Name:         entry.Name,
			Version:      entry.Version,
			Description:  entry.Description,
			Capabilities: slices.Clone(entry.Capabilities),
			Asset:        i.AssetName(entry.Name),
		}
		if d, ok := loadPackage(filepath.Join(root, entry.Name)); ok && d.Name == entry.Name {
			item.Installed = true
			item.InstalledVersion = d.Version
			item.UpdateAvailable = d.Version != "" && entry.Version != "" && d.Version != entry.Version
		}
		out = append(out, item)
	}
	return out
}

// Installed describes the package that now sits in {KI_HOME}/extensions.
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

// Install downloads, verifies and unpacks one catalog extension. The new
// package replaces an older install atomically; private state (config.json,
// cache.json, state.json) from the previous install is carried over.
func (i Installer) Install(ctx context.Context, name string) (Installed, error) {
	entry, ok := i.entry(name)
	if !ok {
		return Installed{}, fmt.Errorf("%w: %q", errExtensionNotInCatalog, name)
	}
	if i.Release == "" {
		return Installed{}, errReleaseUnknown
	}
	if err := i.checkRoot(); err != nil {
		return Installed{}, err
	}
	client := i.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	asset := i.AssetName(name)
	sums, err := fetchChecksums(ctx, client, i.ChecksumURL())
	if err != nil {
		return Installed{}, fmt.Errorf("checksums: %w", err)
	}
	want, ok := sums[asset]
	if !ok {
		return Installed{}, fmt.Errorf("%w: %s", errChecksumMissing, asset)
	}
	archive, err := os.CreateTemp(i.root(), ".download-"+name+"-*")
	if err != nil {
		return Installed{}, err
	}
	archivePath := archive.Name()
	defer func() { _ = os.Remove(archivePath) }()
	i.stage(StageDownload)
	hash := sha256.New()
	written, err := fetchInto(ctx, client, i.AssetURL(name), io.MultiWriter(archive, hash), maxArchiveBytes)
	closeErr := archive.Close()
	if err != nil {
		return Installed{}, err
	}
	if closeErr != nil {
		return Installed{}, closeErr
	}
	if written == 0 {
		return Installed{}, errEmptyArchive
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return Installed{}, fmt.Errorf("%w: got %s want %s", errChecksumMismatch, got, want)
	}

	stage, err := i.stagingDir(name)
	if err != nil {
		return Installed{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	i.stage(StageExtract)
	if err := unzip(archivePath, stage, name); err != nil {
		return Installed{}, err
	}
	d, ok := loadPackage(stage)
	if !ok {
		return Installed{}, errManifestMissing
	}
	if d.Name != entry.Name {
		return Installed{}, fmt.Errorf("%w: archive declares %q, want %q", errManifestNameMismatch, d.Name, entry.Name)
	}
	if d.Error != "" {
		return Installed{}, fmt.Errorf("%w: %s", errManifestInvalid, d.Error)
	}

	dest := filepath.Join(i.root(), name)
	if err := carryPrivateState(dest, stage); err != nil {
		return Installed{}, err
	}
	if err := publishPackage(dest, stage); err != nil {
		return Installed{}, err
	}
	return Installed{Name: d.Name, Version: d.Version, Path: dest}, nil
}

// privateStateFiles are extension-owned state documents that must survive an
// upgrade. They never come from the archive.
var privateStateFiles = []string{"config.json", "cache.json", "state.json"}

func carryPrivateState(current, stage string) error {
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// A linked package is a development checkout, not an installation. Moving
	// it aside would delete the checkout, so refuse instead.
	if info.Mode()&os.ModeSymlink != 0 {
		return errLinkedPackage
	}
	if !info.IsDir() {
		return errInstallTargetNotDir
	}
	for _, name := range privateStateFiles {
		from := filepath.Join(current, name)
		data, err := os.ReadFile(from) //nolint:gosec // name is from privateStateFiles
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// publishPackage swaps stage into dest. The stage directory is a hidden sibling
// of dest inside {KI_HOME}/extensions, so both renames stay on one filesystem;
// the previous package is parked in another hidden sibling and removed only
// after the new one is in place. Discovery skips dot-directories, so a crash
// between the two renames leaves an ignorable leftover, never a broken package.
func publishPackage(dest, stage string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stage, dest)
	} else if err != nil {
		return err
	}
	old, err := os.MkdirTemp(parent, ".replaced-"+filepath.Base(dest)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(old) }()
	parked := filepath.Join(old, filepath.Base(dest))
	if err := os.Rename(dest, parked); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		// Put the previous install back: a failed upgrade must not leave the
		// user without the extension they already had.
		if restoreErr := os.Rename(parked, dest); restoreErr != nil {
			return fmt.Errorf("%w (restore failed: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

func fetchChecksums(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	body, err := fetchAll(ctx, client, url, 1<<20)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(digest); err != nil {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = digest
	}
	if len(sums) == 0 {
		return nil, errChecksumEmpty
	}
	return sums, nil
}

func fetchAll(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := fetchInto(ctx, client, url, &buf, limit); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetchInto copies url into w, refusing bodies larger than limit. file:// URLs
// (and plain absolute paths) read from disk so offline installs and tests never
// need a network.
func fetchInto(ctx context.Context, client *http.Client, url string, w io.Writer, limit int64) (int64, error) {
	if local, ok := localPath(url); ok {
		f, err := os.Open(local) //nolint:gosec // operator-supplied source path
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
		n, err := io.Copy(w, io.LimitReader(f, limit+1))
		if err != nil {
			return n, err
		}
		if n > limit {
			return n, errArchiveTooLarge
		}
		return n, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", url, res.Status)
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errArchiveTooLarge
	}
	return n, nil
}

func localPath(url string) (string, bool) {
	if rest, ok := strings.CutPrefix(url, "file://"); ok {
		// Two accepted spellings: `file:///tmp/x` (the canonical form) and
		// `file://C:/x` on Windows. Strip the separator that belongs to the URL
		// rather than to the path, so a mirror works on every platform.
		if len(rest) >= 3 && rest[0] == '/' && rest[2] == ':' &&
			((rest[1] >= 'a' && rest[1] <= 'z') || (rest[1] >= 'A' && rest[1] <= 'Z')) {
			rest = rest[1:]
		}
		return filepath.FromSlash(rest), true
	}
	if filepath.IsAbs(url) {
		return url, true
	}
	return "", false
}

// unzip unpacks src into dst. Every entry must stay inside dst; directories,
// regular files and executable modes are the only accepted shapes. A single
// wrapping top-level directory named after the package is stripped so archives
// produced by plain `zip -r` still work.
func unzip(src, dst, name string) error {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("%w: %v", errArchiveInvalid, err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) > maxArchiveEntries {
		return errArchiveTooLarge
	}
	prefix := archivePrefix(reader.File, name)
	var total int64
	for _, file := range reader.File {
		rel := strings.TrimPrefix(filepath.ToSlash(file.Name), prefix)
		if rel == "" {
			continue
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errArchiveSymlink, file.Name)
		}
		if file.FileInfo().IsDir() {
			target, err := safeArchivePath(dst, strings.TrimSuffix(rel, "/"))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%w: %s", errArchiveEntryType, file.Name)
		}
		target, err := safeArchivePath(dst, rel)
		if err != nil {
			return err
		}
		if file.UncompressedSize64 > maxArchiveEntry {
			return errArchiveTooLarge
		}
		total += int64(file.UncompressedSize64)
		if total > maxExtractedBytes {
			return errArchiveTooLarge
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeArchiveEntry(file, target, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeArchiveEntry(file *zip.File, target string, mode os.FileMode) error {
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	perm := os.FileMode(0o644)
	if mode.Perm()&0o111 != 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(rc, maxArchiveEntry+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func archivePrefix(files []*zip.File, name string) string {
	want := name + "/"
	found := false
	for _, file := range files {
		slash := filepath.ToSlash(file.Name)
		if slash == name || strings.HasPrefix(slash, want) {
			found = true
			continue
		}
		return ""
	}
	if !found {
		return ""
	}
	return want
}

func safeArchivePath(dst, rel string) (string, error) {
	if rel == "" || strings.HasSuffix(rel, "/") {
		return "", errArchiveEntryType
	}
	if isRootedManifestPath(rel) || strings.Contains(rel, `\`) {
		return "", fmt.Errorf("%w: %s", errArchivePathEscapes, rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %s", errArchivePathEscapes, rel)
	}
	target := filepath.Join(dst, filepath.FromSlash(clean))
	// Join already cleaned the path, but the final check keeps a future
	// refactor from reintroducing an escape.
	if inner, err := filepath.Rel(dst, target); err != nil || inner == ".." || strings.HasPrefix(inner, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", errArchivePathEscapes, rel)
	}
	return target, nil
}
