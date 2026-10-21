package extension

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"ki/internal/session"
)

// copyBundledPackage copies a bundled extension into a temp home, skipping build
// output and dependencies: that is exactly what a user's first Discover sees
// before runtime.install runs. Symlinks are skipped too, so a vendored
// environment (a .venv/lib64 symlink, for instance) cannot break the copy.
func copyBundledPackage(t *testing.T, src, dst string) {
	t.Helper()
	const maxCopiedFile = 1 << 20
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// The source fixture must omit the compiled bin directory itself:
			// skipping its large executables alone leaks an empty directory into PATH.
			if rel == "bin" {
				return fs.SkipDir
			}
			switch entry.Name() {
			case "node_modules", "dist", ".git", ".venv", "target", "__pycache__":
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxCopiedFile {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // repo fixture copied into a temp dir
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// TestBundledExtensionsLoad keeps every package under extensions/ installable:
// a manifest error disables the package for real users and is easy to miss in
// review, so the suite reads the shipped manifests instead of only fixtures.
func TestBundledExtensionsLoad(t *testing.T) {
	bundled := filepath.Join("..", "..", "extensions")
	entries, err := os.ReadDir(bundled)
	if err != nil {
		t.Fatalf("bundled extensions: %v", err)
	}
	home := t.TempDir()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(bundled, entry.Name(), "extension.json")); err != nil {
			continue
		}
		copyBundledPackage(t, filepath.Join(bundled, entry.Name()), filepath.Join(home, "extensions", entry.Name()))
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		t.Fatal("no bundled extensions found")
	}
	sort.Strings(names)

	got := Discover(home, session.Toggle{})
	if len(got.All) != len(names) {
		t.Fatalf("discovered %d packages, want %d: %+v", len(got.All), len(names), got.All)
	}
	for _, d := range got.All {
		if d.Error != "" {
			t.Errorf("bundled extension %s does not load: %s", d.Name, d.Error)
		}
		if len(d.Capabilities) == 0 {
			t.Errorf("bundled extension %s declares no capabilities", d.Name)
		}
		assertI18nParity(t, d)
	}
}

func assertI18nParity(t *testing.T, d Descriptor) {
	t.Helper()
	if d.I18n == nil {
		return
	}
	locales := make([]string, 0, len(d.I18n.Resources))
	for locale := range d.I18n.Resources {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	if len(locales) < 2 {
		return
	}
	base := d.I18n.Resources[locales[0]]
	for _, locale := range locales[1:] {
		other := d.I18n.Resources[locale]
		for key := range base {
			if _, ok := other[key]; !ok {
				t.Errorf("%s: locale %s is missing key %q", d.Name, locale, key)
			}
		}
		for key := range other {
			if _, ok := base[key]; !ok {
				t.Errorf("%s: locale %s has extra key %q", d.Name, locale, key)
			}
		}
	}
}
