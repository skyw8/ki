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

// storeVersion is the schema version of push-subscriptions.json.
const storeVersion = 1

// Subscription is one browser push registration. P256DH and Auth are the
// client's public key and 16-byte auth secret, both base64url as the Push API
// reports them.
type Subscription struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
	Created  string           `json:"createdAt,omitempty"`
}

// SubscriptionKeys is the PushSubscription.toJSON().keys half.
type SubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// storedFile is the on-disk document.
type storedFile struct {
	Version       int            `json:"version"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// Store is the on-disk subscription registry. The whole set is small (one
// entry per browser profile that enabled notifications), so it lives in one
// JSON document rewritten on change, like the workspace registry.
type Store struct {
	mu   sync.Mutex
	path string
	subs []Subscription
}

// OpenStore loads path; a missing file is an empty store. Subscriptions are
// best-effort: a document from a newer schema yields an empty store (browsers
// re-sync on load), while writeLocked refuses to overwrite it.
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

// List returns a copy in registration order.
func (s *Store) List() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Subscription(nil), s.subs...)
}

// Put validates and upserts by endpoint. Registering the same endpoint twice
// (the client re-syncs on every load) refreshes its keys instead of duplicating
// the row.
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

// Delete removes an endpoint. A missing endpoint is not an error: the push
// service reports a dead subscription with the same idempotency.
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

// ErrInvalidSubscription marks a registration the server refuses to store.
var ErrInvalidSubscription = errors.New("invalid push subscription")

// validateSubscription rejects an endpoint or key material the sender could not
// use. The endpoint must be an absolute https URL, or http on loopback so tests
// (and a purely local setup) can point at a stub push service.
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
