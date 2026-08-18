package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
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

const (
	maxTextPreviewBytes  = 1 << 20
	maxMediaPreviewBytes = 50 << 20
)

type fsEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Hidden    bool   `json:"hidden"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
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
	if r.URL.Query().Get("preview") == "1" {
		serveFSPreview(w, r, path)
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
	includeFiles := r.URL.Query().Get("files") == "1"
	// JSON null makes browser clients distinguish an empty directory from a
	// normal listing and previously crashed the attachment picker on `.length`.
	dirs := make([]fsEntry, 0)
	files := make([]fsEntry, 0)
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
		if !isDir && !includeFiles {
			continue
		}
		row := fsEntry{
			Name:      e.Name(),
			Path:      child,
			Hidden:    isHiddenName(e.Name(), info),
			Directory: isDir,
			Size:      info.Size(),
		}
		if isDir {
			dirs = append(dirs, row)
		} else {
			files = append(files, row)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	dirs = append(dirs, files...)
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

func serveFSPreview(w http.ResponseWriter, r *http.Request, path string) {
	// The WebUI may be on a different host behind a port-forward, so host paths
	// cannot be used as preview URLs. Stream only recognized safe formats over the
	// authenticated, same-origin filesystem endpoint.
	f, err := os.Open(path) //nolint:gosec // path is the explicit host file selected in the browser
	if err != nil {
		http.Error(w, "file-unreadable", http.StatusBadRequest)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.Error(w, "file-unreadable", http.StatusBadRequest)
		return
	}
	var head [512]byte
	n, _ := f.Read(head[:])
	if _, err := f.Seek(0, 0); err != nil {
		http.Error(w, "file-unreadable", http.StatusBadRequest)
		return
	}
	contentType := http.DetectContentType(head[:n])
	if extType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); !strings.HasPrefix(contentType, "image/") && strings.HasPrefix(extType, "image/") {
		contentType = extType
	}
	ext := strings.ToLower(filepath.Ext(path))
	isPDF := bytes.HasPrefix(head[:n], []byte("%PDF-")) || ext == ".pdf"
	isImage := strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml"
	isText := !bytes.Contains(head[:n], []byte{0}) && utf8.Valid(head[:n])
	if !isImage && !isPDF && !isText {
		http.Error(w, "preview-unavailable", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isText && !isImage && !isPDF {
		// HTML, SVG, and source files are deliberately served as plain text so a
		// selected host file can never execute inside the WebUI origin.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if st.Size() > maxTextPreviewBytes {
			w.Header().Set("X-KI-Preview-Truncated", "1")
		}
		_, _ = io.Copy(w, io.LimitReader(f, maxTextPreviewBytes))
		return
	}
	if st.Size() > maxMediaPreviewBytes {
		http.Error(w, "preview-too-large", http.StatusRequestEntityTooLarge)
		return
	}
	if isPDF {
		contentType = "application/pdf"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
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
