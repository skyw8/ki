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

// writeReleaseMirror lays out a file:// release directory the way the publish
// job does: one zip per extension plus checksums.txt.
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

// installTestServer builds a server whose installer points at a local mirror.
// The configuration is mutated before the first request, which is safe because
// handlers read s.cfg per request.
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
	// A catalog entry whose package is not installed must still be listed.
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
	// The reload after install must make the package discoverable.
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
	// The mirror has no checksum entry for goal, so the install must fail
	// without touching {KI_HOME}/extensions.
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
	// A failed install is reported through the same listing endpoint.
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
