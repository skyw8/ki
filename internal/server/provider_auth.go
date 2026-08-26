package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ki/internal/extension"
	"ki/internal/provider"
)

const providerAuthStateTTL = 30 * time.Minute

type providerAuthState struct {
	Provider         string
	RequestID        string
	Status           string
	EventType        string
	AuthURL          string
	Instructions     string
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
	Error            string
	CreatedAt        time.Time
}

func (s *Server) providerAuthSpec(id string) (provider.AuthSpec, bool) {
	if s.providerExtensions == nil || !s.registry.ExtensionProvider(id) {
		return provider.AuthSpec{}, false
	}
	for _, item := range s.registry.Providers() {
		if item.ID == id {
			return item.Auth, true
		}
	}
	return provider.AuthSpec{}, false
}

func (s *Server) startProviderAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	auth, ok := s.providerAuthSpec(id)
	if !ok {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}
	if auth.Type != provider.AuthOAuth {
		http.Error(w, "provider does not support OAuth", http.StatusUnprocessableEntity)
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	mode := strings.TrimSpace(body.Mode)
	if mode == "" {
		mode = "browser"
	}
	if mode != "browser" && mode != "device_code" {
		http.Error(w, "auth mode must be browser or device_code", http.StatusBadRequest)
		return
	}
	requestID, err := s.providerExtensions.StartAuth(r.Context(), id, mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.providerAuthMu.Lock()
	s.pruneProviderAuthLocked(time.Now())
	state := s.providerAuth[providerAuthKey(id, requestID)]
	if state == nil {
		state = &providerAuthState{Provider: id, RequestID: requestID, Status: "pending", CreatedAt: time.Now()}
		s.providerAuth[providerAuthKey(id, requestID)] = state
	} else if state.Status == "" {
		// A very fast sidecar may emit completed before the start RPC response
		// reaches this handler. Do not overwrite that terminal transition with
		// pending when the HTTP start response is assembled.
		state.Status = "pending"
	}
	s.providerAuthMu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"provider": id, "requestId": requestID, "status": "pending", "mode": mode,
	})
}

func (s *Server) providerAuthStatus(w http.ResponseWriter, r *http.Request) {
	id, requestID := r.PathValue("id"), r.PathValue("requestId")
	s.providerAuthMu.Lock()
	s.pruneProviderAuthLocked(time.Now())
	state := s.providerAuth[providerAuthKey(id, requestID)]
	if state != nil {
		copy := *state
		state = &copy
	}
	s.providerAuthMu.Unlock()
	if state == nil {
		http.Error(w, "auth request not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":         state.Provider,
		"requestId":        state.RequestID,
		"status":           state.Status,
		"eventType":        state.EventType,
		"authUrl":          state.AuthURL,
		"instructions":     state.Instructions,
		"userCode":         state.UserCode,
		"verificationUri":  state.VerificationURI,
		"intervalSeconds":  state.IntervalSeconds,
		"expiresInSeconds": state.ExpiresInSeconds,
		"error":            state.Error,
	})
}

func (s *Server) providerAuthInput(w http.ResponseWriter, r *http.Request) {
	id, requestID := r.PathValue("id"), r.PathValue("requestId")
	if !s.providerAuthExists(id, requestID) {
		http.Error(w, "auth request not found", http.StatusNotFound)
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Value) == "" {
		http.Error(w, "value required", http.StatusBadRequest)
		return
	}
	if err := s.providerExtensions.AuthInput(r.Context(), id, requestID, body.Value); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"provider": id, "requestId": requestID, "status": "pending"})
}

