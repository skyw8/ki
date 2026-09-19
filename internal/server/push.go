package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"ki/internal/loop"
)

// WebUI push channel: GET /v1/events is one SSE stream per browser tab that
// carries everything the server pushes that no client request asked for.
//
// Two frame kinds:
//
//   - invalidate: "this slice of server state changed, refetch it". State is
//     never pushed inline; the client re-reads the ordinary REST endpoint, and
//     GET /v1/sessions carries an ETag so an unchanged list answers 304.
//   - session sideband loop events (agent_end, run_aborted, runtime_ready,
//     extension notices/UI, manual compaction progress) tagged with sessionId.
//
// Why one stream: a tab used to hold one notification SSE per running session
// plus one for the selected session, and plaintext HTTP/1.1 caps a browser at
// six connections per origin (see docs/webui.md). One stream also removes the
// per-session subscribe-time runtime_ready catch-up: the client reads
// runtime.ready from GET /v1/sessions/{id} instead.
const (
	// Invalidate scopes name the client-side refetch handler.
	scopeSessions   = "sessions"
	scopeWorkspaces = "workspaces"
	scopeProviders  = "providers"
	scopeExtensions = "extensions"

	// pushQueueLimit bounds one slow subscriber's sideband backlog. Overflow
	// closes the connection instead of silently dropping a terminal agent_end:
	// the client reconnects and refetches rather than losing a completion.
	pushQueueLimit = 512
)

// invalidateFrame is the payload of an event: invalidate frame.
type invalidateFrame struct {
	Type  string `json:"type"`
	Scope string `json:"scope"`
}

// readyFrame flushes the response so the client's fetch resolves. Receiving it
// means "the subscription is live", which is the client's cue to refetch.
type readyFrame struct {
	Type string `json:"type"`
}

// globalEvent is the payload of a sideband frame: the loop event plus the
// session it belongs to, now that one stream serves every session.
type globalEvent struct {
	loop.Event
	SessionID string `json:"sessionId,omitempty"`
}

// pushFrame is one queued sideband frame.
type pushFrame struct {
	sessionID string
	event     loop.Event
}

// pushSub is one GET /v1/events connection. Invalidations coalesce into a
// scope set because they describe current state, so an intermediate frame is
// redundant rather than lost; sideband frames keep their arrival order.
type pushSub struct {
	mu     sync.Mutex
	scopes map[string]bool
	events []pushFrame
	// dead is set on queue overflow. The writer ends the response; the client
	// reconnects within its backoff instead of the server dropping the frame.
	dead bool
	wake chan struct{}
}

func newPushSub() *pushSub {
	return &pushSub{scopes: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// signal wakes the writer without blocking the publisher, which may run while
// holding Server.mu.
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

// takeScopes returns the coalesced scopes and clears them. Sorted so a test or
// log sees a deterministic order.
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

// publishPush fans a session sideband event out to every push subscriber. The
// session id lets each client pick out the events it acts on.
func (s *Server) publishPush(sessionID string, ev loop.Event) {
	f := pushFrame{sessionID: sessionID, event: ev}
	s.gsMu.Lock()
	for p := range s.gsubs {
		p.push(f)
	}
	s.gsMu.Unlock()
}

// publishInvalidation tells every client that a slice of state changed. It
// carries no session id on purpose: the client refetches the authoritative
// list, which is also how it notices that the session it has open was deleted.
func (s *Server) publishInvalidation(scope string) {
	s.gsMu.Lock()
	for p := range s.gsubs {
		p.invalidate(scope)
	}
	s.gsMu.Unlock()
}

// pushEvents streams every push frame to one client.
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache") // proxies must not buffer events.
	fl, _ := w.(http.Flusher)
	write := func(name string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			slog.Error("marshal push frame", "err", err)
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		return true
	}
	// Subscribe first, then flush: the client fetches only after this frame
	// arrives, so a change that happened while it was disconnected cannot be
	// missed between the fetch and the subscription.
	if !write("ready", readyFrame{Type: "ready"}) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
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
			// Comment frames keep an idle connection (and any intermediary)
			// alive; SSE clients ignore them.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}
}
