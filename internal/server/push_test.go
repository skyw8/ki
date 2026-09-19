package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
)

// pushEvent is one decoded GET /v1/events frame. Sideband frames embed the
// loop event next to the session id; invalidate frames carry a scope.
type pushEvent struct {
	loop.Event
	SessionID string `json:"sessionId,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

// pushEvents subscribes to the WebUI push channel and yields decoded frames.
func pushEvents(t *testing.T, hs *httptest.Server, token string) <-chan pushEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan pushEvent, 64)
	go func() {
		defer close(out)
		defer func() { _ = res.Body.Close() }()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev pushEvent
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(cancel)
	return out
}

// waitPush blocks until a frame matches. It fails the test on timeout or a
// closed stream so a missing push cannot silently pass.
func waitPush(t *testing.T, events <-chan pushEvent, what string, match func(pushEvent) bool) pushEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("push stream closed before %s", what)
			}
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no push frame for %s", what)
		}
	}
}

func isInvalidate(scope string) func(pushEvent) bool {
	return func(ev pushEvent) bool { return ev.Type == "invalidate" && ev.Scope == scope }
}

// The subscribe handshake must arrive first: the client only refetches after
// it, so a change that happened while it was disconnected cannot slip through
// the gap between connecting and fetching.
func TestPushReadyHandshake(t *testing.T) {
	_, hs := testServer(t)
	events := pushEvents(t, hs, "tok")
	ready := waitPush(t, events, "ready", func(ev pushEvent) bool { return ev.Type == "ready" })
	if ready.SessionID != "" || ready.Scope != "" {
		t.Fatalf("ready frame carried payload: %+v", ready)
	}
}

func TestPushInvalidatesSessionsAndWorkspaces(t *testing.T) {
	_, hs := testServer(t)
	events := pushEvents(t, hs, "tok")
	waitPush(t, events, "ready", func(ev pushEvent) bool { return ev.Type == "ready" })

	id := createSession(t, hs, t.TempDir())
	waitPush(t, events, "sessions invalidate on create", isInvalidate(scopeSessions))

	// A run start/end must invalidate the sidebar: that is how a second client
	// turns the dot green or clears it without refreshing.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	waitPush(t, events, "sessions invalidate on occupy", isInvalidate(scopeSessions))
	waitPush(t, events, "agent_end tagged with the session", func(ev pushEvent) bool {
		return ev.Type == loop.AgentEnd && ev.SessionID == id
	})

	status, created := postJSON(t, hs, http.MethodPost, "/v1/workspaces", map[string]any{"path": t.TempDir()})
	if status != http.StatusCreated {
		t.Fatalf("create workspace: %d", status)
	}
	waitPush(t, events, "workspaces invalidate", isInvalidate(scopeWorkspaces))
	wsID, _ := created["id"].(string)

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodDelete, hs.URL+"/v1/workspaces/"+wsID, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete workspace: %d", res.StatusCode)
	}
	waitPush(t, events, "sessions invalidate on workspace delete", isInvalidate(scopeSessions))
}
