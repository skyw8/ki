package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Message struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	CWD       string `json:"cwd,omitempty"`
}

type payload struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	CWD       string `json:"cwd,omitempty"`
}

const (
	payloadLimit = 4096 - 16 - 1
	ttlSeconds   = 24 * 60 * 60
	queueDepth   = 64
)

var errSubscriptionGone = errors.New("push subscription is gone")

type Service struct {
	key    *Key
	store  *Store
	client *http.Client
	queue  chan Message
	done   chan struct{}
	wg     sync.WaitGroup
}

func NewService(key *Key, store *Store) *Service {
	return &Service{
		key:    key,
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
		queue:  make(chan Message, queueDepth),
		done:   make(chan struct{}),
	}
}

func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case m := <-s.queue:
				s.deliver(ctx, m)
			}
		}
	}()
}

func (s *Service) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.wg.Wait()
}

func (s *Service) PublicKey() (string, error) { return s.key.PublicKey() }

func (s *Service) PutSubscription(sub Subscription) error { return s.store.Put(sub) }

func (s *Service) RemoveSubscription(endpoint string) error { return s.store.Delete(endpoint) }

func (s *Service) Notify(m Message) {
	if s == nil {
		return
	}
	select {
	case s.queue <- m:
	default:
		slog.Warn("push queue full, dropping notification", "session_id", m.SessionID)
	}
}

func (s *Service) deliver(ctx context.Context, m Message) {
	subs := s.store.List()
	if len(subs) == 0 {
		return
	}
	body, err := json.Marshal(payload{Type: "run_complete", SessionID: m.SessionID, Title: m.Title, CWD: m.CWD})
	if err != nil {
		slog.Warn("marshal push payload", "err", err)
		return
	}
	for _, sub := range subs {
		err := s.send(ctx, sub, body)
		switch {
		case err == nil:
		case errors.Is(err, errSubscriptionGone):
			if err := s.store.Delete(sub.Endpoint); err != nil {
				slog.Warn("prune push subscription", "err", err)
			}
		case ctx.Err() != nil:
			return
		default:
			slog.Warn("web push delivery failed", "endpoint", sub.Endpoint, "err", err)
		}
	}
}

func (s *Service) send(ctx context.Context, sub Subscription, body []byte) error {
	encrypted, err := encrypt(body, sub.Keys.P256DH, sub.Keys.Auth)
	if err != nil {
		return err
	}
	auth, err := s.key.authorization(sub.Endpoint, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(encrypted))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", fmt.Sprint(ttlSeconds))
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return errSubscriptionGone
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	default:
		return fmt.Errorf("push endpoint %s: %s", endpointHost(sub.Endpoint), res.Status)
	}
}

func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	return u.Host
}

func encrypt(plaintext []byte, p256dhB64, authB64 string) ([]byte, error) {
	if len(plaintext)+1 > payloadLimit {
		return nil, fmt.Errorf("push payload too large: %d bytes", len(plaintext))
	}
	uaBytes, err := decodeBase64(p256dhB64)
	if err != nil {
		return nil, fmt.Errorf("decode p256dh: %w", err)
	}
	authSecret, err := decodeBase64(authB64)
	if err != nil {
		return nil, fmt.Errorf("decode auth: %w", err)
	}
	curve := ecdh.P256()
	uaPublic, err := curve.NewPublicKey(uaBytes)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	asPrivate, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	secret, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	prk, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, err
	}
	keyInfo := make([]byte, 0, len("WebPush: info\x00")+len(uaBytes)+len(asPublic))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaBytes...)
	keyInfo = append(keyInfo, asPublic...)
	ikm, err := hkdf.Expand(sha256.New, prk, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk2, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk2, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk2, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := append(append([]byte{}, plaintext...), 0x02)
	ciphertext := gcm.Seal(nil, nonce, record, nil)

	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, 4096)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return append(header, ciphertext...), nil
}
