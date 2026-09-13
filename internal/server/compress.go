package server

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// compressibleTypes lists the response media types worth gzipping. These are
// the SPA assets (web/dist) and the JSON API bodies; the session view can carry
// hundreds of kilobytes of index JSON, so the API matters as much as the bundle.
//
// text/event-stream is deliberately absent: the WebUI reads SSE with a Flush per
// event, and buffering those writes inside gzip would stall the stream.
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

// gzipHandler compresses eligible responses in place for one code path that
// covers both serveUI and every /v1 handler.
//
// It is the outermost wrapper so recoverHTTP's error write is compressed too.
// HEAD is left alone because there is no body to compress and the handler's
// Content-Length (which we would have to drop) is the only useful part.
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

// gzipWriter delays committing the status until the first body write. Handlers
// set Content-Type right before they write (writeJSON, http.ServeContent), so
// the type is only final at Write time; deciding earlier could compress a type
// we must not touch, or miss one we should.
//
// The delay is also what lets us drop the Content-Length that http.ServeContent
// computed for the uncompressed body: the compressed length is unknown, so the
// response has to fall back to chunked framing instead of a stale number.
type gzipWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	status   int
	header   bool // WriteHeader already seen from the handler
	decided  bool // first write inspected and framing chosen
	passthru bool // this response is not compressed
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

// decide inspects the first body chunk and commits the response.
func (w *gzipWriter) decide(first []byte) {
	w.decided = true
	h := w.Header()
	switch {
	case w.status == http.StatusNoContent, w.status == http.StatusNotModified, w.status == http.StatusPartialContent:
		// 206 comes from http.ServeContent answering a Range request; the bytes
		// are a slice of the file, not a whole representation to compress.
		w.passthru = true
	case h.Get("Content-Encoding") != "":
		w.passthru = true
	default:
		ct := h.Get("Content-Type")
		if ct == "" {
			// Mirror net/http sniffing: without this the client would see the
			// gzip bytes' type instead of the payload's.
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

// close finishes the gzip stream (the trailer is buffered until here) or, for a
// body-less response such as 304, commits the status we deferred.
func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		return
	}
	if w.header && !w.decided {
		w.ResponseWriter.WriteHeader(w.status)
	}
}

// Flush keeps SSE working through the wrapper. SSE is never compressed, so this
// only has to forward to the underlying writer.
func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func addVary(h http.Header, value string) {
	for _, v := range h.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}

// acceptsGzip reports whether the client offered gzip. An explicit q=0 is a
// refusal even though the token is present.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
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
