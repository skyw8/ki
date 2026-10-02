package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type downloadTransport func(*http.Request) (*http.Response, error)

func (f downloadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func BenchmarkTelegramDownload(b *testing.B) {
	data := strings.Repeat("x", 8<<20)
	api := &botAPI{base: "https://telegram.invalid", token: "token", client: &http.Client{Transport: downloadTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(data)), Header: http.Header{}, Request: req}, nil
	})}}
	target := filepath.Join(b.TempDir(), "attachment")
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		file, err := os.CreateTemp(filepath.Dir(target), ".download-*")
		if err != nil {
			b.Fatal(err)
		}
		if err := api.download(context.Background(), "file", file); err != nil {
			b.Fatal(err)
		}
		if err := file.Close(); err != nil {
			b.Fatal(err)
		}
		if err := os.Rename(file.Name(), target); err != nil {
			b.Fatal(err)
		}
	}
}

type failedDownloadReader struct{}

func (failedDownloadReader) Read(p []byte) (int, error) {
	return copy(p, "partial"), io.ErrUnexpectedEOF
}

type repeatedDownloadReader struct{}

func (repeatedDownloadReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestDownloadFilePublishesOnlyCompleteAttachments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   io.Reader
		status int
	}{
		{"success", strings.NewReader("complete"), http.StatusOK},
		{"truncated", failedDownloadReader{}, http.StatusOK},
		{"too-large", io.LimitReader(repeatedDownloadReader{}, telegramDownloadLimit+1), http.StatusOK},
		{"http-error", strings.NewReader("unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &botAPI{base: "https://telegram.invalid", token: "token", client: &http.Client{Transport: downloadTransport(func(req *http.Request) (*http.Response, error) {
				status, body := tc.status, tc.body
				if strings.HasSuffix(req.URL.Path, "/getFile") {
					status, body = http.StatusOK, strings.NewReader(`{"ok":true,"result":{"file_path":"files/file"}}`)
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(body), Header: http.Header{}, Request: req}, nil
			})}}
			target := filepath.Join(t.TempDir(), "attachment")
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			worker := &telegramWorker{ctx: context.Background(), api: api}
			path, err := worker.downloadFile("id", target)
			want := "original"
			if tc.name == "success" {
				want = "complete"
				if err != nil || path != target {
					t.Fatalf("download = %q, %v", path, err)
				}
			} else if err == nil || path != "" {
				t.Fatalf("failed download = %q, %v", path, err)
			}
			body, readErr := os.ReadFile(target)
			if readErr != nil || string(body) != want {
				t.Fatalf("attachment = %q, %v; want %q", body, readErr, want)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".download-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary files = %v, %v", files, err)
			}
		})
	}
}

func TestDownloadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &botAPI{base: "https://telegram.invalid", client: &http.Client{Transport: downloadTransport(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}}
	if err := api.download(ctx, "file", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("download error = %v", err)
	}
}

func TestDownloadAcceptsExactSizeLimit(t *testing.T) {
	api := &botAPI{base: "https://telegram.invalid", client: &http.Client{Transport: downloadTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.LimitReader(repeatedDownloadReader{}, telegramDownloadLimit)), Header: http.Header{}, Request: req}, nil
	})}}
	if err := api.download(context.Background(), "file", io.Discard); err != nil {
		t.Fatal(err)
	}
}
