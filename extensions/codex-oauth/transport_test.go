package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"ki/pkg/codexclient"
)

func transportFixture(t *testing.T, server string) object {
	t.Helper()
	payload := basicPayload(server)
	obj(payload["request"])["sessionId"] = t.Name()
	obj(payload["request"])["turnId"] = "turn-1"
	t.Cleanup(closeCodexTransports)
	return payload
}

func wsBody(input ...any) object {
	return object{"model": "gpt-6-astra", "stream": true, "store": false, "instructions": "test",
		"input": input, "tool_choice": "auto", "parallel_tool_calls": true,
		"include": []any{"reasoning.encrypted_content"}, "reasoning": object{"effort": "high"}}
}

func wsComplete(conn *websocket.Conn, id string, output []any) error {
	return conn.WriteJSON(object{"type": "response.completed", "response": object{"id": id, "status": "completed", "output": output}})
}

func TestWebSocketReuseIncrementalAndRouting(t *testing.T) {
	var handshakes atomic.Int32
	seen := make(chan object, 3)
	headers := make(chan http.Header, 2)
	user := object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": "hello"}}}
	answer := object{"type": "message", "role": "assistant", "id": "msg-1", "content": []any{object{"type": "output_text", "text": "answer", "annotations": []any{}}}}
	next := object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": "again"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{"X-Codex-Turn-State": []string{"sticky"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		handshakes.Add(1)
		headers <- r.Header.Clone()
		for i := 0; ; i++ {
			var request object
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			seen <- request
			if err := wsComplete(conn, fmt.Sprintf("resp-%d", i+1), []any{answer}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	body := wsBody(user)
	body["service_tier"] = "priority"
	for _, input := range [][]any{{user}, {user, answer, next}} {
		body["input"] = input
		used, err := streamWebsocket(context.Background(), payload, body, func(object) {}, "request")
		if !used || err != nil {
			t.Fatal(used, err)
		}
	}
	first, second := <-seen, <-seen
	if first["type"] != "response.create" || first["previous_response_id"] != nil {
		t.Fatal(first)
	}
	if second["previous_response_id"] != "resp-1" {
		t.Fatal(second)
	}
	equalJSON(t, second["input"], []any{next})
	if obj(second["client_metadata"])["x-codex-turn-state"] != "sticky" {
		t.Fatal(second)
	}
	h := <-headers
	if h.Get("OpenAI-Beta") != codexclient.WebSocketBeta || h.Get("X-Codex-Routing-Hint") != "model=gpt-6-astra;tier=priority" || handshakes.Load() != 1 {
		t.Fatal(h, handshakes.Load())
	}
	// A tier change requires a new handshake, never incremental reuse on a
	// socket that was originally routed to a different service tier.
	delete(body, "service_tier")
	used, err := streamWebsocket(context.Background(), payload, body, func(object) {}, "request-3")
	if !used || err != nil {
		t.Fatal(used, err)
	}
	third := <-seen
	if third["previous_response_id"] != nil || handshakes.Load() != 2 {
		t.Fatal(third, handshakes.Load())
	}
}

func TestWebSocketPreviousResponseRejectionRetriesFull(t *testing.T) {
	seen := make(chan object, 3)
	var connections atomic.Int32
	item := object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": "hello"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		connections.Add(1)
		for {
			var request object
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			seen <- request
			if request["previous_response_id"] != nil {
				_ = conn.WriteJSON(object{"type": "error", "error": object{"code": "previous_response_not_found", "message": "missing"}})
				return
			}
			if err := wsComplete(conn, "resp-full", nil); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	for range 2 {
		used, err := streamWebsocket(context.Background(), payload, wsBody(item), func(object) {}, "request")
		if !used || err != nil {
			t.Fatal(used, err)
		}
	}
	first, rejected, full := <-seen, <-seen, <-seen
	if first["previous_response_id"] != nil || rejected["previous_response_id"] != "resp-full" || full["previous_response_id"] != nil || connections.Load() != 2 {
		t.Fatal(first, rejected, full, connections.Load())
	}
	equalJSON(t, full["input"], []any{item})
}

func TestWebSocketHandshakeAPIErrorIsNotFallback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid model"}}`))
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	err := streamCodex(context.Background(), payload, func(object) {}, "request")
	var upstream *httpFailure
	if !errors.As(err, &upstream) || upstream.status != 400 || !strings.Contains(err.Error(), "invalid model") || requests.Load() != 1 {
		t.Fatal(err, requests.Load())
	}
}

func TestWebSocketPartialResponseNeverFallsBack(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			posts.Add(1)
			http.Error(w, "must not replay", 500)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var request object
		if err := conn.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		_ = conn.WriteJSON(object{"type": "response.created", "response": object{"id": "resp"}})
		_ = conn.WriteJSON(object{"type": "response.output_item.added", "item": object{"type": "message", "id": "msg"}})
		_ = conn.WriteJSON(object{"type": "response.output_text.delta", "item_id": "msg", "delta": "partial"})
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	var deltas int
	err := streamCodex(context.Background(), payload, func(event object) {
		if obj(event["params"])["type"] == "text_delta" {
			deltas++
		}
	}, "request")
	if err == nil || deltas != 1 || posts.Load() != 0 {
		t.Fatal(err, deltas, posts.Load())
	}
	var marker interface{ NonRetryable() bool }
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatal("host can retry a partially delivered response", err)
	}
}

func TestHTTPPartialResponseIsNonRetryable(t *testing.T) {
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, object{"type": "response.created", "response": object{"id": "started"}})
	}))
	defer server.Close()
	err := streamCodex(context.Background(), transportFixture(t, server.URL), func(object) {}, "request")
	var marker interface{ NonRetryable() bool }
	if !errors.As(err, &marker) || !marker.NonRetryable() {
		t.Fatal("host can retry a started HTTP response", err)
	}
}

