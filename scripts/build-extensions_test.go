package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestOutputDirectoryProtectsSourcesAndRepositoryAncestors(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "goal", "ki")
	sources := filepath.Join(root, "extensions")
	if err := os.MkdirAll(filepath.Join(sources, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sources, "goal", "main.go")
	if err := os.WriteFile(sentinel, []byte("source must survive"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{root, filepath.Dir(root), base, sources, filepath.Join(sources, "goal"), filepath.Join(sources, "new", "nested")} {
		t.Run(filepath.Base(unsafe), func(t *testing.T) {
			if _, err := safeOutputDirectory(root, unsafe); err == nil {
				t.Fatalf("accepted unsafe output %s", unsafe)
			}
		})
	}
	for _, safe := range []string{filepath.Join(root, "var", "extensions"), filepath.Join(base, "packages")} {
		got, err := safeOutputDirectory(root, safe)
		if err != nil {
			t.Fatal(err)
		}
		if got != safe {
			t.Fatalf("output=%s want=%s", got, safe)
		}
		if _, err := os.Stat(safe); !os.IsNotExist(err) {
			t.Fatalf("validation created output: %s", safe)
		}
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "source must survive" {
		t.Fatalf("sources changed: %v %s", err, data)
	}
}
func TestOutputDirectoryRejectsSymlinksIntoSources(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "repo")
	sources := filepath.Join(root, "extensions")
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "output-link")
	if err := os.Symlink(sources, alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, unsafe := range []string{alias, filepath.Join(alias, "new", "nested")} {
		if _, err := safeOutputDirectory(root, unsafe); err == nil {
			t.Fatalf("accepted source symlink output %s", unsafe)
		}
	}
	ancestor := filepath.Join(base, "parent-link")
	if err := os.Symlink(base, ancestor); err != nil {
		t.Fatal(err)
	}
	if _, err := safeOutputDirectory(root, ancestor); err == nil {
		t.Fatal("accepted symlink to repository ancestor")
	}
	safeRoot := filepath.Join(base, "packages")
	if err := os.MkdirAll(safeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	safeAlias := filepath.Join(base, "safe-link")
	if err := os.Symlink(safeRoot, safeAlias); err != nil {
		t.Fatal(err)
	}
	got, err := safeOutputDirectory(root, filepath.Join(safeAlias, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(safeRoot, "nested") {
		t.Fatalf("unresolved output %s", got)
	}
}
func TestOutputDirectoryRejectsBrokenSymlinkAncestor(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(root, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "broken")
	if err := os.Symlink(filepath.Join(base, "missing"), alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, unsafe := range []string{alias, filepath.Join(alias, "nested")} {
		if _, err := safeOutputDirectory(root, unsafe); err == nil || !strings.Contains(err.Error(), "cannot resolve output path") {
			t.Fatalf("output %s error=%v", unsafe, err)
		}
	}
}

func TestOutputDirectoryProtectsExternalSourceSymlinkAncestors(t *testing.T) {
	root := filepath.Join(canonicalTempDir(t), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	external := canonicalTempDir(t)
	sources := filepath.Join(external, "goal")
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sources, filepath.Join(root, "extensions")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, unsafe := range []string{external, sources, filepath.Join(sources, "nested")} {
		if _, err := safeOutputDirectory(root, unsafe); err == nil {
			t.Fatalf("accepted external source overlap %s", unsafe)
		}
	}
}

func TestSourcePackagesArePortableAndExcludeRuntimeArtifacts(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	// Source staging itself needs no compiler or native build dependencies.
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"codex-oauth", "deep-web-search", "freerouter", "goal", "telegram-bot", "zvec-grep"} {
		t.Run(name, func(t *testing.T) {
			if err := buildPackage(root, destination, name, "windows", "arm64", true); err != nil {
				t.Fatal(err)
			}
			packageRoot := filepath.Join(destination, name)
			module, err := os.ReadFile(filepath.Join(packageRoot, "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(module), "module ki/extensions/"+name+"\n") || !strings.Contains(string(module), "require ki v0.0.0") || !strings.Contains(string(module), "replace ki => ./_ki") {
				t.Fatal(string(module))
			}
			if _, err := os.Stat(filepath.Join(packageRoot, "bin")); !os.IsNotExist(err) {
				t.Fatal("source package contains binary output", err)
			}
			if err := filepath.WalkDir(packageRoot, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Name() == "config.json" || entry.Name() == "cache.json" || entry.Name() == "state.json" || entry.Name() == "target" || entry.Name() == "node_modules" || entry.Name() == "dist" || strings.HasSuffix(entry.Name(), "_test.go") {
					t.Errorf("unexpected runtime/cache/test artifact: %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if name == "zvec-grep" {
				for _, file := range []string{"Cargo.lock", "launcher/main.rs", "native/Cargo.lock", "native/src/main.rs", "native/search-tool.json", "native/UPSTREAM-LICENSE"} {
					if _, err := os.Stat(filepath.Join(packageRoot, filepath.FromSlash(file))); err != nil {
						t.Fatal(file, err)
					}
				}
			}
		})
	}
}
