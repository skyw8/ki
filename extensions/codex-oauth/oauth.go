package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const callbackPath = "/auth/callback"
const refreshWindow = int64(60000)

var deviceLifetime = 15 * time.Minute

func nowMS() int64 { return time.Now().UnixMilli() }
func authBase() string {
	base, ok := os.LookupEnv("KI_CODEX_AUTH_BASE_URL")
	if !ok {
		base = "https://auth.openai.com"
	}
	return strings.TrimRight(base, "/")
}
func callbackPort() int {
	port, err := strconv.Atoi(os.Getenv("KI_CODEX_CALLBACK_PORT"))
	if err != nil || port <= 0 || port >= 65536 {
		return 1455
	}
	return port
}
func redirectURI() string { return fmt.Sprintf("http://localhost:%d%s", callbackPort(), callbackPath) }
func pkce() (string, string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:])
}
func authorizationFlow() (string, string, string) {
	verifier, challenge := pkce()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	state := hex.EncodeToString(b)
	query := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI()}, "scope": {"openid profile email offline_access"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "originator": {originator}}
	return verifier, state, authBase() + "/oauth/authorize?" + query.Encode()
}
func parseAuthInput(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		q := parsed.Query()
		return strings.TrimSpace(q.Get("code")), strings.TrimSpace(q.Get("state"))
	}
	if a, b, ok := strings.Cut(value, "#"); ok {
		return strings.TrimSpace(a), strings.TrimSpace(b)
	}
	if strings.Contains(value, "code=") {
		q, _ := url.ParseQuery(value)
		return strings.TrimSpace(q.Get("code")), strings.TrimSpace(q.Get("state"))
	}
	return value, ""
}
func accountID(access string) string {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var payload object
	if decode(string(raw), &payload) != nil {
		return ""
	}
	return str(obj(payload["https://api.openai.com/auth"])["chatgpt_account_id"])
}
func credentialFromToken(token object) (object, error) {
	access, refresh := str(token["access_token"]), str(token["refresh_token"])
	expires := integer(token["expires_in"])
	if access == "" || refresh == "" || expires <= 0 {
		return nil, errors.New("token response is incomplete")
	}
	account := accountID(access)
	if account == "" {
		return nil, errors.New("access token does not contain a ChatGPT account id")
	}
	return object{"type": "oauth", "value": object{"access": access, "refresh": refresh, "expires": nowMS() + expires*1000, "accountId": account}}, nil
}
func refreshCredential(ctx context.Context, credential object) (object, bool, error) {
	value := obj(credential["value"])
	if integer(value["expires"]) > nowMS()+refreshWindow {
		return credential, false, nil
	}
	refresh := str(value["refresh"])
	if refresh == "" {
		return nil, false, errors.New("OAuth credential has no refresh token")
	}
	token, err := formJSON(ctx, authBase()+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if err != nil {
		return nil, false, err
	}
	credential, err = credentialFromToken(token)
	return credential, true, err
}

type oauthSession struct {
	request object
	send    func(object)
	manual  chan string
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	server  *http.Server
}

func newOAuthSession(ctx context.Context, request object, send func(object)) *oauthSession {
	ctx, cancel := context.WithCancel(ctx)
	return &oauthSession{request: request, send: send, manual: make(chan string, 1), ctx: ctx, cancel: cancel}
}
func (s *oauthSession) event(kind string, values object) {
	p := object{"requestId": get(s.request, "requestId", ""), "provider": "openai-codex", "type": kind}
	for k, v := range values {
		if v != nil && v != "" {
			p[k] = v
		}
	}
	s.send(object{"jsonrpc": "2.0", "method": "provider.auth.event", "params": p})
}
func (s *oauthSession) close() {
	s.cancel()
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}
func (s *oauthSession) run() {
	var err error
	if get(s.request, "mode", "browser") == "device_code" {
		err = s.runDevice()
	} else {
		err = s.runBrowser()
	}
	if err != nil && s.ctx.Err() == nil {
		s.event("error", object{"error": safeError(err)})
	}
	s.close()
}
func (s *oauthSession) runBrowser() error {
	verifier, state, endpoint := authorizationFlow()
	result := make(chan string, 1)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort()))
	if err != nil {
		return fmt.Errorf("OAuth callback port %d is unavailable", callbackPort())
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != callbackPath || q.Get("state") != state {
			w.WriteHeader(400)
			return
		}
		code := q.Get("code")
		if code != "" {
			select {
			case result <- code:
			default:
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if code == "" {
			w.WriteHeader(400)
		}
		_, _ = w.Write([]byte("<p>OpenAI authentication completed. You can close this window.</p>"))
	})}
	s.mu.Lock()
	s.server = server
	s.mu.Unlock()
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	s.event("auth_url", object{"url": endpoint, "instructions": "Complete login in a browser. If the callback cannot reach this machine, paste the redirect URL or authorization code."})
	code := ""
	for code == "" {
		select {
		case <-s.ctx.Done():
			return nil
		case code = <-result:
		case input := <-s.manual:
			var supplied string
			code, supplied = parseAuthInput(input)
			if supplied != "" && supplied != state {
				return errors.New("OAuth state mismatch")
			}
		}
	}
	if s.ctx.Err() != nil {
		return nil
	}
	token, err := formJSON(s.ctx, authBase()+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI()}})
	if err != nil {
		return err
	}
	credential, err := credentialFromToken(token)
	if err != nil {
		return err
	}
	s.event("completed", object{"credential": credential})
	return nil
}
func (s *oauthSession) runDevice() error {
	status, body, err := httpJSON(s.ctx, "POST", authBase()+"/api/accounts/deviceauth/usercode", object{"client_id": clientID})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("device code request failed (%d)", status)
	}
	deviceID, code := str(body["device_auth_id"]), str(body["user_code"])
	interval := integer(fallback(body["interval"], 5))
	if deviceID == "" || code == "" {
		return errors.New("invalid device code response")
	}
	s.event("device_code", object{"userCode": code, "verificationUri": authBase() + "/codex/device", "intervalSeconds": interval, "expiresInSeconds": int64(deviceLifetime / time.Second)})
	deadline := time.Now().Add(deviceLifetime)
	for s.ctx.Err() == nil && time.Now().Before(deadline) {
		status, body, err = httpJSON(s.ctx, "POST", authBase()+"/api/accounts/deviceauth/token", object{"device_auth_id": deviceID, "user_code": code})
		if err != nil {
			return err
		}
		if status == 200 {
			code, verifier := str(body["authorization_code"]), str(body["code_verifier"])
			if code == "" || verifier == "" {
				return errors.New("invalid device auth response")
			}
			token, err := formJSON(s.ctx, authBase()+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {authBase() + "/deviceauth/callback"}})
			if err != nil {
				return err
			}
			credential, err := credentialFromToken(token)
			if err != nil {
				return err
			}
			s.event("completed", object{"credential": credential})
			return nil
		}
		errorCode := ""
		switch e := body["error"].(type) {
		case string:
			errorCode = e
		case map[string]any:
			errorCode = str(e["code"])
		}
		if status != 403 && status != 404 && errorCode != "deviceauth_authorization_pending" && errorCode != "authorization_pending" && errorCode != "slow_down" {
			return fmt.Errorf("device auth failed (%d)", status)
		}
		if errorCode == "slow_down" {
			interval += 5
		}
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	if s.ctx.Err() != nil {
		return nil
	}
	return errors.New("device code expired")
}