func TestWebSocketTerminalErrorEventIsNonRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var request object
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		_ = conn.WriteJSON(object{"type": "error", "error": object{"message": "inference failed"}})
	}))
	defer server.Close()
	var event object
	err := streamCodex(context.Background(), transportFixture(t, server.URL), func(value object) {
		if obj(value["params"])["type"] == "error" {
			event = obj(value["params"])
		}
	}, "request")
	if err != nil || event["nonRetryable"] != true {
		t.Fatal(err, event)
	}
}

func TestWebSocketCancellationIsolatedAndInvalidatesConnection(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var request object
		if err := conn.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		if r.Header.Get("Thread-Id") == t.Name()+"-cancel" {
			_ = conn.WriteJSON(object{"type": "response.created", "response": object{"id": "waiting"}})
			close(started)
			_, _, _ = conn.ReadMessage()
			close(closed)
			return
		}
		_ = wsComplete(conn, "independent", nil)
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	obj(payload["request"])["sessionId"] = t.Name() + "-cancel"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- streamCodex(ctx, payload, func(object) {}, "cancel") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket did not start")
	}
	other := basicPayload(server.URL)
	obj(other["request"])["sessionId"] = t.Name() + "-other"
	if err := streamCodex(context.Background(), other, func(object) {}, "other"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock the reader")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled connection not closed")
	}
	sockets.Lock()
	defer sockets.Unlock()
	for key, socket := range sockets.values {
		if strings.HasPrefix(key, t.Name()+"-cancel\x00") && socket.connection.Load() != nil {
			t.Fatal("canceled socket retained")
		}
	}
}

