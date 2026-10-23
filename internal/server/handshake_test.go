package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteServerFileIsAtomicAndComplete guards the handshake against the read
// race a client hits while a daemon starts: a poller must never observe an
// empty or half-written document.
func TestWriteServerFileIsAtomicAndComplete(t *testing.T) {
	home := t.TempDir()
	want := File{
		Addr:      "127.0.0.1:19800",
		Token:     "tok",
		PID:       4242,
		Build:     "1.2.3",
		Exe:       filepath.Join(home, "ki"),
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := WriteServerFile(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "server.json" {
			t.Fatalf("unexpected leftover file %q", entry.Name())
		}
	}
}

// TestRemoveServerFileKeepsAnotherInstancesHandshake covers the displacement
// case: a daemon that lost its handshake to a newer instance must not delete the
// replacement's file when it finally exits.
func TestRemoveServerFileKeepsAnotherInstancesHandshake(t *testing.T) {
	home := t.TempDir()
	if err := WriteServerFile(home, File{Addr: "127.0.0.1:1", Token: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServerFile(home, "old"); err != nil {
		t.Fatalf("remove with a stale token: %v", err)
	}
	got, err := ReadServerFile(home)
	if err != nil {
		t.Fatalf("handshake of the newer instance was deleted: %v", err)
	}
	if got.Token != "new" {
		t.Fatalf("unexpected handshake: %+v", got)
	}

	if err := RemoveServerFile(home, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadServerFile(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner did not remove its handshake: %v", err)
	}
	// Removing an already absent file is success: `ki stop` runs twice in
	// ordinary scripts.
	if err := RemoveServerFile(home, "new"); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
}

// TestHealthAndMetaReportBuildIdentity keeps the two discovery endpoints honest:
// health is unauthenticated and minimal but still carries the version, while
// meta (authenticated) describes the process for Settings → About.
func TestHealthAndMetaReportBuildIdentity(t *testing.T) {
	srv, hs := testServer(t)

	res, err := http.Get(hs.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var health map[string]any
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["ok"] != true || health["version"] == nil {
		t.Fatalf("health: %+v", health)
	}

	req, err := http.NewRequest(http.MethodGet, hs.URL+"/v1/meta", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res2.Body.Close() }()
	var meta map[string]any
	if err := json.NewDecoder(res2.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "pid", "startedAt", "exe", "kiHome"} {
		if meta[key] == nil || meta[key] == "" {
			t.Fatalf("meta missing %q: %+v", key, meta)
		}
	}
	if meta["pid"] != float64(os.Getpid()) {
		t.Fatalf("meta pid = %v, want %d", meta["pid"], os.Getpid())
	}
	if meta["kiHome"] != srv.cfg.Home {
		t.Fatalf("meta kiHome = %v, want %s", meta["kiHome"], srv.cfg.Home)
	}
}

// TestShutdownRouteSignalsOwner checks the route `ki stop` calls: it answers
// before stopping, and it only closes the stop channel.
func TestShutdownRouteSignalsOwner(t *testing.T) {
	srv, hs := testServer(t)
	req, err := http.NewRequest(http.MethodPost, hs.URL+"/v1/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status: %d", res.StatusCode)
	}
	select {
	case <-srv.StopRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown route did not signal the owner")
	}
	if out, err := http.Post(hs.URL+"/v1/stop", "application/json", nil); err == nil {
		_ = out.Body.Close()
		if out.StatusCode != http.StatusNotFound {
			t.Fatalf("undefined stop route answered %d", out.StatusCode)
		}
	}
}

// TestShutdownRouteRequiresAuth keeps a forwarded origin's visitor from ending
// the daemon with a plain POST.
func TestShutdownRouteRequiresAuth(t *testing.T) {
	_, hs := testServer(t)
	res, err := http.Post(hs.URL+"/v1/shutdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated shutdown: %d", res.StatusCode)
	}
}
