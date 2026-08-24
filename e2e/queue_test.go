package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ki/internal/provider"
)

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

	status, out := serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": provider.HoldToken})
	if status != http.StatusAccepted || out["accepted"] != "started" {
		t.Fatalf("hold %d %+v", status, out)
	}
	waitSessionRunning(t, sf, id, true)

	status, out = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "queued-keep"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("keep %d %+v", status, out)
	}
	status, out = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"text": "queued-promote"})
	if status != http.StatusAccepted || out["accepted"] != "queued" {
		t.Fatalf("promote enqueue %d %+v", status, out)
	}
	_, detail := serveJSON(t, sf, http.MethodGet, "/v1/sessions/"+id, nil)
	queued, _ := detail["queued"].([]any)
	if len(queued) != 2 {
		t.Fatalf("queued = %+v", detail["queued"])
	}
	tail, _ := queued[1].(map[string]any)
	tailID, _ := tail["id"].(string)
	status, out = serveJSON(t, sf, http.MethodPost, "/v1/sessions/"+id+"/prompt", map[string]any{"delivery": "steer", "queueId": tailID})
	if status != http.StatusAccepted || out["accepted"] != "steered" {
		t.Fatalf("promote %d %+v", status, out)
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
		raw, _ := json.Marshal(detail["messages"])
		if len(queued) == 0 && strings.Contains(string(raw), "queued-keep") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("keep did not dispatch: queued=%+v messages=%s", queued, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
