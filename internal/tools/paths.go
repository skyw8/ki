package tools

import "path/filepath"

func resolve(cwd, p string) string {
	if p == "" {
		return cwd
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if cwd == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(cwd, p))
}
