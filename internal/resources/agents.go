package resources

import (
	"os"
	"path/filepath"
	"slices"
)

// ContextFile is one loaded AGENTS/CLAUDE instruction file.
type ContextFile struct {
	Path    string
	Content string
}

var contextCandidates = []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

// collectContextFiles loads global context followed by repository root→cwd.
func collectContextFiles(home, cwd string) []ContextFile {
	var out []ContextFile
	seen := map[string]bool{}
	add := func(dir string) {
		f := loadContextFile(dir)
		if f == nil || seen[f.Path] {
			return
		}
		seen[f.Path] = true
		out = append(out, *f)
	}
	if home != "" {
		add(home)
	}
	if cwd == "" {
		return out
	}
	root := findGitRoot(cwd)
	var stack []string
	d := cwd
	for {
		stack = append(stack, d)
		if root == "" || sameDir(d, root) || sameDir(d, filepath.Dir(d)) {
			if root == "" {
				// Without a repository boundary, only the explicit cwd contributes.
				stack = []string{cwd}
			}
			break
		}
		p := filepath.Dir(d)
		if p == d {
			break
		}
		d = p
	}
	for _, dir := range slices.Backward(stack) {
		add(dir)
	}
	return out
}

func loadContextFile(dir string) *ContextFile {
	for _, name := range contextCandidates {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		//nolint:gosec // path was discovered under the configured context roots.
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return &ContextFile{Path: path, Content: string(content)}
	}
	return nil
}

func findGitRoot(cwd string) string {
	d := cwd
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func sameDir(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}
