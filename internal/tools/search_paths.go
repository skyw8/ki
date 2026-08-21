package tools

import (
	"path/filepath"
	"strings"
)

func displaySearchPath(cwd, path string) string {
	base := cwd
	if base == "" {
		base = "."
	}
	base, _ = filepath.Abs(base)
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(abs)
	}
	if rel == "." {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// splitAbsoluteGlob mirrors Claude Code's optimization: an absolute glob is
// split into a traversal root and a relative --glob pattern because ripgrep's
// glob filter is relative to its search root.
func splitAbsoluteGlob(pattern string) (root, relative string, ok bool) {
	if !filepath.IsAbs(pattern) {
		return "", pattern, false
	}
	first := strings.IndexAny(pattern, "*?[{")
	if first < 0 {
		return filepath.Dir(pattern), filepath.Base(pattern), true
	}
	staticPrefix := pattern[:first]
	separator := strings.LastIndexAny(staticPrefix, `/\`)
	if separator < 0 {
		return filepath.Dir(pattern), filepath.Base(pattern), true
	}
	root = staticPrefix[:separator]
	if root == "" {
		root = string(filepath.Separator)
	}
	if volume := filepath.VolumeName(root); volume != "" && root == volume {
		root += string(filepath.Separator)
	}
	return root, pattern[separator+1:], true
}
