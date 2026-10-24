package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"testing"
	"time"

	"ki/internal/provider"
	"ki/internal/push"
	"ki/internal/session"
	"ki/internal/types"
)

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

func (c pushClient) decrypt(t *testing.T, encrypted []byte) map[string]string {
	t.Helper()
	if len(encrypted) < 86 || binary.BigEndian.Uint32(encrypted[16:20]) != 4096 || encrypted[20] != 65 {
		t.Fatalf("invalid encrypted push header: %x", encrypted)
	}
	public, err := ecdh.P256().NewPublicKey(encrypted[21:86])
	if err != nil {
		t.Fatal(err)
	}
	secret, err := c.private.ECDH(public)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, secret, c.auth)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), c.private.PublicKey().Bytes()...)
	info = append(info, public.Bytes()...)
	ikm, err := hkdf.Expand(sha256.New, prk, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk, err = hkdf.Extract(sha256.New, ikm, encrypted[:16])
	if err != nil {
		t.Fatal(err)
	}
	key, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	body, err := gcm.Open(nil, nonce, encrypted[86:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || body[len(body)-1] != 2 {
		t.Fatal("missing push record delimiter")
	}
	var p map[string]string
	if err := json.Unmarshal(body[:len(body)-1], &p); err != nil {
		t.Fatal(err)
	}
	return p
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

func TestQuestionRequestedSendsWebPushOnlyForNewMainQuestions(t *testing.T) {
	hits := make(chan []byte, 16)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read encrypted push: %v", err)
		}
		hits <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()
	srv, hs := testServer(t)
	client := newPushClient(t)
	if got := registerPush(t, hs, client.body(t, endpoint.URL)); got != http.StatusNoContent {
		t.Fatalf("register: %d", got)
	}
	id := createSession(t, hs, t.TempDir())
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if _, err := sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "Choose release scope"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask", Name: "request_user_input_async"}}}); err != nil {
		t.Fatal(err)
	}
	inputs := []session.QuestionInput{{Title: "Which scope?", Options: []string{"private option"}}, {Title: "Deploy now?"}}
	if err := srv.requestQuestions(sess, "missing-source", inputs); err == nil {
		t.Fatal("missing source was accepted")
	}
	for range 2 {
		if err := srv.requestQuestions(sess, "ask", inputs); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.requestQuestions(sess, "ask", []session.QuestionInput{{Title: "conflicting repeat"}}); err == nil {
		t.Fatal("conflicting repeat was accepted")
	}
	questions := sess.Questions()
	if len(questions) != 2 {
		t.Fatalf("questions: %+v", questions)
	}
	for _, action := range []string{"dismiss", "restore"} {
		q, _ := sess.Question(questions[0].QuestionItemID)
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(mustJSON(t, map[string]any{"questionItemId": q.QuestionItemID, "revision": q.Revision, "action": action})))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		srv.questionAction(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	q, _ := sess.Question(questions[0].QuestionItemID)
	q.Status, q.Answer, q.SubmissionID, q.Revision = "answered", "private answer", "submission", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	srv.publishQuestion(id, q)
	fresh, err := session.Open(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if err := srv.requestQuestions(fresh, "ask", inputs); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "renamed-ask", Name: "request_user_input_async"}}}); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("Release review"); err != nil {
		t.Fatal(err)
	}
	if err := srv.requestQuestions(sess, "renamed-ask", []session.QuestionInput{{Title: "Approve release?"}}); err != nil {
		t.Fatal(err)
	}
	questions = sess.Questions()
	for _, mode := range []string{"tree", "question_side"} {
		other, err := session.Create(srv.cfg.Sessions.Root, t.TempDir(), "test", "test", "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close() })
		if _, err := other.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask", Name: "request_user_input_async"}}}); err != nil {
			t.Fatal(err)
		}
		if mode == "tree" {
			other.Header.ForkMode = session.ForkModeTree
		} else {
			other.Config.Metadata = map[string]any{"purpose": "question_side"}
			if err := other.SaveConfig(); err != nil {
				t.Fatal(err)
			}
		}
		err = srv.requestQuestions(other, "ask", inputs)
		if mode == "question_side" && err == nil {
			t.Fatal("side conversation question was accepted")
		}
		if mode == "tree" && err != nil {
			t.Fatal(err)
		}
	}
	srv.push.Notify(push.Message{SessionID: "queue-barrier", Title: "barrier"})
	got := map[string]map[string]string{}
	for {
		select {
		case encrypted := <-hits:
			p := client.decrypt(t, encrypted)
			if p["sessionId"] == "queue-barrier" {
				if p["type"] != "run_complete" || len(got) != len(questions) {
					t.Fatalf("unexpected notifications before queue barrier: %+v", got)
				}
				return
			}
			questionID := p["questionItemId"]
			if len(p) != 5 || p["type"] != "question_requested" || p["sessionId"] != id || got[questionID] != nil {
				t.Fatalf("unexpected or duplicate question push: %+v", p)
			}
			q, ok := sess.Question(questionID)
			if !ok || p["body"] != q.Title {
				t.Fatalf("question push does not match durable question: %+v", p)
			}
			wantTitle := "Choose release scope"
			if q.CallID == "renamed-ask" {
				wantTitle = "Release review"
			}
			if p["title"] != wantTitle {
				t.Fatalf("question session title = %q, want %q", p["title"], wantTitle)
			}
			got[questionID] = p
		case <-time.After(5 * time.Second):
			t.Fatal("question push queue did not reach barrier")
		}
	}
}

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
