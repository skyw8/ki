package extension

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/session"
)

// releaseFixture writes a release directory (checksums.txt + one asset) that the
// installer reads through a file:// source, so tests never touch the network.
type releaseFixture struct {
	dir    string
	assets map[string][]byte
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	return &releaseFixture{dir: t.TempDir(), assets: map[string][]byte{}}
}

func (r *releaseFixture) add(t *testing.T, name string, files map[string]string, modes map[string]os.FileMode) {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for path, body := range files {
		header := &zip.FileHeader{Name: path, Method: zip.Deflate}
		if mode, ok := modes[path]; ok {
			header.SetMode(mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r.assets[name] = buf.Bytes()
}

func (r *releaseFixture) write(t *testing.T) {
	t.Helper()
	release := filepath.Join(r.dir, "v9.9.9")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for name, body := range r.assets {
		if err := os.WriteFile(filepath.Join(release, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(release, checksumFile), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *releaseFixture) installer(home string) Installer {
	return Installer{
		Home:    home,
		Source:  "file://" + filepath.ToSlash(r.dir),
		Release: "v9.9.9",
		GOOS:    "linux",
		GOARCH:  "amd64",
	}
}

const fixtureManifest = `{"name":"goal","version":"1.0.0","description":"fixture","capabilities":["prompt.append"],"prompt":{"append":["prompt/APPEND.md"]}}`

func manifestFiles() map[string]string {
	return map[string]string{
		"goal/extension.json":   fixtureManifest,
		"goal/prompt/APPEND.md": "fixture prompt",
	}
}

func TestInstallVerifiesChecksumAndPublishesPackage(t *testing.T) {
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", manifestFiles(), nil)
	release.write(t)
	home := t.TempDir()
	installer := release.installer(home)

	stages := []string{}
	installer.OnStage = func(stage string) { stages = append(stages, stage) }
	got, err := installer.Install(t.Context(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "goal" || got.Version != "1.0.0" {
		t.Fatalf("installed=%+v", got)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "goal", "extension.json")); err != nil {
		t.Fatal(err)
	}
	// The archive had a single wrapping directory; it must be stripped.
	if _, err := os.Stat(filepath.Join(home, "extensions", "goal", "goal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrapping directory survived: %v", err)
	}
	if len(stages) != 2 || stages[0] != StageDownload || stages[1] != StageExtract {
		t.Fatalf("stages=%v", stages)
	}
	listed := installer.List()
	// The embedded catalog does not carry this fixture's version, so List is
	// exercised through an explicit catalog entry instead.
	installer.Catalog = InstallCatalog{Version: InstallCatalogVersion, Extensions: []InstallEntry{{Name: "goal", Version: "1.0.0"}}}
	listed = installer.List()
	if len(listed) != 1 || listed[0].Name != "goal" || !listed[0].Installed || listed[0].InstalledVersion != "1.0.0" {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", manifestFiles(), nil)
	release.write(t)
	// Corrupt the published digest without touching the asset.
	sums := filepath.Join(release.dir, "v9.9.9", checksumFile)
	body, err := os.ReadFile(sums)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := strings.Replace(string(body), string(body[0]), "0", 1)
	if err := os.WriteFile(sums, []byte(corrupted), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	_, err = release.installer(home).Install(t.Context(), "goal")
	if !errors.Is(err, errChecksumMismatch) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "goal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed install published a package: %v", err)
	}
}

func TestInstallRejectsEscapingAndSymlinkEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		modes map[string]os.FileMode
		want  error
	}{
		{
			name:  "parent escape",
			files: map[string]string{"goal/extension.json": fixtureManifest, "goal/../../escape.txt": "x"},
			want:  errArchivePathEscapes,
		},
		{
			name:  "absolute path",
			files: map[string]string{"goal/extension.json": fixtureManifest, "/etc/escape.txt": "x"},
			want:  errArchivePathEscapes,
		},
		{
			name:  "symlink",
			files: map[string]string{"goal/extension.json": fixtureManifest, "goal/link": "/etc/passwd"},
			modes: map[string]os.FileMode{"goal/link": os.ModeSymlink | 0o777},
			want:  errArchiveSymlink,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := newReleaseFixture(t)
			release.add(t, "goal-linux-amd64.zip", tc.files, tc.modes)
			release.write(t)
			home := t.TempDir()
			_, err := release.installer(home).Install(t.Context(), "goal")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(home, "extensions", "goal")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed install published a package: %v", err)
			}
		})
	}
}

func TestInstallRequiresMatchingManifest(t *testing.T) {
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", map[string]string{
		"goal/extension.json": `{"name":"telegram-bot","capabilities":["tool"]}`,
	}, nil)
	release.write(t)
	_, err := release.installer(t.TempDir()).Install(t.Context(), "goal")
	if !errors.Is(err, errManifestNameMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallUpgradeKeepsPrivateStateAndReplacesFiles(t *testing.T) {
	home := t.TempDir()
	current := filepath.Join(home, "extensions", "goal")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "extension.json"), []byte(`{"name":"goal","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Private documents must survive; a stale source file must not.
	if err := os.WriteFile(filepath.Join(current, "config.json"), []byte(`{"version":1,"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "stale.go"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", manifestFiles(), nil)
	release.write(t)
	installer := release.installer(home)
	if _, err := installer.Install(t.Context(), "goal"); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(current, "config.json"))
	if err != nil || !strings.Contains(string(config), "secret") {
		t.Fatalf("config=%s err=%v", config, err)
	}
	if _, err := os.Stat(filepath.Join(current, "stale.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file survived the upgrade: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(current, "extension.json"))
	if err != nil || !strings.Contains(string(manifest), "1.0.0") {
		t.Fatalf("manifest=%s err=%v", manifest, err)
	}
}

func TestInstallRefusesToReplaceLinkedCheckout(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "extension.json"), []byte(`{"name":"goal"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, filepath.Join(home, "extensions", "goal")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", manifestFiles(), nil)
	release.write(t)
	_, err := release.installer(home).Install(t.Context(), "goal")
	if !errors.Is(err, errLinkedPackage) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, "extension.json")); err != nil {
		t.Fatalf("checkout was damaged: %v", err)
	}
}

func TestInstallRequiresCatalogEntryAndRelease(t *testing.T) {
	home := t.TempDir()
	installer := Installer{Home: home, Release: "v1.0.0"}
	if _, err := installer.Install(t.Context(), "not-a-package"); !errors.Is(err, errExtensionNotInCatalog) {
		t.Fatalf("err=%v", err)
	}
	installer = Installer{Home: home}
	if _, err := installer.Install(t.Context(), "goal"); !errors.Is(err, errReleaseUnknown) {
		t.Fatalf("err=%v", err)
	}
}

func TestAssetNameTracksPlatform(t *testing.T) {
	installer := Installer{GOOS: "windows", GOARCH: "amd64", Source: "https://example.invalid/releases", Release: "v1.2.3"}
	if got := installer.AssetName("goal"); got != "goal-windows-amd64.zip" {
		t.Fatalf("asset=%s", got)
	}
	want := "https://example.invalid/releases/v1.2.3/goal-windows-amd64.zip"
	if got := installer.AssetURL("goal"); got != want {
		t.Fatalf("url=%s want=%s", got, want)
	}
	if got := installer.ChecksumURL(); got != "https://example.invalid/releases/v1.2.3/checksums.txt" {
		t.Fatalf("checksums=%s", got)
	}
}

func TestInstallRefusesLinkedExtensionsRoot(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	if err := os.Symlink(checkout, filepath.Join(home, "extensions")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	release := newReleaseFixture(t)
	release.add(t, "goal-linux-amd64.zip", manifestFiles(), nil)
	release.write(t)
	_, err := release.installer(home).Install(t.Context(), "goal")
	if !errors.Is(err, errLinkedExtensionsRoot) {
		t.Fatalf("err=%v", err)
	}
	entries, err := os.ReadDir(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("checkout was written through the link: %v", entries)
	}
}

func TestDiscoveryIgnoresHiddenDirectories(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "extensions")
	if err := os.MkdirAll(filepath.Join(root, ".goal-123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goal-123", "extension.json"), []byte(fixtureManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "goal", "extension.json"), []byte(fixtureManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Discover(home, session.Toggle{})
	if len(got.All) != 1 || got.All[0].Name != "goal" {
		t.Fatalf("discovered=%+v", got.All)
	}
}

func TestEmbeddedCatalogListsBundledExtensions(t *testing.T) {
	catalog := BuiltinInstallCatalog()
	if catalog.Version != InstallCatalogVersion || len(catalog.Extensions) == 0 {
		t.Fatalf("catalog=%+v", catalog)
	}
	bundled := filepath.Join("..", "..", "extensions")
	for _, entry := range catalog.Extensions {
		manifest := filepath.Join(bundled, entry.Name, "extension.json")
		raw, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		d, ok := loadPackage(filepath.Join(bundled, entry.Name))
		if !ok || d.Error != "" {
			t.Fatalf("%s does not load: %s", entry.Name, d.Error)
		}
		if d.Version != entry.Version {
			t.Errorf("%s: catalog version %s, manifest %s", entry.Name, entry.Version, d.Version)
		}
		if !bytes.Contains(raw, []byte(`"name": "`+entry.Name+`"`)) {
			t.Errorf("%s: manifest name mismatch", entry.Name)
		}
	}
}
