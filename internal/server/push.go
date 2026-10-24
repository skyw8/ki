package server

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"ki/internal/loop"
)

const (
	scopeSessions   = "sessions"
	scopeWorkspaces = "workspaces"
	scopeProviders  = "providers"
	scopeExtensions = "extensions"

	pushQueueLimit = 512
)

var ssePingInterval = 15 * time.Second

type invalidateFrame struct {
	Type  string `json:"type"`
	Scope string `json:"scope"`
}

type readyFrame struct {
	Type     string `json:"type"`
	ServerID string `json:"serverId"`
}

type globalEvent struct {
	loop.Event
	SessionID string `json:"sessionId,omitempty"`
}

type pushFrame struct {
	sessionID string
	event     loop.Event
}

type pushSub struct {
	mu     sync.Mutex
	scopes map[string]bool
	events []pushFrame
	dead   bool
	wake   chan struct{}
}

func newPushSub() *pushSub {
	return &pushSub{scopes: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (p *pushSub) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pushSub) invalidate(scope string) {
	p.mu.Lock()
	p.scopes[scope] = true
	p.mu.Unlock()
	p.signal()
}

func (p *pushSub) push(f pushFrame) {
	p.mu.Lock()
	if len(p.events) >= pushQueueLimit {
		p.dead = true
	} else {
		p.events = append(p.events, f)
	}
	p.mu.Unlock()
	p.signal()
}

func (p *pushSub) takeScopes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.scopes) == 0 {
		return nil
	}
	out := slices.Sorted(maps.Keys(p.scopes))
	p.scopes = map[string]bool{}
	return out
}

func (p *pushSub) pop() (pushFrame, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) == 0 {
		return pushFrame{}, false
	}
	f := p.events[0]
	p.events = p.events[1:]
	return f, true
}

func (p *pushSub) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

func (s *Server) subscribePush(p *pushSub) {
	s.gsMu.Lock()
	s.gsubs[p] = struct{}{}
	s.gsMu.Unlock()
}

func (s *Server) unsubscribePush(p *pushSub) {
	s.gsMu.Lock()
	delete(s.gsubs, p)
	s.gsMu.Unlock()
}

func (s *Server) publishPush(sessionID string, ev loop.Event) {
	f := pushFrame{sessionID: sessionID, event: ev}
	s.gsMu.Lock()
	for p := range s.gsubs {
		p.push(f)
	}
	s.gsMu.Unlock()
}

func (s *Server) publishInvalidation(scope string) {
	s.gsMu.Lock()
	for p := range s.gsubs {
		p.invalidate(scope)
	}
	s.gsMu.Unlock()
}

func (s *Server) pushEvents(w http.ResponseWriter, r *http.Request) {
	s.runtimeMu.Lock()
	closed, shutdown := s.runtimeClosed, s.runtimeCtx
	s.runtimeMu.Unlock()
	if closed {
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	p := newPushSub()
	s.subscribePush(p)
	defer s.unsubscribePush(p)
	writer := newSSEWriter(w)
	defer writer.watch(r.Context())()
	write := func(name string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			slog.Error("marshal push frame", "err", err)
			return false
		}
		return writer.write("event: %s\ndata: %s\n\n", name, b) == nil
	}
	if !write("ready", readyFrame{Type: "ready", ServerID: s.serverID}) {
		return
	}
	ticker := time.NewTicker(ssePingInterval)
	defer ticker.Stop()
	for {
		if scopes := p.takeScopes(); len(scopes) > 0 {
			for _, scope := range scopes {
				if !write("invalidate", invalidateFrame{Type: "invalidate", Scope: scope}) {
					return
				}
			}
			continue
		}
		if f, ok := p.pop(); ok {
			if !write(string(f.event.Type), globalEvent{Event: f.event, SessionID: f.sessionID}) {
				return
			}
			continue
		}
		if p.isDead() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-shutdown.Done():
			return
		case <-p.wake:
		case <-ticker.C:
			if err := writer.write(": ping\n\n"); err != nil {
				return
			}
		}
	}
}
