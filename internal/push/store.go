package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"ki/internal/state"
)

const storeVersion = 1

type Subscription struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
	Created  string           `json:"createdAt,omitempty"`
}

type SubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type storedFile struct {
	Version       int            `json:"version"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type Store struct {
	mu   sync.Mutex
	path string
	subs []Subscription
}

func OpenStore(path string) *Store {
	s := &Store{path: path}
	b, _, err := state.ReadFile(path, storeVersion, nil)
	if err != nil {
		return s
	}
	var doc storedFile
	if json.Unmarshal(b, &doc) == nil {
		s.subs = doc.Subscriptions
	}
	return s
}

func (s *Store) List() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Subscription(nil), s.subs...)
}

func (s *Store) Put(sub Subscription) error {
	if err := validateSubscription(sub); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].Endpoint == sub.Endpoint {
			sub.Created = s.subs[i].Created
			s.subs[i] = sub
			return s.writeLocked()
		}
	}
	if sub.Created == "" {
		sub.Created = time.Now().UTC().Format(time.RFC3339)
	}
	s.subs = append(s.subs, sub)
	return s.writeLocked()
}

func (s *Store) Delete(endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.subs[:0]
	for _, sub := range s.subs {
		if sub.Endpoint != endpoint {
			out = append(out, sub)
		}
	}
	if len(out) == len(s.subs) {
		return nil
	}
	s.subs = out
	return s.writeLocked()
}

func (s *Store) writeLocked() error {
	return state.WriteVersioned(s.path, storeVersion, storedFile{Version: storeVersion, Subscriptions: s.subs}, 0o600)
}

var ErrInvalidSubscription = errors.New("invalid push subscription")

func validateSubscription(sub Subscription) error {
	if strings.TrimSpace(sub.Endpoint) == "" {
		return fmt.Errorf("%w: empty endpoint", ErrInvalidSubscription)
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: bad endpoint", ErrInvalidSubscription)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	default:
		return fmt.Errorf("%w: endpoint must be https", ErrInvalidSubscription)
	}
	p256dh, err := decodeBase64(sub.Keys.P256DH)
	if err != nil || len(p256dh) != 65 {
		return fmt.Errorf("%w: p256dh must be a 65-byte P-256 point", ErrInvalidSubscription)
	}
	auth, err := decodeBase64(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return fmt.Errorf("%w: auth must be 16 bytes", ErrInvalidSubscription)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
