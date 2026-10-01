package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testClient is a browser-side subscription key pair: the P-256 key the browser
// generates in PushManager.subscribe plus its auth secret.
type testClient struct {
	private *ecdh.PrivateKey
	auth    []byte
}

func newTestClient(t *testing.T) testClient {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return testClient{private: priv, auth: auth}
}

func (c testClient) subscription(t *testing.T, endpoint string) Subscription {
	t.Helper()
	return Subscription{
		Endpoint: endpoint,
		Keys: SubscriptionKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(c.private.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(c.auth),
		},
	}
}

// decrypt is the receiver half of RFC 8291. The package's own RFC 8291
// correctness is proven by round-tripping through it rather than by comparing
// ciphertext to a fixture.
func (c testClient) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != 4096 {
		t.Fatalf("record size %d", rs)
	}
	idLen := int(body[20])
	keyID := body[21 : 21+idLen]
	ciphertext := body[21+idLen:]

	asPublic, err := ecdh.P256().NewPublicKey(keyID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := c.private.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, secret, c.auth)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), c.private.PublicKey().Bytes()...)
	info = append(info, keyID...)
	ikm, err := hkdf.Expand(sha256.New, prk, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk2, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Expand(sha256.New, prk2, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk2, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plaintext) == 0 || plaintext[len(plaintext)-1] != 0x02 {
		t.Fatalf("missing record delimiter: %q", plaintext)
	}
	return plaintext[:len(plaintext)-1]
}

func TestEncryptRoundTripsThroughTheClientKey(t *testing.T) {
	client := newTestClient(t)
	sub := client.subscription(t, "https://push.example/abc")
	body, err := encrypt([]byte(`{"type":"run_complete"}`), sub.Keys.P256DH, sub.Keys.Auth)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(client.decrypt(t, body)); got != `{"type":"run_complete"}` {
		t.Fatalf("plaintext: %q", got)
	}

	// A second message must not reuse the ephemeral key or salt.
	again, err := encrypt([]byte(`{"type":"run_complete"}`), sub.Keys.P256DH, sub.Keys.Auth)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) == string(body) {
		t.Fatal("encryption reused the ephemeral key and salt")
	}
}

func TestEncryptRejectsOversizePayload(t *testing.T) {
	client := newTestClient(t)
	sub := client.subscription(t, "https://push.example/abc")
	if _, err := encrypt(make([]byte, payloadLimit+1), sub.Keys.P256DH, sub.Keys.Auth); err == nil {
		t.Fatal("oversize payload was accepted")
	}
}

func TestKeyPersistsAndSignsVerifiableTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.json")
	key, err := LoadOrCreateKey(path, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := key.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["version"] != float64(vapidVersion) {
		t.Fatalf("vapid schema version = %v, want %d", stored["version"], vapidVersion)
	}
	reloaded, err := LoadOrCreateKey(path, "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reloaded.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("public key changed across load: %s vs %s", first, second)
	}
	if reloaded.Subject != "mailto:ops@example.com" {
		t.Fatalf("subject: %q", reloaded.Subject)
	}

	header, err := key.authorization("https://push.example/xyz", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("authorization: %q", header)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(header, "vapid t="), ",k="+first)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token parts: %d", len(parts))
	}
	signingInput := parts[0] + "." + parts[1]
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://push.example" {
		t.Fatalf("aud: %q", claims.Aud)
	}
	if claims.Sub != DefaultSubject {
		t.Fatalf("sub: %q", claims.Sub)
	}
	if time.Unix(claims.Exp, 0).Before(time.Now()) || time.Unix(claims.Exp, 0).After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("exp outside the 24h window: %s", time.Unix(claims.Exp, 0))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("JOSE signature length: %d", len(sig))
	}
	digest := sha256.Sum256([]byte(signingInput))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.priv.PublicKey, digest[:], r, s) {
		t.Fatal("ES256 signature did not verify")
	}
}

func TestStoreUpsertsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subs", "push-subscriptions.json")
	store := OpenStore(path)
	client := newTestClient(t)
	sub := client.subscription(t, "https://push.example/one")
	if err := store.Put(sub); err != nil {
		t.Fatal(err)
	}
	// A re-registration of the same endpoint refreshes rather than duplicates.
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	if err := store.Put(sub); err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 1 || got[0].Keys.Auth != sub.Keys.Auth {
		t.Fatalf("upsert: %+v", got)
	}

	if err := store.Put(Subscription{Endpoint: "http://evil.example/x"}); err == nil {
		t.Fatal("non-https endpoint was accepted")
	}
	if err := store.Put(Subscription{Endpoint: sub.Endpoint, Keys: SubscriptionKeys{P256DH: "AAAA", Auth: "AAAA"}}); err == nil {
		t.Fatal("short keys were accepted")
	}

	// The registry survives a reopen and Delete is idempotent.
	reopened := OpenStore(path)
	if got := reopened.List(); len(got) != 1 {
		t.Fatalf("reopen: %+v", got)
	}
	if err := reopened.Delete(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Delete(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 0 {
		t.Fatalf("delete: %+v", got)
	}
}

func TestServiceDeliversAndPrunes(t *testing.T) {
	client := newTestClient(t)
	type received struct {
		body []byte
		auth string
		enc  string
	}
	got := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		got <- received{body: body, auth: r.Header.Get("Authorization"), enc: r.Header.Get("Content-Encoding")}
	}))
	defer server.Close()

	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "vapid.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	store := OpenStore(filepath.Join(t.TempDir(), "push-subscriptions.json"))
	if err := store.Put(client.subscription(t, server.URL)); err != nil {
		t.Fatal(err)
	}
	service := NewService(key, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.Start(ctx)
	defer service.Close()

	service.Notify(Message{SessionID: "s-1", Title: "hello", CWD: "/tmp/work"})
	select {
	case r := <-got:
		if r.enc != "aes128gcm" {
			t.Fatalf("content-encoding: %q", r.enc)
		}
		if !strings.HasPrefix(r.auth, "vapid t=") {
			t.Fatalf("authorization: %q", r.auth)
		}
		var payload payload
		if err := json.Unmarshal(client.decrypt(t, r.body), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Type != "run_complete" || payload.SessionID != "s-1" || payload.Title != "hello" || payload.CWD != "/tmp/work" {
			t.Fatalf("payload: %+v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push was not delivered")
	}
}

func TestServicePrunesGoneSubscriptions(t *testing.T) {
	client := newTestClient(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()

	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "vapid.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	store := OpenStore(filepath.Join(t.TempDir(), "push-subscriptions.json"))
	if err := store.Put(client.subscription(t, server.URL)); err != nil {
		t.Fatal(err)
	}
	service := NewService(key, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.Start(ctx)

	service.Notify(Message{SessionID: "s-1", Title: "gone"})
	deadline := time.After(5 * time.Second)
	for {
		if len(store.List()) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("a 410 subscription was not pruned")
		case <-time.After(10 * time.Millisecond):
		}
	}
	service.Close()
}

func TestNormalizeSubject(t *testing.T) {
	cases := map[string]string{
		"":                         DefaultSubject,
		"ops@example.com":          "mailto:ops@example.com",
		"mailto:ops@example.com":   "mailto:ops@example.com",
		"https://example.com/ki":   "https://example.com/ki",
		"  mailto:a@example.com  ": "mailto:a@example.com",
	}
	for in, want := range cases {
		if got := NormalizeSubject(in); got != want {
			t.Fatalf("NormalizeSubject(%q) = %q, want %q", in, got, want)
		}
	}
}
