package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeReleaseMirror(t *testing.T, release string, packages map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, release)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for name, files := range packages {
		var buf bytes.Buffer
		writer := zip.NewWriter(&buf)
		for path, body := range files {
			entry, err := writer.Create(name + "/" + path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		asset := name + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
		if err := os.WriteFile(filepath.Join(target, asset), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(buf.Bytes())
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + asset + "\n")
	}
	if err := os.WriteFile(filepath.Join(target, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func installTestServer(t *testing.T, mirror string) (*Server, *httptest.Server, string) {
	t.Helper()
	srv, hs := testServer(t)
	srv.cfg.Extensions.Source = "file://" + filepath.ToSlash(mirror)
	srv.cfg.Extensions.Release = "v9.9.9"
	return srv, hs, srv.cfg.Home
}

func waitInstallState(t *testing.T, srv *Server, name string) installState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if state, ok := srv.extInstall.get(name); ok && (state.state == "done" || state.state == "failed") {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("install of %s did not settle", name)
	return installState{}
}

func TestAvailableExtensionsMergeCatalogWithDisk(t *testing.T) {
	srv, hs := testServer(t)
	srv.cfg.Extensions.Release = "v9.9.9"
	res, err := authedGet(t, hs, "/v1/extensions/available")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var out struct {
		Items []struct {
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
			Asset     string `json:"asset"`
		} `json:"items"`
		Release string `json:"release"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Release != "v9.9.9" {
		t.Fatalf("release=%s", out.Release)
	}
	found := false
	for _, item := range out.Items {
		if item.Name == "goal" {
			found = true
			if item.Installed {
				t.Fatal("goal reported installed in an empty home")
			}
			if !strings.HasSuffix(item.Asset, "-"+runtime.GOOS+"-"+runtime.GOARCH+".zip") {
				t.Fatalf("asset=%s", item.Asset)
			}
		}
	}
	if !found {
		t.Fatalf("catalog missing goal: %+v", out.Items)
	}
}

func TestInstallExtensionDownloadsVerifiesAndReloads(t *testing.T) {
	mirror := writeReleaseMirror(t, "v9.9.9", map[string]map[string]string{
		"goal": {
			"extension.json":   `{"name":"goal","version":"9.9.9","description":"installed by test","capabilities":["prompt.append"],"prompt":{"append":["prompt/APPEND.md"]}}`,
			"prompt/APPEND.md": "installed prompt",
		},
	})
	srv, hs, home := installTestServer(t, mirror)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/extensions/goal/install", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if state := waitInstallState(t, srv, "goal"); state.state != "done" {
		t.Fatalf("state=%+v", state)
	}
	manifest := filepath.Join(home, "extensions", "goal", "extension.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "9.9.9") {
		t.Fatalf("manifest=%s", body)
	}
	res2, err := authedGet(t, hs, "/v1/extensions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res2.Body.Close() }()
	var catalog struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range catalog.Items {
		if item.Name == "goal" {
			found = true
		}
	}
	if !found {
		t.Fatalf("installed package missing from catalog: %+v", catalog.Items)
	}
}

func TestInstallExtensionReportsFailureAndKeepsCatalog(t *testing.T) {
	mirror := writeReleaseMirror(t, "v9.9.9", map[string]map[string]string{
		"telegram-bot": {
			"extension.json": `{"name":"telegram-bot","version":"9.9.9","capabilities":["settings"],"config":{"schema":{"type":"object"}}}`,
		},
	})
	srv, hs, home := installTestServer(t, mirror)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/extensions/goal/install", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", res.StatusCode)
	}
	state := waitInstallState(t, srv, "goal")
	if state.state != "failed" || state.err == "" {
		t.Fatalf("state=%+v", state)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "goal")); !os.IsNotExist(err) {
		t.Fatalf("failed install left a package: %v", err)
	}
	res2, err := authedGet(t, hs, "/v1/extensions/available")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res2.Body.Close() }()
	var out struct {
		Items []struct {
			Name    string `json:"name"`
			Install *struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"install"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, item := range out.Items {
		if item.Name != "goal" {
			continue
		}
		if item.Install == nil || item.Install.State != "failed" || item.Install.Error == "" {
			t.Fatalf("install state not exposed: %+v", item)
		}
		return
	}
	t.Fatal("goal missing from available list")
}

func TestInstallExtensionRejectsUnknownName(t *testing.T) {
	_, hs := testServer(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/extensions/nope/install", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", res.StatusCode)
	}
}

func TestInstallExtensionRequiresRelease(t *testing.T) {
	srv, hs := testServer(t)
	srv.cfg.Extensions.Release = ""
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/extensions/goal/install", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d", res.StatusCode)
	}
}

func TestAvailableAndInstallReportALinkedRoot(t *testing.T) {
	srv, hs := testServer(t)
	checkout := t.TempDir()
	if err := os.RemoveAll(filepath.Join(srv.cfg.Home, "extensions")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "goal", "extension.json"), []byte(`{"name":"goal","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, filepath.Join(srv.cfg.Home, "extensions")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	srv.cfg.Extensions.Release = "v9.9.9"

	res, err := authedGet(t, hs, "/v1/extensions/available")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		Items []struct {
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
			Linked    bool   `json:"linked"`
		} `json:"items"`
		LinkedRoot bool `json:"linkedRoot"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.LinkedRoot {
		t.Fatal("linkedRoot not reported")
	}
	for _, item := range out.Items {
		if !item.Linked {
			t.Errorf("%s not marked linked", item.Name)
		}
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/extensions/goal/install", nil)
	req.Header.Set("Authorization", "Bearer tok")
	post, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409 for a linked root", post.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(checkout, "goal", "extension.json")); err != nil {
		t.Fatalf("checkout damaged: %v", err)
	}
}
