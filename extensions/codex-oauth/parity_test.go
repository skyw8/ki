package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func parsed(t *testing.T, s string) object {
	t.Helper()
	var m object
	if err := decode(s, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("got %s\nwant %s", jsonString(got), jsonString(want))
	}
}
func normalize(v any) any {
	var parsed any
	_ = decode(jsonString(v), &parsed)
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if (k == "textSignature" || k == "thinkingSignature") && str(v) != "" {
					var signature any
					if decode(str(v), &signature) == nil {
						x[k] = walk(signature)
						continue
					}
				}
				x[k] = walk(v)
			}
		case []any:
			for i, v := range x {
				x[i] = walk(v)
			}
		}
		return v
	}
	return walk(parsed)
}
func wantError(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Fatalf("got error %v; want containing %q", err, substring)
	}
}

// These fixtures were captured from all 33 original Python tests before their
// removal. Complete replay bodies and emitted event snapshots preserve their
// assertions and catch differences the original tests did not inspect.
func TestOriginalBehaviorParity(t *testing.T) {
	// Reference snapshots retain the original shapes except the intentional
	// nonRetryable safety marker: a terminal provider failure must not be
	// resubmitted by the host's generic backoff loop.
	data, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []object
	if err = decode(string(data), &fixtures); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		fixture := fixture
		t.Run(fmt.Sprintf("%s/%02d", str(fixture["test"]), index), func(t *testing.T) {
			var got any
			var err error
			switch fixture["kind"] {
			case "request":
				got, err = buildRequest(obj(fixture["payload"]))
			case "compact":
				b := newCompactBuilder()
				for _, v := range list(fixture["objects"]) {
					o := obj(v)
					err = b.process(str(o["name"]), obj(o["obj"]))
					if err != nil {
						break
					}
				}
				if err == nil {
					got, err = b.result()
				}
			case "stream_http":
				events := []any{}
				server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for _, value := range list(fixture["objects"]) {
						event := obj(value)
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", str(event["name"]), jsonString(event["obj"]))
					}
				}))
				defer server.Close()
				payload := basicPayload(server.URL)
				payload["model"] = clone(fixture["model"])
				obj(payload["model"])["baseUrl"] = server.URL
				err = streamCodex(context.Background(), payload, func(e object) { events = append(events, e) }, str(fixture["requestId"]))
				got = events
				if err == nil {
					equalJSON(t, obj(obj(events[len(events)-1])["params"])["message"], fixture["message"])
				}
			default:
				t.Fatal("unknown fixture")
			}
			if expected := str(fixture["error"]); expected != "" {
				wantError(t, err, expected)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := fixture["result"]
			if fixture["kind"] == "stream_http" {
				expected = fixture["events"]
			}
			equalJSON(t, got, expected)
		})
	}
}
func jwtForAccount(account string) string {
	encode := func(v any) string { return base64.RawURLEncoding.EncodeToString([]byte(jsonString(v))) }
	return encode(object{"alg": "none"}) + "." + encode(object{"https://api.openai.com/auth": object{"chatgpt_account_id": account}}) + ".signature"
}
func TestAuthInputAndAccountID(t *testing.T) {
	for _, value := range []string{"abc#xyz", "http://localhost/callback?code=abc&state=xyz", "code=abc&state=xyz"} {
		code, state := parseAuthInput(value)
		if code != "abc" || state != "xyz" {
			t.Fatal(code, state)
		}
	}
	if accountID(jwtForAccount("acct")) != "acct" {
		t.Fatal("account id")
	}
	for _, value := range []string{"", "not-a-jwt", "a.%.c", "a.b.c"} {
		if accountID(value) != "" {
			t.Fatal(value)
		}
	}
}
func TestSlotRegistryPrefersItemIDOverReusedOutputIndex(t *testing.T) {
	slots := newSlots()
	first := slots.resolve(object{"output_index": 0}, "rs-1", "")
	second := slots.resolve(object{"output_index": 0}, "fc-1", "call-1")
	if first == second {
		t.Fatal("merged identities")
	}
	for _, tc := range []struct {
		o              object
		id, call, want string
	}{{object{"output_index": 0}, "", "call-1", second}, {object{"output_index": 0}, "", "", second}, {object{"output_index": 7}, "", "call-9", "call:call-9"}, {object{"output_index": 7}, "", "", "index:7"}, {object{}, "", "", "item:unknown"}} {
		if got := slots.resolve(tc.o, tc.id, tc.call); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
func TestBuildCompactRequestAppendsTriggerAfterOpaqueContext(t *testing.T) {
	checkpoint := object{"type": "compaction", "id": "cmp-old", "encrypted_content": "opaque-old", "future": object{"nested": []any{1, 2, 3}}}
	payload := object{"model": object{"id": "gpt-5.6", "input": []any{"text"}}, "request": object{"responsesContext": []any{checkpoint}, "messages": []any{object{"role": "user", "content": []any{object{"type": "text", "text": "continue"}}}}}}
	body, err := buildCompactRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := list(body["input"])
	equalJSON(t, input[0], checkpoint)
	equalJSON(t, input[2], object{"type": "compaction_trigger"})
	if obj(input[1])["type"] != "message" {
		t.Fatal(input)
	}
	obj(input[0])["id"] = "changed"
	if checkpoint["id"] != "cmp-old" {
		t.Fatal("mutated original")
	}
}
func TestRemoteCompactionV2RetainsRecentUserTurnsOnly(t *testing.T) {
	items := list(parsed(t, `{"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"function_call","call_id":"call-1"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reply"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}]}`)["items"])
	retained := retainedCompactionItems(items)
	equalJSON(t, retained, []any{items[0], items[3]})
	obj(retained[0])["future"] = true
	if obj(items[0])["future"] != nil {
		t.Fatal("mutated original")
	}
	huge := object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": strings.Repeat("x", 300000)}}}
	if got := retainedCompactionItems([]any{items[0], huge, items[3]}); len(got) != 1 {
		t.Fatal("budget crossed", len(got))
	}
	if pythonJSONLength(object{"text": "你好😀"}) != 35 {
		t.Fatal("unicode retention estimate", pythonJSONLength(object{"text": "你好😀"}))
	}
}
func TestManifestAdvertisesCodexV2Compaction(t *testing.T) {
	provider := providerSpec()
	if len(list(provider["models"])) == 0 {
		t.Fatal("no models")
	}
	for _, v := range list(provider["models"]) {
		equalJSON(t, obj(v)["compaction"], object{"standalone": "codex-v2"})
		equalJSON(t, obj(v)["execToolType"], "freeform")
		equalJSON(t, obj(v)["applyPatchToolType"], "freeform")
	}
	var manifest object
	_ = decode(string(manifestBytes), &manifest)
	equalJSON(t, manifest["runtime"], object{"kind": "rpc", "command": "bin/codex-oauth", "args": []any{}, "install": []any{"go", "run", "./install/main.go"}, "installWhen": "missing"})
}

func TestManifestPricingMatchesOpenAIStandardCatalog(t *testing.T) {
	// OAuth costs are API-equivalent estimates, not subscription charges.
	// Compare both catalogs so a core price update cannot leave Codex stale.
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "provider", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := parsed(t, string(data))
	costs := map[string]any{}
	for _, value := range list(catalog["providers"]) {
		p := obj(value)
		if str(p["id"]) == "openai" {
			for _, value := range list(p["models"]) {
				model := obj(value)
				costs[str(model["id"])] = model["cost"]
			}
		}
	}
	models := list(providerSpec()["models"])
	if len(models) == 0 {
		t.Fatal("no Codex models")
	}
	for _, value := range models {
		model := obj(value)
		t.Run(str(model["id"]), func(t *testing.T) {
			cost, ok := costs[str(model["id"])]
			if !ok || cost == nil || model["cost"] == nil {
				t.Fatal("missing OpenAI or Codex pricing")
			}
			equalJSON(t, model["cost"], cost)
		})
	}
}

func basicPayload(base string) object {
	return object{"provider": "openai-codex", "model": object{"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": base, "input": []any{"text"}}, "credential": object{"type": "oauth", "value": object{"access": "access", "accountId": "acct"}}, "request": object{"sessionId": "session-1", "messages": []any{object{"role": "user", "content": []any{object{"type": "text", "text": "hi"}}}}}}
}
func sse(w http.ResponseWriter, events ...object) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonString(event))
	}
}
func TestResponsesRequestAdvertisesCodexCompactionBetaFeature(t *testing.T) {
	for _, compaction := range []bool{false, true} {
		t.Run(fmt.Sprint(compaction), func(t *testing.T) {
			seen := make(chan struct {
				headers http.Header
				body    object
				path    string
			}, 1)
			server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var body object
				_ = decode(string(raw), &body)
				seen <- struct {
					headers http.Header
					body    object
					path    string
				}{r.Header.Clone(), body, r.URL.Path}
				output := []any{}
				if compaction {
					output = append(output, object{"type": "compaction", "id": "cmp-new", "encrypted_content": "opaque-new", "future_field": object{"keep": []any{"all", "fields"}}})
				}
				for _, v := range output {
					sse(w, object{"type": "response.output_item.done", "item": v})
				}
				sse(w, object{"type": "response.completed", "response": object{"id": "resp-compact", "status": "completed", "output": []any{}, "usage": object{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25, "input_tokens_details": object{"cached_tokens": 7, "cache_write_tokens": 3}}}})
			}))
			defer server.Close()
			payload := basicPayload(server.URL)
			if compaction {
				obj(payload["request"])["responsesContext"] = []any{object{"type": "compaction", "encrypted_content": "opaque-old", "unknown": true}}
			}
			var result object
			var err error
			if compaction {
				result, err = compactCodex(context.Background(), payload)
			} else {
				err = streamCodex(context.Background(), payload, func(object) {}, "stream-1")
			}
			if err != nil {
				t.Fatal(err)
			}
			request := <-seen
			if request.path != "/codex/responses" {
				t.Fatal(request.path)
			}
			headers, body := request.headers, request.body
			for k, want := range map[string]string{"Chatgpt-Account-Id": "acct", "X-Codex-Beta-Features": betaFeatures, "Originator": originator, "Session-Id": "session-1", "Thread-Id": "session-1", "X-Client-Request-Id": "session-1"} {
				if headers.Get(k) != want {
					t.Fatal(k, headers.Get(k))
				}
			}
			if !strings.HasPrefix(headers.Get("User-Agent"), "codex_cli_rs/") {
				t.Fatal(headers)
			}
			metadata := parsed(t, headers.Get("X-Codex-Turn-Metadata"))
			kind := "turn"
			if compaction {
				kind = "compaction"
			}
			if metadata["request_kind"] != kind {
				t.Fatal(metadata)
			}
			if compaction {
				descriptor := obj(metadata["compaction"])
				if descriptor["implementation"] != "responses_compaction_v2" || descriptor["phase"] != "standalone_turn" {
					t.Fatal(descriptor)
				}
				input := list(body["input"])
				equalJSON(t, input[0], obj(payload["request"])["responsesContext"].([]any)[0])
				equalJSON(t, input[len(input)-1], object{"type": "compaction_trigger"})
				equalJSON(t, result["items"], []any{obj(body["input"].([]any)[1]), object{"type": "compaction", "id": "cmp-new", "encrypted_content": "opaque-new", "future_field": object{"keep": []any{"all", "fields"}}}})
				equalJSON(t, result["usage"], object{"input": 10, "output": 5, "cacheRead": 7, "cacheWrite": 3, "totalTokens": 25})
			} else if metadata["compaction"] != nil {
				t.Fatal(metadata)
			}
			client := obj(body["client_metadata"])
			if metadata["window_id"] != headers.Get("X-Codex-Window-Id") || client["x-codex-window-id"] != metadata["window_id"] || client["x-codex-turn-metadata"] != headers.Get("X-Codex-Turn-Metadata") || client["session_id"] != "session-1" || client["turn_id"] != metadata["turn_id"] || metadata["root_turn_id"] != metadata["turn_id"] || str(client["x-codex-installation-id"]) == "" {
				t.Fatal(metadata, client)
			}
		})
	}
}
func TestHTTPErrorIncludesUpstreamResponseBody(t *testing.T) {
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: max_output_tokens"}}`))
	}))
	defer server.Close()
	err := streamCodex(context.Background(), basicPayload(server.URL), func(object) {}, "stream-1")
	var upstream *httpFailure
	if !errors.As(err, &upstream) || upstream.status != 400 {
		t.Fatal(err)
	}
	wantError(t, err, "unsupported parameter")
}
func TestStreamSSE(t *testing.T) {
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, object{"type": "response.created", "response": object{"id": "resp-1"}}, object{"type": "response.output_item.added", "item": object{"type": "message", "id": "msg-1"}}, object{"type": "response.output_text.delta", "item_id": "msg-1", "delta": "hello"}, object{"type": "response.completed", "response": object{"status": "completed"}})
	}))
	defer server.Close()
	events := []any{}
	if err := streamCodex(context.Background(), basicPayload(server.URL), func(e object) { events = append(events, e) }, "stream-1"); err != nil {
		t.Fatal(err)
	}
	types := []any{}
	for _, v := range events {
		types = append(types, obj(obj(v)["params"])["type"])
	}
	equalJSON(t, types, []any{"start", "text_start", "text_delta", "done"})
	if obj(obj(obj(events[len(events)-1])["params"])["message"])["responseId"] != "resp-1" {
		t.Fatal(events)
	}
}
func TestStalledResponseHeadersFailWithinHeaderBudget(t *testing.T) {
	old := streamHeaderTimeout
	streamHeaderTimeout = 100 * time.Millisecond
	defer func() { streamHeaderTimeout = old }()
	release := make(chan struct{})
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	started := time.Now()
	err := streamCodex(context.Background(), basicPayload(server.URL), func(object) {}, "stall")
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("header timeout too long")
	}
}
func TestIdleStreamSurvivesPastHeaderBudget(t *testing.T) {
	oldHeader, oldIdle := streamHeaderTimeout, streamIdleTimeout
	streamHeaderTimeout = 50 * time.Millisecond
	streamIdleTimeout = time.Second
	defer func() { streamHeaderTimeout, streamIdleTimeout = oldHeader, oldIdle }()
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		sse(w, object{"type": "response.created", "response": object{"id": "resp-idle"}}, object{"type": "response.output_text.delta", "item_id": "msg-1", "delta": "hi"}, object{"type": "response.completed", "response": object{"status": "completed"}})
	}))
	defer server.Close()
	var final object
	err := streamCodex(context.Background(), basicPayload(server.URL), func(e object) { final = obj(e["params"]) }, "idle")
	if err != nil {
		t.Fatal(err)
	}
	if final["type"] != "done" || obj(final["message"])["responseId"] != "resp-idle" {
		t.Fatal(final)
	}
}
func TestCancellationClosesOnlyBoundResponse(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	finishUnrelated := make(chan struct{})
	responseClosed := make(chan struct{})
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		if r.Header.Get("Session-Id") == "cancel" {
			select {
			case <-r.Context().Done():
				close(responseClosed)
			case <-release:
			}
			return
		}
		select {
		case <-finishUnrelated:
		case <-release:
			return
		}
		sse(w, object{"type": "response.completed", "response": object{"status": "completed", "output": []any{object{"type": "compaction", "encrypted_content": "opaque"}}}})
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := basicPayload(server.URL)
	obj(first["request"])["sessionId"] = "cancel"
	cancelled := make(chan error, 1)
	go func() { _, err := compactCodex(ctx, first); cancelled <- err }()
	<-entered
	unrelated := make(chan error, 1)
	go func() { _, err := compactCodex(context.Background(), basicPayload(server.URL)); unrelated <- err }()
	<-entered
	cancel()
	select {
	case err := <-cancelled:
		wantError(t, err, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation cause", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked cancellation")
	}
	select {
	case <-responseClosed:
	case <-time.After(time.Second):
		t.Fatal("bound response remained open")
	}
	close(finishUnrelated)
	if err := <-unrelated; err != nil {
		t.Fatal("cancelled unrelated request", err)
	}
	_, err := compactCodex(ctx, object{})
	wantError(t, err, "cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation cause before request", err)
	}
}
func TestCompactionCancellationBeforeResponseHeaders(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		// Server-side Flush is not a client-side response fence. Withhold all
		// headers to deterministically cancel while Client.Do is still pending.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := compactCodex(ctx, basicPayload(server.URL)); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		wantError(t, err, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation cause before headers", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked cancellation before headers")
	}
}
func TestProviderCompactRPCReturnsResultAndPreservesRefreshShape(t *testing.T) {
	s := newSidecar(io.Discard)
	defer s.shutdown()
	sent := make(chan object, 4)
	s.send = func(v object) { sent <- v }
	expected := object{"items": []any{object{"type": "compaction", "encrypted_content": "opaque"}}, "usage": object{"input": 1, "output": 2, "totalTokens": 3}}
	s.compact = func(context.Context, object) (object, error) { return expected, nil }
	s.handle(object{"jsonrpc": "2.0", "id": "compact-1", "method": "provider.compact", "params": object{"provider": "openai-codex"}})
	equalJSON(t, <-sent, object{"jsonrpc": "2.0", "id": "compact-1", "result": expected})
	future := object{"type": "oauth", "value": object{"access": "access", "refresh": "refresh", "expires": nowMS() + refreshWindow + 60000, "accountId": "acct"}}
	s.handle(object{"id": "refresh-1", "method": "provider.auth.refresh", "params": object{"credential": future}})
	equalJSON(t, <-sent, object{"jsonrpc": "2.0", "id": "refresh-1", "result": object{}})
	token := object{"access_token": jwtForAccount("acct"), "refresh_token": "new-refresh", "expires_in": 3600}
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "old-refresh" {
			t.Error(r.Form)
		}
		_, _ = w.Write([]byte(jsonString(token)))
	}))
	defer server.Close()
	t.Setenv("KI_CODEX_AUTH_BASE_URL", server.URL)
	s.handle(object{"id": "refresh-2", "method": "provider.auth.refresh", "params": object{"credential": object{"type": "oauth", "value": object{"refresh": "old-refresh", "expires": nowMS() - 1}}}})
	refreshed := obj((<-sent)["result"])
	credential := obj(refreshed["credential"])
	if refreshed["refreshed"] != true || credential["type"] != "oauth" || obj(credential["value"])["refresh"] != "new-refresh" || obj(credential["value"])["accountId"] != "acct" {
		t.Fatal(refreshed)
	}
}
func TestProviderCompactRPCPreservesHTTPStatusAndBody(t *testing.T) {
	s := newSidecar(io.Discard)
	defer s.shutdown()
	sent := make(chan object, 1)
	s.send = func(v object) { sent <- v }
	s.compact = func(context.Context, object) (object, error) {
		return nil, &httpFailure{422, `HTTP Error 422: Unprocessable Entity: {"error":{"message":"compaction unavailable"}}`}
	}
	s.handle(object{"id": "compact-error", "method": "provider.compact", "params": object{"provider": "openai-codex"}})
	e := obj((<-sent)["error"])
	if integer(e["code"]) != 422 || !strings.Contains(str(e["message"]), "compaction unavailable") {
		t.Fatal(e)
	}
}
func TestAuthBrowserAndManualFlow(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			t.Setenv("KI_CODEX_CALLBACK_PORT", fmt.Sprint(port))
			tokenSeen := make(chan url.Values, 1)
			server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				tokenSeen <- r.Form
				_, _ = w.Write([]byte(jsonString(object{"access_token": jwtForAccount("acct"), "refresh_token": "refresh", "expires_in": 3600})))
			}))
			defer server.Close()
			t.Setenv("KI_CODEX_AUTH_BASE_URL", server.URL)
			events := make(chan object, 4)
			session := newOAuthSession(context.Background(), object{"requestId": "auth-1"}, func(e object) { events <- obj(e["params"]) })
			defer session.close()
			done := make(chan struct{})
			go func() { session.run(); close(done) }()
			event := <-events
			if event["type"] != "auth_url" {
				t.Fatal(event)
			}
			u, _ := url.Parse(str(event["url"]))
			q := u.Query()
			if q.Get("client_id") != clientID || q.Get("code_challenge_method") != "S256" || q.Get("originator") != originator {
				t.Fatal(q)
			}
			redirect := fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=authorization&state=%s", port, q.Get("state"))
			if manual {
				session.manual <- redirect
			} else {
				bad, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=bad&state=bad", port))
				if err != nil {
					t.Fatal(err)
				}
				bad.Body.Close()
				if bad.StatusCode != 400 {
					t.Fatal(bad.StatusCode)
				}
				response, err := http.Get(redirect)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatal(response.StatusCode)
				}
			}
			completed := <-events
			if completed["type"] != "completed" || obj(obj(completed["credential"])["value"])["accountId"] != "acct" {
				t.Fatal(completed)
			}
			form := <-tokenSeen
			if form.Get("code") != "authorization" || form.Get("grant_type") != "authorization_code" || form.Get("redirect_uri") != redirectURI() || form.Get("code_verifier") == "" {
				t.Fatal(form)
			}
			<-done
		})
	}
}
func TestDeviceAuthorizationPendingAndSuccess(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	server := newHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = w.Write([]byte(`{"device_auth_id":"device-1","user_code":"code-1","interval":"0.01"}`))
		case "/api/accounts/deviceauth/token":
			mu.Lock()
			polls++
			poll := polls
			mu.Unlock()
			if poll == 1 {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"authorization_code":"authorized","code_verifier":"verifier"}`))
		case "/oauth/token":
			r.ParseForm()
			if r.Form.Get("redirect_uri") != authBase()+"/deviceauth/callback" || r.Form.Get("code_verifier") != "verifier" {
				t.Error(r.Form)
			}
			_, _ = w.Write([]byte(jsonString(object{"access_token": jwtForAccount("acct"), "refresh_token": "refresh", "expires_in": 3600})))
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("KI_CODEX_AUTH_BASE_URL", server.URL)
	events := make(chan object, 4)
	s := newOAuthSession(context.Background(), object{"requestId": "device", "mode": "device_code"}, func(e object) { events <- obj(e["params"]) })
	s.run()
	first, second := <-events, <-events
	if first["type"] != "device_code" || first["userCode"] != "code-1" || first["verificationUri"] != server.URL+"/codex/device" || integer(first["expiresInSeconds"]) != 900 || second["type"] != "completed" {
		t.Fatal(first, second)
	}
}
func TestOpaqueAndSSEValidation(t *testing.T) {
	for _, raw := range []string{"[1]", "null", "{broken"} {
		b := newCompactBuilder()
		_, _ = b.feed("data: " + raw)
		_, err := b.feed("")
		if err == nil {
			t.Fatal(raw)
		}
	}
	b := newCompactBuilder()
	_, err := b.feed("data: " + strings.Repeat("x", 64*1024*1024))
	wantError(t, err, "too large")
	for _, output := range []any{"bad", []any{"bad"}, nil} {
		b := newCompactBuilder()
		err := b.process("", object{"type": "response.completed", "response": object{"output": output}})
		wantError(t, err, "terminal output is invalid")
	}
	for _, arguments := range []string{"{broken", "[]", "null"} {
		_, err := parseArguments(arguments)
		if err == nil {
			t.Fatal(arguments)
		}
	}
}
func TestRPCCompactionCancellationAndDuplicate(t *testing.T) {
	s := newSidecar(io.Discard)
	defer s.shutdown()
	sent := make(chan object, 4)
	s.send = func(v object) { sent <- v }
	entered := make(chan struct{})
	s.compact = func(ctx context.Context, p object) (object, error) {
		close(entered)
		<-ctx.Done()
		return nil, errors.New("Codex compaction was cancelled")
	}
	request := object{"id": "compact-1", "method": "provider.compact", "params": object{"provider": "openai-codex"}}
	s.handle(request)
	<-entered
	s.handle(request)
	duplicate := <-sent
	if integer(obj(duplicate["error"])["code"]) != -32600 {
		t.Fatal(duplicate)
	}
	s.handle(object{"method": "cancel", "params": object{"id": "compact-1"}})
	select {
	case cancelled := <-sent:
		if integer(obj(cancelled["error"])["code"]) != -32800 {
			t.Fatal(cancelled)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
}
