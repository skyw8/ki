package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ki/internal/session"
)

func TestExpiredReplayPrintsExactDurableRepliesOnSelectedBranch(t *testing.T) {
	large := strings.Repeat("answer 中🙂 ", 100)
	var methods []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"replay_unavailable"}`))
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("view") == "compact":
			_ = json.NewEncoder(w).Encode(map[string]any{"leafId": "a2", "running": false, "compactTurns": []session.CompactTurn{{ID: "u"}}})
		case q.Get("fields") == "index":
			_ = json.NewEncoder(w).Encode(map[string]any{"index": []session.IndexEntry{{ID: "older", Role: "assistant"}, {ID: "u", ParentID: "older", Role: "user"}, {ID: "a1", ParentID: "u", Role: "assistant"}, {ID: "sibling", ParentID: "u", Role: "assistant"}, {ID: "a2", ParentID: "a1", Role: "assistant"}}})
		case q.Get("entries") == "a1,a2":
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []session.Entry{{ID: "a1", Message: assistant("first")}, {ID: "a2", Message: assistant(large)}}})
		default:
			t.Errorf("unexpected recovery request %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer hs.Close()
	var err error
	out := captureStdout(t, func() { err = streamEvents(context.Background(), hs.URL, "token", "s") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "first"+large {
		t.Fatalf("recovery printed wrong/truncated branch: %q", out)
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatal("recovery resubmitted input")
		}
	}
}

func TestStreamEventsRejectsHTTPFailure(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) }))
	defer hs.Close()
	if err := streamEvents(context.Background(), hs.URL, "bad", "s"); err == nil {
		t.Fatal("HTTP failure became an empty success")
	}
}
