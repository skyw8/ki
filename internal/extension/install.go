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

const InstallCatalogVersion = 1

const DefaultExtensionSource = "https://github.com/skyw8/ki/releases"

const (
	maxArchiveBytes   = 512 << 20
	maxExtractedBytes = 1 << 30
	maxArchiveEntry   = 256 << 20
	maxArchiveEntries = 8192
	checksumFile      = "checksums.txt"
)

type InstallEntry struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

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

func BuiltinInstallCatalog() InstallCatalog {
	out := InstallCatalog{Version: builtinInstallCatalog.Version, Extensions: slices.Clone(builtinInstallCatalog.Extensions)}
	for i := range out.Extensions {
		out.Extensions[i].Capabilities = slices.Clone(out.Extensions[i].Capabilities)
	}
	return out
}

type Available struct {
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Description      string   `json:"description"`
	Capabilities     []string `json:"capabilities"`
	Asset            string   `json:"asset"`
	Installed        bool     `json:"installed"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	UpdateAvailable  bool     `json:"updateAvailable,omitempty"`
	Linked           bool     `json:"linked,omitempty"`
}

const (
	StageDownload = "download"
	StageExtract  = "extract"
)

type Installer struct {
	Home    string
	Source  string
	Release string
	GOOS    string
	GOARCH  string
	Client  *http.Client
	Catalog InstallCatalog
	OnStage func(stage string)
}

func (i Installer) stage(stage string) {
	if i.OnStage != nil {
		i.OnStage(stage)
	}
}

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

func (i Installer) AssetURL(name string) string {
	base := strings.TrimRight(i.Source, "/")
	if base == "" {
		base = DefaultExtensionSource
	}
	return base + "/" + i.Release + "/" + i.AssetName(name)
}

func (i Installer) ChecksumURL() string {
	base := strings.TrimRight(i.Source, "/")
	if base == "" {
		base = DefaultExtensionSource
	}
	return base + "/" + i.Release + "/" + checksumFile
}

func (i Installer) root() string { return filepath.Join(i.Home, "extensions") }

func (i Installer) checkRoot() error {
	info, err := os.Lstat(i.root())
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(i.root(), 0o755)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s (link it away from a checkout or unset KI_HOME/extensions)", errLinkedExtensionsRoot, i.root())
	}
	if !info.IsDir() {
		return errInstallTargetNotDir
	}
	return nil
}

func (i Installer) stagingDir(name string) (string, error) {
	return os.MkdirTemp(i.root(), ".stage-"+name+"-*")
}

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

func (i Installer) LinkedRoot() bool {
	info, err := os.Lstat(i.root())
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func (i Installer) List() []Available {
	root := i.root()
	linkedRoot := i.LinkedRoot()
	out := make([]Available, 0, len(i.catalog().Extensions))
	for _, entry := range i.catalog().Extensions {
		item := Available{
			Name:         entry.Name,
			Version:      entry.Version,
			Description:  entry.Description,
			Capabilities: slices.Clone(entry.Capabilities),
			Asset:        i.AssetName(entry.Name),
		}
		pkg := filepath.Join(root, entry.Name)
		if d, ok := loadPackage(pkg); ok && d.Name == entry.Name {
			item.Installed = true
			item.InstalledVersion = d.Version
			item.UpdateAvailable = d.Version != "" && entry.Version != "" && d.Version != entry.Version
		}
		info, err := os.Lstat(pkg)
		item.Linked = linkedRoot || (err == nil && info.Mode()&os.ModeSymlink != 0)
		out = append(out, item)
	}
	return out
}

type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

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

var privateStateFiles = []string{"config.json", "cache.json", "state.json"}

func carryPrivateState(current, stage string) error {
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
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
	if inner, err := filepath.Rel(dst, target); err != nil || inner == ".." || strings.HasPrefix(inner, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", errArchivePathEscapes, rel)
	}
	return target, nil
}
