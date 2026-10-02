package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type extensionManifest struct {
	Name    string `json:"name"`
	Runtime struct {
		Kind        string   `json:"kind"`
		Command     string   `json:"command"`
		Install     []string `json:"install"`
		InstallWhen string   `json:"installWhen"`
		Path        []string `json:"path"`
	} `json:"runtime"`
	I18n struct {
		Resources map[string]string `json:"resources"`
	} `json:"i18n"`
	Prompt struct {
		Append []string `json:"append"`
	} `json:"prompt"`
}

func main() {
	out := flag.String("out", "var/extensions", "Directory containing the distributable extension packages")
	sourceOnly := flag.Bool("source", false, "Stage self-contained source packages that build their missing executable at first launch")
	only := flag.String("only", "", "Comma-separated extension names (default: every bundled extension)")
	goos := flag.String("goos", runtime.GOOS, "Target operating system for Go extensions")
	goarch := flag.String("goarch", runtime.GOARCH, "Target architecture for Go extensions")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(errors.New("unexpected positional arguments"))
	}
	root, err := repositoryRoot()
	if err != nil {
		fail(err)
	}
	destination := *out
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(root, destination)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		fail(err)
	}
	destination, err = safeOutputDirectory(root, destination)
	if err != nil {
		fail(err)
	}
	names, err := selectedExtensions(root, *only)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		fail(err)
	}
	for _, name := range names {
		if err := buildPackage(root, destination, name, *goos, *goarch, *sourceOnly); err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
		}
		if *sourceOnly {
			fmt.Fprintf(os.Stderr, "Staged source package %s/%s\n", destination, name)
		} else {
			fmt.Fprintf(os.Stderr, "Built %s/%s for %s/%s\n", destination, name, *goos, *goarch)
		}
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
func within(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

// Resolve existing ancestors before creating output. A symlink to extensions
// would otherwise pass a lexical check and RemoveAll could erase the sources.
func resolvedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	tail := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(tail) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, tail[index])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if _, statErr := os.Lstat(current); statErr == nil {
			// An existing broken link must not be interpreted as a new path.
			return "", fmt.Errorf("cannot resolve output path %s: %w", current, err)
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}
func safeOutputDirectory(root, destination string) (string, error) {
	canonicalRoot, err := resolvedPath(root)
	if err != nil {
		return "", err
	}
	sources, err := resolvedPath(filepath.Join(root, "extensions"))
	if err != nil {
		return "", err
	}
	output, err := resolvedPath(destination)
	if err != nil {
		return "", err
	}
	// Ancestor output can contain a package directory that is itself the
	// repository or one of its ancestors, so package replacement is unsafe.
	if within(output, canonicalRoot) || within(output, sources) || within(sources, output) {
		return "", errors.New("output must be outside extension sources and cannot be the repository or its ancestor")
	}
	return output, nil
}

func repositoryRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{cwd}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(file))
	}
	for _, start := range candidates {
		for current := start; ; current = filepath.Dir(current) {
			if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
				if _, err := os.Stat(filepath.Join(current, "extensions")); err == nil {
					return current, nil
				}
			}
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	return "", errors.New("cannot locate Ki repository: run the script from its checkout")
}
func selectedExtensions(root, selection string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "extensions"))
	if err != nil {
		return nil, err
	}
	available := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(root, "extensions", entry.Name(), "extension.json")); err == nil {
				available[entry.Name()] = true
			}
		}
	}
	names := []string{}
	if selection == "" {
		for name := range available {
			names = append(names, name)
		}
	} else {
		seen := map[string]bool{}
		for _, item := range strings.Split(selection, ",") {
			name := strings.TrimSpace(item)
			if !available[name] {
				return nil, fmt.Errorf("unknown bundled extension %q", name)
			}
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("no bundled extensions found")
	}
	return names, nil
}
func buildPackage(root, destination, name, goos, goarch string, sourceOnly bool) error {
	source := filepath.Join(root, "extensions", name)
	raw, err := os.ReadFile(filepath.Join(source, "extension.json"))
	if err != nil {
		return err
	}
	var manifest extensionManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if manifest.Name != name || manifest.Runtime.Kind != "rpc" || filepath.ToSlash(filepath.Clean(manifest.Runtime.Command)) != "bin/"+name || manifest.Runtime.InstallWhen != "missing" || strings.Join(manifest.Runtime.Install, "\x00") != "go\x00run\x00./install/main.go" {
		return errors.New("bundled manifest must launch bin/<name> with the missing-binary Go installer")
	}
	stage, err := os.MkdirTemp(destination, "."+name+"-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	assets := map[string]bool{"extension.json": true}
	for _, path := range manifest.I18n.Resources {
		assets[path] = true
	}
	for _, path := range manifest.Prompt.Append {
		assets[path] = true
	}
	for path := range assets {
		if err := copyAsset(source, stage, path); err != nil {
			return err
		}
	}
	if sourceOnly {
		if err := stageSourcePackage(root, source, stage, name); err != nil {
			return err
		}
	} else {
		binary := filepath.Join(stage, "bin", name)
		if goos == "windows" {
			binary += ".exe"
		}
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(source, "Cargo.toml")); err == nil {
			if name != "zvec-grep" {
				return errors.New("only zvec-grep is a bundled Rust extension")
			}
			if goos != runtime.GOOS || goarch != runtime.GOARCH {
				return errors.New("zvec-grep requires a native Rust/C++ build; use -only to cross-compile the Go extensions separately")
			}
			command := exec.Command("cargo", "build", "--release", "--locked", "--manifest-path", filepath.Join(source, "Cargo.toml"))
			command.Dir = root
			command.Env = setEnv(os.Environ(), "CARGO_TARGET_DIR", filepath.Join(source, "target"))
			command.Stdout = os.Stderr
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil {
				return err
			}
			compiled := filepath.Join(source, "target", "release", name)
			alias := filepath.Join(stage, "bin", "zg")
			if goos == "windows" {
				compiled += ".exe"
				alias += ".exe"
			}
			if err := copyFile(compiled, binary, 0o755); err != nil {
				return err
			}
			if err := copyFile(compiled, alias, 0o755); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		} else {
			command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, "./extensions/"+name)
			command.Dir = root
			command.Env = setEnv(setEnv(setEnv(os.Environ(), "GOOS", goos), "GOARCH", goarch), "CGO_ENABLED", "0")
			command.Stdout = os.Stderr
			command.Stderr = os.Stderr
			if err := command.Run(); err != nil {
				return err
			}
		}
	}
	final := filepath.Join(destination, name)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	return os.Rename(stage, final)
}
func stageSourcePackage(root, source, stage, name string) error {
	// A small standalone module can be copied away from the repository. Keep the
	// Ki module path for internal/state's visibility, with only shared runtime code.
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	sourceModule := standaloneModule(module, name)
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte(sourceModule), 0644); err != nil {
		return err
	}
	for _, file := range []string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(root, file), filepath.Join(stage, "_ki", file), 0644); err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(root, "go.sum"), filepath.Join(stage, "go.sum"), 0644); err != nil {
		return err
	}
	for _, shared := range []string{"pkg/extensionrpc", "pkg/extensionbuild", "pkg/thinking", "pkg/codexclient", "internal/state"} {
		if err := copyGoSources(filepath.Join(root, filepath.FromSlash(shared)), filepath.Join(stage, "_ki", filepath.FromSlash(shared))); err != nil {
			return err
		}
	}
	if err := copyGoSources(filepath.Join(source, "install"), filepath.Join(stage, "install")); err != nil {
		return err
	}
	if err := copyAsset(source, stage, "README.md"); err != nil {
		return err
	}
	if name == "zvec-grep" {
		for _, file := range []string{"Cargo.toml", "Cargo.lock", "build.rs", "launcher/main.rs", "native/Cargo.toml", "native/Cargo.lock", "native/build.rs", "native/search-tool.json", "native/UPSTREAM-LICENSE"} {
			if err := copyAsset(source, stage, file); err != nil {
				return err
			}
		}
		return filepath.WalkDir(filepath.Join(source, "native", "src"), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".rs" {
				return nil
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			return copyAsset(source, stage, filepath.ToSlash(relative))
		})
	}
	if err := copyGoSources(source, stage); err != nil {
		return err
	}
	for _, file := range []string{"tools.json", "prompts.json"} {
		if _, err := os.Stat(filepath.Join(source, file)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := copyAsset(source, stage, file); err != nil {
			return err
		}
	}
	return nil
}

func standaloneModule(module []byte, name string) string {
	// Windows checkouts can use CRLF. Normalize before replacing the declaration,
	// or the package remains module ki and its local ki dependency is ignored.
	normalized := strings.ReplaceAll(string(module), "\r\n", "\n")
	return strings.Replace(normalized, "module ki\n", "module ki/extensions/"+name+"\n", 1) + "\nrequire ki v0.0.0\n\nreplace ki => ./_ki\n"
}

func copyGoSources(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source must be a regular file: %s", filepath.Join(source, entry.Name()))
		}
		if err := copyFile(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()), 0644); err != nil {
			return err
		}
	}
	return nil
}

func copyAsset(source, stage, path string) error {
	relative := filepath.FromSlash(path)
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("asset path must be package-relative: %q", path)
	}
	info, err := os.Lstat(filepath.Join(source, relative))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("asset must be a regular file: %q", path)
	}
	return copyFile(filepath.Join(source, relative), filepath.Join(stage, relative), 0o644)
}
func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		existing, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(existing, key) {
			continue
		}
		out = append(out, item)
	}
	return append(out, key+"="+value)
}
