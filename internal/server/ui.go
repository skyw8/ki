package server

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"ki/web"
)

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		s.writeIndex(w, root)
		return
	}
	f, err := root.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Host paths like /data/hgy/tmp are not assets. Serve the SPA
			// here (do not 302 — a full navigation must not go blank).
			s.writeIndex(w, root)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		s.writeIndex(w, root)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, path, stat.ModTime(), strings.NewReader(string(b)))
		return
	}
	http.ServeContent(w, r, path, stat.ModTime(), rs)
}

func (s *Server) writeIndex(w http.ResponseWriter, root fs.FS) {
	b, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "web ui not built (cd web && npm run build)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, string(b))
}
