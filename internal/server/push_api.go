package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ki/internal/push"
	"ki/internal/session"
)

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

func (s *Server) notifyPushQuestion(sess *session.Session, q session.Question) {
	if s.push == nil || !s.questionEligible(sess) || sess.Header.EffectiveForkMode() == session.ForkModeTree {
		return
	}
	title := strings.TrimSpace(session.TitleOf(sess))
	if title == "" {
		title = "ki"
	}
	s.push.Notify(push.Message{
		Type: "question_requested", SessionID: sess.ID(), Title: title,
		QuestionItemID: q.QuestionItemID, Body: q.Title,
	})
}

func (s *Server) markPushAborted(id string) {
	s.pushAbortMu.Lock()
	s.pushAborted[id] = struct{}{}
	s.pushAbortMu.Unlock()
}

func (s *Server) consumePushAborted(id string) bool {
	s.pushAbortMu.Lock()
	defer s.pushAbortMu.Unlock()
	if _, ok := s.pushAborted[id]; !ok {
		return false
	}
	delete(s.pushAborted, id)
	return true
}
