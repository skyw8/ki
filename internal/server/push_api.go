package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ki/internal/push"
	"ki/internal/session"
)

// Web Push API for the WebUI settings page.
//
// GET /v1/push/config hands the browser the VAPID public key it needs to build
// a subscription. POST/DELETE keep the server's registry in step with the
// browser: the client re-POSTs on every load, so an endpoint the server pruned
// (push service reported it gone) heals on the next visit.

func (s *Server) pushConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body := map[string]any{"enabled": false, "publicKey": ""}
	if s.push != nil {
		if public, err := s.push.PublicKey(); err == nil {
			body["enabled"] = true
			body["publicKey"] = public
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) putPushSubscription(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		http.Error(w, "push unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	err := s.push.PutSubscription(push.Subscription{
		Endpoint: body.Endpoint,
		Keys:     push.SubscriptionKeys{P256DH: body.Keys.P256DH, Auth: body.Keys.Auth},
	})
	if err != nil {
		if errors.Is(err, push.ErrInvalidSubscription) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePushSubscription(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		http.Error(w, "push unavailable", http.StatusServiceUnavailable)
		return
	}
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		http.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	if err := s.push.RemoveSubscription(endpoint); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyPushCompletion queues a Web Push for a finished run. It is a no-op
// without a push service, for a session without a directory, or for a subagent
// session: a tree child is an implementation detail of a parent run whose own
// completion already notifies.
//
// A user abort is silent for the same reason the WebUI suppresses its own
// notification: the browser's `run_aborted` sideband marks the run, and the
// agent_end that follows consumes the mark.
//
// Best-effort by design. The queue never blocks the run's event funnel, and a
// dropped message only means the browser learns of the completion from its own
// stream (or the resume catch-up) instead of the push service.
func (s *Server) notifyPushCompletion(id string) {
	aborted := s.consumePushAborted(id)
	if s.push == nil || aborted {
		return
	}
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return
	}
	info, err := s.slist.Row(dir)
	if err != nil {
		return
	}
	if info.ForkMode == session.ForkModeTree {
		return
	}
	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = "ki"
	}
	s.push.Notify(push.Message{SessionID: id, Title: title, CWD: info.CWD})
}

// markPushAborted records that the user aborted a run, before the loop emits
// the agent_end that would otherwise push a "finished" notification.
func (s *Server) markPushAborted(id string) {
	s.pushAbortMu.Lock()
	s.pushAborted[id] = struct{}{}
	s.pushAbortMu.Unlock()
}

// consumePushAborted reports and clears the abort mark. Clearing on the
// completion (rather than on the abort) keeps a later run of the same session
// notifying: the mark belongs to exactly one agent_end.
func (s *Server) consumePushAborted(id string) bool {
	s.pushAbortMu.Lock()
	defer s.pushAbortMu.Unlock()
	if _, ok := s.pushAborted[id]; !ok {
		return false
	}
	delete(s.pushAborted, id)
	return true
}
