package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	"ki/internal/state"
)

type Key struct {
	priv    *ecdsa.PrivateKey
	Subject string
}

type storedKey struct {
	Version int    `json:"version"`
	Private string `json:"private"`
	Public  string `json:"public"`
	Subject string `json:"subject"`
}

const vapidVersion = 1

func LoadOrCreateKey(path, subject string) (*Key, error) {
	subject = strings.TrimSpace(subject)
	if b, _, err := state.ReadFile(path, vapidVersion, nil); err == nil {
		var sk storedKey
		if err := json.Unmarshal(b, &sk); err != nil {
			return nil, fmt.Errorf("decode vapid key: %w", err)
		}
		raw, err := decodeBase64(sk.Private)
		if err != nil {
			return nil, fmt.Errorf("decode vapid private key: %w", err)
		}
		key, err := keyFromScalar(raw)
		if err != nil {
			return nil, err
		}
		key.Subject = NormalizeSubject(sk.Subject)
		if subject != "" {
			key.Subject = NormalizeSubject(subject)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	key := &Key{priv: priv, Subject: NormalizeSubject(subject)}
	public, err := key.PublicKey()
	if err != nil {
		return nil, err
	}
	doc := storedKey{
		Version: vapidVersion,
		Private: base64.RawURLEncoding.EncodeToString(priv.D.FillBytes(make([]byte, 32))),
		Public:  public,
		Subject: key.Subject,
	}
	if err := state.WriteVersioned(path, vapidVersion, doc, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

const DefaultSubject = "mailto:ki@localhost"

func NormalizeSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return DefaultSubject
	}
	if strings.HasPrefix(subject, "mailto:") || strings.HasPrefix(subject, "https://") {
		return subject
	}
	return "mailto:" + subject
}

func (k *Key) PublicKey() (string, error) {
	if k == nil || k.priv == nil {
		return "", errors.New("push key is not loaded")
	}
	b, err := k.priv.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (k *Key) authorization(endpoint string, now time.Time) (string, error) {
	audience, err := endpointOrigin(endpoint)
	if err != nil {
		return "", err
	}
	public, err := k.PublicKey()
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": k.Subject,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signingInput + "." + base64.RawURLEncoding.EncodeToString(sig) + ",k=" + public, nil
}

func endpointOrigin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse push endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("push endpoint is not absolute: %q", endpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

func keyFromScalar(raw []byte) (*Key, error) {
	d := new(big.Int).SetBytes(raw)
	if d.Sign() <= 0 || d.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, errors.New("vapid private key is out of range")
	}
	x, y := elliptic.P256().ScalarBaseMult(raw)
	return &Key{priv: &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y},
		D:         d,
	}}, nil
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "=")
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(value); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64 value")
}
