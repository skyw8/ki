package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"ki/internal/session"
	"ki/internal/types"
)

func TestSessionTraceAndInspectViews(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, filepath.Join(t.TempDir(), "work"))
	dir, err := srv.sessionDir(id)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendAssistant := func(input, read int) {
		t.Helper()
		if _, err := sess.AppendMessage(types.Message{
			Role: "assistant", Usage: &types.Usage{Input: input, CacheRead: read},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendAssistant(30_000, 0)
	appendAssistant(100, 29_800)
	appendAssistant(30_000, 0)
	_ = sess.Close()

	trace := sessionGETURL(t, hs, "/v1/sessions/"+id+"?view=trace&cacheMiss=true")
	rows, ok := trace["trace"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("trace: %+v", trace)
	}
	inspect := sessionGETURL(t, hs, "/v1/sessions/"+id+"?view=inspect")
	analysis, ok := inspect["analysis"].(map[string]any)
	if !ok || len(analysis["cacheMisses"].([]any)) != 1 {
		t.Fatalf("inspect: %+v", inspect)
	}
}

func TestSessionGETRejectsUnknownView(t *testing.T) {
	_, hs := testServer(t)
	id := createSession(t, hs, filepath.Join(t.TempDir(), "work"))
	res, err := authedGet(t, hs, "/v1/sessions/"+id+"?view=unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		var body any
		_ = json.NewDecoder(res.Body).Decode(&body)
		t.Fatalf("status = %d body=%v", res.StatusCode, body)
	}
}
