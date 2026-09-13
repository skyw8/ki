package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ki/web"
)

// TestHashedAssetsAreImmutableAndCompressed exercises the whole stack (embed →
// serveUI → gzipHandler). It needs the built SPA, so it only runs with -tags embed.
func TestHashedAssetsAreImmutableAndCompressed(t *testing.T) {
	if !web.HasAssets() {
		t.Skip("web/dist not embedded; run go test -tags embed to cover the UI")
	}
	_, hs := testServer(t)

	index, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(index.Body)
	_ = index.Body.Close()
	start := strings.Index(string(html), "/assets/")
	if start < 0 {
		t.Fatalf("index.html has no hashed asset reference:\n%s", html)
	}
	rel := string(html)[start:]
	rel = rel[:strings.Index(rel, `"`)]

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set Accept-Encoding ourselves: the transport only auto-decompresses what it
	// negotiated, so the header must survive for us to assert on it.
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("asset %s: %d", rel, res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if len(gunzip(t, body)) == 0 {
		t.Fatal("decompressed asset is empty")
	}
}

func gzipRequest(t *testing.T, accept string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	return req
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}

func TestGzipCompressesLargeJSON(t *testing.T) {
	body := strings.Repeat("index-row-", 2000)
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, "gzip, deflate, br"))

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want removed for gzip framing", got)
	}
	if got := gunzip(t, rec.Body.Bytes()); got != body {
		t.Fatalf("body mismatch: %d bytes", len(got))
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, ""))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGzipSkippedForSSE(t *testing.T) {
	var flushed bool
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, "event: delta\ndata: {}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, "gzip"))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE must not be compressed, Content-Encoding = %q", got)
	}
	if !flushed {
		t.Fatal("wrapper must expose http.Flusher to the SSE handler")
	}
	if !strings.Contains(rec.Body.String(), "event: delta") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGzipSkippedForBodylessStatus(t *testing.T) {
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, "gzip"))

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for 304", got)
	}
}

func TestGzipSkippedForBinary(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, "gzip"))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for image", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatal("image bytes must pass through unchanged")
	}
}

// TestGzipDropsServeContentLength covers the http.ServeContent interaction: it
// sets Content-Length for the uncompressed file before writing, which must not
// survive into a gzipped response.
func TestGzipDropsServeContentLength(t *testing.T) {
	body := strings.Repeat("body{color:red}", 500)
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "app.css", time.Time{}, strings.NewReader(body))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gzipRequest(t, "gzip"))

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want removed", got)
	}
	if got := gunzip(t, rec.Body.Bytes()); got != body {
		t.Fatalf("body mismatch: %d bytes", len(got))
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate, gzip;q=1.0", true},
		{"gzip;q=0", false},
		{"gzip; q=0.0", false},
		{"deflate", false},
		{"identity", false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(gzipRequest(t, tc.header)); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