func (s *Server) cancelProviderAuth(w http.ResponseWriter, r *http.Request) {
	id, requestID := r.PathValue("id"), r.PathValue("requestId")
	if !s.providerAuthExists(id, requestID) {
		http.Error(w, "auth request not found", http.StatusNotFound)
		return
	}
	unlockCredential := s.providerExtensions.LockCredential(id)
	if err := s.providerExtensions.CancelAuth(r.Context(), id, requestID); err != nil {
		unlockCredential()
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	status := "cancelled"
	s.providerAuthMu.Lock()
	state := s.providerAuth[providerAuthKey(id, requestID)]
	if state != nil {
		if state.Status != "completed" && state.Status != "error" {
			state.Status = status
		} else {
			status = state.Status
		}
	}
	s.providerAuthMu.Unlock()
	unlockCredential()
	writeJSON(w, http.StatusOK, map[string]any{"provider": id, "requestId": requestID, "status": status})
}

func (s *Server) logoutProviderAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	auth, ok := s.providerAuthSpec(id)
	if !ok {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}
	if auth.Type != provider.AuthOAuth {
		http.Error(w, "provider does not support OAuth", http.StatusUnprocessableEntity)
		return
	}
	unlockCredential := s.providerExtensions.LockCredential(id)
	var active []string
	s.providerAuthMu.Lock()
	// Why: completion events arrive asynchronously from the sidecar. Serialize
	// clearing credentials with completion persistence so Logout cannot be
	// undone by an in-flight OAuth callback.
	if err := s.registry.SetCredential(id, nil); err != nil {
		s.providerAuthMu.Unlock()
		unlockCredential()
		registryError(w, err)
		return
	}
	for _, state := range s.providerAuth {
		if state.Provider != id || state.Status == "completed" || state.Status == "cancelled" {
			continue
		}
		active = append(active, state.RequestID)
		state.Status = "cancelled"
	}
	s.providerAuthMu.Unlock()
	unlockCredential()
	for _, requestID := range active {
		cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.providerExtensions.CancelAuth(cancelCtx, id, requestID)
		cancel()
	}
	s.providers(w, r)
}

func (s *Server) providerAuthExists(providerID, requestID string) bool {
	s.providerAuthMu.Lock()
	defer s.providerAuthMu.Unlock()
	s.pruneProviderAuthLocked(time.Now())
	_, ok := s.providerAuth[providerAuthKey(providerID, requestID)]
	return ok
}

func (s *Server) onProviderAuthEvent(event extension.ProviderAuthEvent) {
	if event.Provider == "" || event.RequestID == "" {
		return
	}
	now := time.Now()
	key := providerAuthKey(event.Provider, event.RequestID)
	var unlockCredential func()
	if event.Type == "completed" && s.providerExtensions != nil {
		// Keep the same lock order as Logout: credential mutation first, then
		// auth state. This also prevents refresh from racing a completed login.
		unlockCredential = s.providerExtensions.LockCredential(event.Provider)
		defer unlockCredential()
	}
	s.providerAuthMu.Lock()
	defer s.providerAuthMu.Unlock()
	s.pruneProviderAuthLocked(now)
	state := s.providerAuth[key]
	if state == nil {
		state = &providerAuthState{
			Provider: event.Provider, RequestID: event.RequestID, Status: "pending", CreatedAt: now,
		}
		s.providerAuth[key] = state
	}
	if state.Status == "cancelled" || state.Status == "error" || state.Status == "completed" {
		return
	}
	state.EventType = event.Type
	if event.URL != "" {
		state.AuthURL = event.URL
	}
	if event.Instructions != "" {
		state.Instructions = event.Instructions
	}
	if event.UserCode != "" {
		state.UserCode = event.UserCode
	}
	if event.VerificationURI != "" {
		state.VerificationURI = event.VerificationURI
	}
	if event.IntervalSeconds != 0 {
		state.IntervalSeconds = event.IntervalSeconds
	}
	if event.ExpiresInSeconds != 0 {
		state.ExpiresInSeconds = event.ExpiresInSeconds
	}
	if event.Type == "error" {
		state.Status = "error"
		state.Error = strings.TrimSpace(event.Error)
		if state.Error == "" {
			state.Error = "provider authentication failed"
		}
		return
	}
	if event.Type != "completed" {
		state.Status = "pending"
		return
	}
	if event.Credential == nil || event.Credential.Type != provider.AuthOAuth || len(event.Credential.Value) == 0 {
		state.Status = "error"
		state.Error = "provider returned an invalid OAuth credential"
		return
	}
	// Keep the auth state lock while persisting. Logout takes the same lock, so
	// either the completed credential wins and is then cleared, or cancellation
	// wins and this event is discarded above.
	if err := s.registry.SetCredentialValue(event.Provider, provider.AuthOAuth, event.Credential.Value); err != nil {
		state.Status = "error"
		state.Error = fmt.Sprintf("save credential: %v", err)
		return
	}
	state.Status = "completed"
}

func providerAuthKey(providerID, requestID string) string { return providerID + "\x00" + requestID }

func (s *Server) pruneProviderAuthLocked(now time.Time) {
	for key, state := range s.providerAuth {
		if now.Sub(state.CreatedAt) > providerAuthStateTTL {
			delete(s.providerAuth, key)
		}
	}
}
