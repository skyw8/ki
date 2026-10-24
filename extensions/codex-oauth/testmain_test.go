package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ki-codex-tests-")
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

func newHTTPTestServer(handler http.Handler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			http.Error(w, "HTTP fixture", http.StatusUpgradeRequired)
			return
		}
		if r.Header.Get("Content-Encoding") == "zstd" {
			decoder, err := zstd.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer decoder.Close()
			original := r.Body
			defer original.Close()
			r.Body = httpBody{Decoder: decoder}
		}
		handler.ServeHTTP(w, r)
	}))
}

type httpBody struct{ *zstd.Decoder }

func (httpBody) Close() error { return nil }
