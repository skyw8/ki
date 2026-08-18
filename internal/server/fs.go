package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"ki/internal/workspace"
)

const fsCap = 500

type fsEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Hidden bool   `json:"hidden"`
}

type fsListing struct {
	Path      string    `json:"path"`
	Home      string    `json:"home"`
	Separator string    `json:"separator"`
	Crumbs    []fsEntry `json:"crumbs"`
	Entries   []fsEntry `json:"entries"`
	Truncated bool      `json:"truncated"`
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func (s *Server) listFS(w http.ResponseWriter, r *http.Request) {
	home := userHome()
	raw := r.URL.Query().Get("path")
	if strings.TrimSpace(raw) == "" {
		raw = home
	}
	path, err := workspace.Normalize(raw, home)
	if err != nil {
		http.Error(w, "directory-unreadable", http.StatusBadRequest)
		return
	}
	// workspace.Normalize intentionally permits the host-absolute paths exposed
	// by the directory browser; this is not an archive extraction boundary.
	//nolint:gosec // the normalized browser path is an intentional user selection
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		http.Error(w, "directory-unreadable", http.StatusBadRequest)
		return
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, "directory-unreadable", http.StatusBadRequest)
		return
	}
	var dirs []fsEntry
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		child := filepath.Join(path, e.Name())
		isDir := e.IsDir()
		if !isDir {
			if resolved, err := filepath.EvalSymlinks(child); err == nil {
				// The symlink target is inspected only to classify a browser entry.
				//nolint:gosec // this is a normalized, user-selected browser path
				if st, err := os.Stat(resolved); err == nil && st.IsDir() {
					isDir = true
				}
			}
		}
		if !isDir {
			continue
		}
		dirs = append(dirs, fsEntry{
			Name:   e.Name(),
			Path:   child,
			Hidden: isHiddenName(e.Name(), info),
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	trunc := false
	if len(dirs) > fsCap {
		dirs = dirs[:fsCap]
		trunc = true
	}
	writeJSON(w, 200, fsListing{
		Path:      path,
		Home:      home,
		Separator: string(os.PathSeparator),
		Crumbs:    crumbsOf(path),
		Entries:   dirs,
		Truncated: trunc,
	})
}

func crumbsOf(path string) []fsEntry {
	vol := filepath.VolumeName(path)
	var chain []string
	d := path
	for {
		chain = append(chain, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	out := make([]fsEntry, 0, len(chain))
	for _, v := range slices.Backward(chain) {
		p := v
		name := filepath.Base(p)
		if p == "/" || p == vol || p == vol+string(os.PathSeparator) || p == vol+"/" {
			name = p
		}
		out = append(out, fsEntry{Name: name, Path: p})
	}
	return out
}

func (s *Server) createFS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := checkDirName(body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	home := userHome()
	parent, err := workspace.Normalize(body.Path, home)
	if err != nil {
		http.Error(w, "directory-create-failed", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() {
		http.Error(w, "directory-create-failed", http.StatusBadRequest)
		return
	}
	dest := filepath.Join(parent, body.Name)
	if _, err := os.Stat(dest); err == nil {
		http.Error(w, "directory-exists", http.StatusConflict)
		return
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		http.Error(w, "directory-create-failed", http.StatusBadRequest)
		return
	}
	writeJSON(w, 200, map[string]any{"path": dest})
}

func checkDirName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return errBadName
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) || strings.ContainsRune(name, 0) {
		return errBadName
	}
	if utf8.RuneCountInString(name) == 0 {
		return errBadName
	}
	return nil
}

type badNameError string

func (e badNameError) Error() string { return string(e) }

const errBadName badNameError = "invalid name"
