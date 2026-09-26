package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ki/internal/provider"
	"ki/internal/server"
)

// promptJSON posts a prompt and also returns the raw body, so a failure shows
// what the server answered instead of the empty map serveJSON falls back to.
func promptJSON(t *testing.T, sf server.File, id string, body map[string]any) (int, map[string]any, string) {
	t.Helper()
	status, raw, err := serveRaw(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", body)
	if err != nil {
		t.Fatalf("prompt request: %v", err)
	}
	out := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return status, out, raw
}

func TestBusyQueuePromoteHTTP(t *testing.T) {
	home, proj := isolate(t)
	sf := startServe(t, home)

	status, created := serveJSON(t, sf, http.MethodPost, "/v1/sessions", map[string]any{"cwd": proj})
	if status != http.StatusOK {
		t.Fatalf("create %d %+v", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("no session id")
	}
	status, _ = serveJSON(t, sf, http.MethodPatch, "/v1/message", map[string]any{"busy": "queue"})
	if status != http.StatusOK {
		t.Fatalf("message toggle %d", status)
	}

	status, out, raw := promptJSON(t, sf, id, map[string]any{"text": provider.HoldToken})
	if status != http.StatusAccepted || out["accepted"] != "started" {
		t.Fatalf("hold %d %+v body=%q", status, out, raw)
	}
	waitSessionRunning(t, sf, id, true)

	status, out, raw = promptJSON(t, sf, id, map[string]any{"text": "queued-keep"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("keep %d %+v body=%q", status, out, raw)
	}
	status, out, raw = promptJSON(t, sf, id, map[string]any{"text": "queued-promote"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("promote enqueue %d %+v body=%q", status, out, raw)
	}
	status, detail := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
	queued, _ := detail["queued"].([]any)
	if status != http.StatusOK || len(queued) != 2 {
		rawStatus, raw, rawErr := serveRaw(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
		t.Fatalf("detail %d queued=%+v body=%+v retry=%d err=%v body=%.400q", status, detail["queued"], detail, rawStatus, rawErr, raw)
	}
	tail, _ := queued[1].(map[string]any)
	tailID, _ := tail["id"].(string)
	status, out, raw = promptJSON(t, sf, id, map[string]any{"delivery": "steer", "queueId": tailID})
	if status != http.StatusAccepted || out["accepted"] != "steered" {
		t.Fatalf("promote %d %+v body=%q", status, out, raw)
	}
	_, detail = serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
	queued, _ = detail["queued"].([]any)
	if len(queued) != 1 {
		t.Fatalf("after promote queued = %+v", detail["queued"])
	}
	keep, _ := queued[0].(map[string]any)
	if keep["id"] == tailID {
		t.Fatal("promoted tail still queued")
	}

	status, _ = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/abort", nil)
	if status != http.StatusOK {
		t.Fatalf("abort %d", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, detail = serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
		queued, _ = detail["queued"].([]any)
		raw, err := json.Marshal(detail["entries"])
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) == 0 && strings.Contains(string(raw), "queued-keep") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("keep did not dispatch: queued=%+v entries=%s", queued, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
