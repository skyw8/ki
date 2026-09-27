package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"ki/internal/provider"
	"testing"
	"time"
)

// pushClient is the browser half of a subscription: the P-256 key and auth
// secret a PushManager.subscribe call would produce.
type pushClient struct {
	private *ecdh.PrivateKey
	auth    []byte
}

func newPushClient(t *testing.T) pushClient {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return pushClient{private: priv, auth: auth}
}

func (c pushClient) body(t *testing.T, endpoint string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(c.private.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(c.auth),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func registerPush(t *testing.T, hs *httptest.Server, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/push/subscriptions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestPushConfigExposesTheApplicationServerKey(t *testing.T) {
	_, hs := testServer(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/push/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var got struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("push config must be enabled by default")
	}
	if _, err := ecdh.P256().NewPublicKey(mustDecode(t, got.PublicKey)); err != nil {
		t.Fatalf("publicKey is not a P-256 point: %v", err)
	}
}

func TestPushSubscriptionValidation(t *testing.T) {
	_, hs := testServer(t)
	client := newPushClient(t)
	if got := registerPush(t, hs, client.body(t, "http://169.254.169.254/push")); got != http.StatusBadRequest {
		t.Fatalf("non-https endpoint accepted: %d", got)
	}
	if got := registerPush(t, hs, `{"endpoint":"https://push.example/x","keys":{"p256dh":"AAAA","auth":"AAAA"}}`); got != http.StatusBadRequest {
		t.Fatalf("bad keys accepted: %d", got)
	}
	if got := registerPush(t, hs, client.body(t, "https://push.example/x")); got != http.StatusNoContent {
		t.Fatalf("valid subscription rejected: %d", got)
	}
}

// A finished run reaches the browser's push service, which is the whole point
// of the feature: the page may already be frozen.
func TestRunCompletionSendsWebPush(t *testing.T) {
	hits := make(chan http.Header, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case hits <- r.Header.Clone():
		default:
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	srv, hs := testServer(t)
	client := newPushClient(t)
	if got := registerPush(t, hs, client.body(t, endpoint.URL)); got != http.StatusNoContent {
		t.Fatalf("register: %d", got)
	}

	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "notify me")
	waitRunEnd(t, srv, id)

	select {
	case header := <-hits:
		if got := header.Get("Content-Encoding"); got != "aes128gcm" {
			t.Fatalf("content-encoding: %q", got)
		}
		if got := header.Get("Authorization"); !strings.HasPrefix(got, "vapid t=") {
			t.Fatalf("authorization: %q", got)
		}
		if got := header.Get("TTL"); got == "" {
			t.Fatal("missing TTL header")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run completion did not send a web push")
	}
}

// Subagent sessions are an implementation detail of the parent run, so they
// never raise a push of their own.
func TestSubagentCompletionDoesNotPush(t *testing.T) {
	hits := make(chan struct{}, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case hits <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	srv, hs := testServer(t)
	client := newPushClient(t)
	if got := registerPush(t, hs, client.body(t, endpoint.URL)); got != http.StatusNoContent {
		t.Fatalf("register: %d", got)
	}

	parent := createSession(t, hs, t.TempDir())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+parent+"/fork", strings.NewReader(`{"forkMode":"tree"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var child struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if child.ID == "" {
		t.Fatal("fork returned no child id")
	}
	srv.notifyPushCompletion(child.ID)

	select {
	case <-hits:
		t.Fatal("subagent session pushed a notification")
	case <-time.After(300 * time.Millisecond):
	}
}

func mustDecode(t *testing.T, value string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(fmt.Errorf("decode %q: %w", value, err))
	}
	return b
}

// A user abort ends the run with a normal agent_end; the push fallback must not
// announce it as a finished run (the WebUI suppresses its own notification the
// same way).
func TestAbortedRunDoesNotPush(t *testing.T) {
	hits := make(chan struct{}, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case hits <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	gate := &gateStreamer{inner: &provider.Scripted{}}
	srv, hs := testServerWith(t, gate)
	client := newPushClient(t)
	if got := registerPush(t, hs, client.body(t, endpoint.URL)); got != http.StatusNoContent {
		t.Fatalf("register: %d", got)
	}

	id := createSession(t, hs, t.TempDir())
	gate.arm()
	prompt202(t, hs, id, "abort me")
	waitBuffered(t, srv, id, 6)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/abort", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("abort: %d", res.StatusCode)
	}
	gate.release()
	waitAgentEnd(t, hs, id)

	select {
	case <-hits:
		t.Fatal("aborted run pushed a completion")
	case <-time.After(400 * time.Millisecond):
	}
}