func TestHTTPFallbackTurnStateScopeAndCheckpoint(t *testing.T) {
	seen := make(chan http.Header, 5)
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("X-Codex-Turn-State", "sticky-http")
		sse(w, object{"type": "response.completed", "response": object{"status": "completed"}})
	}))
	defer server.Close()
	payload := transportFixture(t, server.URL)
	for range 2 {
		if err := streamCodex(context.Background(), payload, func(object) {}, "request"); err != nil {
			t.Fatal(err)
		}
	}
	first, continuation := <-seen, <-seen
	if first.Get("X-Codex-Turn-State") != "" || continuation.Get("X-Codex-Turn-State") != "sticky-http" {
		t.Fatal(first, continuation)
	}
	// A credential change during the same logical turn must not send a token
	// issued to the previous backend/account binding.
	obj(obj(payload["credential"])["value"])["access"] = "different-access"
	if err := streamCodex(context.Background(), payload, func(object) {}, "new-auth"); err != nil {
		t.Fatal(err)
	}
	newAuth := <-seen
	if newAuth.Get("X-Codex-Turn-State") != "" || newAuth.Get("X-Codex-Window-Id") == first.Get("X-Codex-Window-Id") {
		t.Fatal(newAuth)
	}
	obj(payload["request"])["turnId"] = "turn-2"
	if err := streamCodex(context.Background(), payload, func(object) {}, "new-turn"); err != nil {
		t.Fatal(err)
	}
	newTurn := <-seen
	if newTurn.Get("X-Codex-Turn-State") != "" {
		t.Fatal(newTurn)
	}
	codexclient.AdvanceWindow(t.Name())
	if err := streamCodex(context.Background(), payload, func(object) {}, "new-window"); err != nil {
		t.Fatal(err)
	}
	newWindow := <-seen
	if newWindow.Get("X-Codex-Window-Id") == newTurn.Get("X-Codex-Window-Id") || newWindow.Get("X-Codex-Turn-State") != "" {
		t.Fatal(newWindow)
	}
	if first.Get("OpenAI-Beta") != "" || first.Get("Content-Encoding") != "zstd" {
		t.Fatal(first)
	}
}

func TestWebSocketPoolAdmissionAndBaselineBounds(t *testing.T) {
	closeCodexTransports()
	t.Cleanup(closeCodexTransports)
	releases := make([]func(), 0, websocketPoolLimit)
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for index := range websocketPoolLimit {
		_, release, err := acquireSocket(context.Background(), fmt.Sprintf("key-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, _, err := acquireSocket(context.Background(), "overflow"); !errors.Is(err, errSocketCapacity) {
		t.Fatal(err)
	}
	if len(sockets.values) != websocketPoolLimit {
		t.Fatal("pool grew past its bound")
	}
	var httpRequests atomic.Int32
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests.Add(1)
		sse(w, object{"type": "response.completed", "response": object{"status": "completed"}})
	}))
	defer server.Close()
	if err := streamCodex(context.Background(), transportFixture(t, server.URL), func(object) {}, "overflow-http"); err != nil || httpRequests.Load() != 1 {
		t.Fatal("capacity did not safely fall back to HTTP", err, httpRequests.Load())
	}
	if len(sockets.values) != websocketPoolLimit {
		t.Fatal("fallback created an overflow socket")
	}
	socket := &codexSocket{}
	socket.retainBaseline(wsBody(object{"text": "small"}), nil, "response")
	if socket.baselineSize == 0 {
		t.Fatal("small baseline not retained")
	}
	socket.retainBaseline(wsBody(object{"text": strings.Repeat("x", websocketBaselineLimit)}), nil, "large")
	if socket.baselineSize != 0 || socket.prefix != nil {
		t.Fatal("oversized baseline retained")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 20 {
				socket.retainBaseline(wsBody(object{"text": "small"}), nil, "response")
				_, _ = socket.continuation(wsBody(object{"text": "small"}))
				socket.close()
			}
		})
	}
	workers.Wait()
	socket.close()
	if socket.prefix != nil || socket.baselineSize != 0 {
		t.Fatal("closed socket retains history")
	}
	baselineBudget.Lock()
	defer baselineBudget.Unlock()
	if baselineBudget.bytes < 0 || baselineBudget.bytes > websocketBaselineBudget {
		t.Fatal(baselineBudget.bytes)
	}
}
