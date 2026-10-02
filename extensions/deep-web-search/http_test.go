package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestMain(m *testing.M) {
	// The shared Codex helper persists identity even when a test does not need
	// OAuth. Never let such a test write into the developer's real Ki home.
	home, err := os.MkdirTemp("", "ki-dws-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("KI_HOME", home); err != nil {
		_ = os.RemoveAll(home)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func decodeCodexTestBody(raw []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return decoder.DecodeAll(raw, nil)
}

// decodedCodexTestRequests keeps semantic parity fixtures focused on decoded
// JSON. Wire assertions use the original request body instead of this wrapper.
func decodedCodexTestRequests(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "zstd" {
			raw, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err == nil {
				raw, err = decodeCodexTestBody(raw)
			}
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid compressed test request", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
		}
		next.ServeHTTP(w, r)
	})
}
