package server

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

var compressibleTypes = map[string]bool{
	"application/json":          true,
	"application/javascript":    true,
	"application/manifest+json": true,
	"application/xml":           true,
	"image/svg+xml":             true,
	"text/css":                  true,
	"text/html":                 true,
	"text/javascript":           true,
	"text/plain":                true,
	"text/xml":                  true,
}

func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, status: http.StatusOK}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	status   int
	header   bool
	decided  bool
	passthru bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.header {
		return
	}
	w.header = true
	w.status = code
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.header {
		w.WriteHeader(http.StatusOK)
	}
	if !w.decided {
		w.decide(b)
	}
	if w.passthru {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

func (w *gzipWriter) decide(first []byte) {
	w.decided = true
	h := w.Header()
	switch {
	case w.status == http.StatusNoContent, w.status == http.StatusNotModified, w.status == http.StatusPartialContent:
		w.passthru = true
	case h.Get("Content-Encoding") != "":
		w.passthru = true
	default:
		ct := h.Get("Content-Type")
		if ct == "" {
			ct = http.DetectContentType(first)
			h.Set("Content-Type", ct)
		}
		if !compressibleTypes[mediaType(ct)] {
			w.passthru = true
		}
	}
	if w.passthru {
		w.ResponseWriter.WriteHeader(w.status)
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	addVary(h, "Accept-Encoding")
	w.ResponseWriter.WriteHeader(w.status)
	w.gz = gzip.NewWriter(w.ResponseWriter)
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		return
	}
	if w.header && !w.decided {
		w.ResponseWriter.WriteHeader(w.status)
	}
}

func (w *gzipWriter) Flush() {
	_ = w.FlushError()
}

func (w *gzipWriter) FlushError() error {
	if w.gz != nil {
		if err := w.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func addVary(h http.Header, value string) {
	for _, v := range h.Values("Vary") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}

func acceptsGzip(r *http.Request) bool {
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		for p := range strings.SplitSeq(params, ";") {
			k, v, ok := strings.Cut(p, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && q == 0 {
				return false
			}
		}
		return true
	}
	return false
}
