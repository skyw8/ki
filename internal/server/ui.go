package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
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
		http.Error(w, err.Error(), 500)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || !strings.Contains(path, ".") {
		s.writeIndex(w, root)
		return
	}
	f, err := root.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.writeIndex(w, root)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		s.writeIndex(w, root)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, err.Error(), 500)
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
	cwd, _ := os.Getwd()
	boot, _ := json.Marshal(map[string]string{"token": s.token, "cwd": cwd})
	html := strings.Replace(string(b), "__KI_BOOT__", string(boot), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}
