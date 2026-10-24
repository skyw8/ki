package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ki/internal/session"
)

func TestSessionWorkspaceIDsRefreshesMatches(t *testing.T) {
	srv, _ := testServer(t)
	cwd, unmatched := t.TempDir(), t.TempDir()
	rec, _, err := srv.ws.Create(cwd, "registered")
	if err != nil {
		t.Fatal(err)
	}
	infos := []session.Info{{CWD: cwd}, {CWD: cwd}, {CWD: unmatched}, {CWD: unmatched}, {CWD: ""}}
	ids := srv.sessionWorkspaceIDs(infos)
	if len(ids) != 3 || ids[cwd] != rec.ID || ids[unmatched] != "" || ids[""] != "" {
		t.Fatalf("workspace matches = %+v", ids)
	}
	missing := srv.infoMap(session.Info{CWD: unmatched}, sessionActivity{}, ids[unmatched])
	if _, found := missing["workspaceId"]; found {
		t.Fatalf("unmatched session has workspace: %+v", missing)
	}
	rec, _, err = srv.ws.Create(unmatched, "newly registered")
	if err != nil {
		t.Fatal(err)
	}
	ids = srv.sessionWorkspaceIDs(infos)
	if ids[unmatched] != rec.ID {
		t.Fatalf("subsequent request retained missing match: %+v", ids)
	}
	matched := srv.infoMap(session.Info{CWD: unmatched}, sessionActivity{}, ids[unmatched])
	if matched["workspaceId"] != rec.ID {
		t.Fatalf("matched session workspace: %+v", matched)
	}
}

func TestSessionListETag(t *testing.T) {
	_, hs := testServer(t)
	_ = createSession(t, hs, t.TempDir())

	res, err := authedGet(t, hs, "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", res.StatusCode)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	var listed []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(listed) > 0 && listed[0]["questionLinksLoading"] == true {
		if time.Now().After(deadline) {
			t.Fatal("list summary did not settle")
		}
		time.Sleep(time.Millisecond)
		res, err = authedGet(t, hs, "/v1/sessions")
		if err != nil {
			t.Fatal(err)
		}
		etag = res.Header.Get("ETag")
		if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("If-None-Match", etag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("unchanged list status = %d body=%s", res.StatusCode, body)
	}
	if res.Header.Get("ETag") != etag {
		t.Fatalf("304 etag %q != %q", res.Header.Get("ETag"), etag)
	}

	_ = createSession(t, hs, t.TempDir())
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("If-None-Match", etag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("changed list status = %d", res.StatusCode)
	}
	if res.Header.Get("ETag") == etag {
		t.Fatal("etag did not change after a new session")
	}
}

func TestSessionListSeesExternalConfigChange(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	res, err := authedGet(t, hs, "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("session dir")
	}

	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg session.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Title = "renamed externally"
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err = authedGet(t, hs, "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if len(listed) != 1 || listed[0]["title"] != "renamed externally" {
		t.Fatalf("list: %+v", listed)
	}
}
