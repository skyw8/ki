package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ki/internal/session"
)

func TestBrowserSessionsIsolatedAcrossLocalhostPorts(t *testing.T) {
	first, a := testServer(t)
	second, b := testServer(t)
	if first.serverID == "" || first.serverID == second.serverID {
		t.Fatal("server instances must have independent identities")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	secondHandler := second.Handler()
	client := &http.Client{Jar: jar}
	call := func(base, method, path, body, csrf string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-Ki-CSRF", csrf)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	status := func(base string, srv *Server, authenticated bool) {
		t.Helper()
		res := call(base, http.MethodGet, "/v1/auth/status", "", "")
		defer res.Body.Close()
		var out struct {
			Authenticated  bool   `json:"authenticated"`
			ServerID       string `json:"serverId"`
			CSRFCookieName string `json:"csrfCookieName"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK || out.Authenticated != authenticated || out.ServerID != srv.serverID || out.CSRFCookieName != srv.browserCSRFCookie {
			t.Fatalf("auth status: %d %+v", res.StatusCode, out)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("identity must not be cached across instance changes")
		}
	}
	cookie := func(name string) string {
		t.Helper()
		u, err := url.Parse(a.URL)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range jar.Cookies(u) {
			if c.Name == name {
				return c.Value
			}
		}
		return ""
	}
	expectStatus := func(res *http.Response, expected int) {
		t.Helper()
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != expected {
			t.Fatalf("HTTP %d, want %d: %s", res.StatusCode, expected, data)
		}
	}
	status(a.URL, first, false)
	expectStatus(call(a.URL, http.MethodPost, "/v1/auth/login", `{"token":"tok"}`, ""), http.StatusOK)
	status(a.URL, first, true)
	status(b.URL, second, false)
	expectStatus(call(b.URL, http.MethodGet, "/v1/sessions", "", ""), http.StatusUnauthorized)
	expectStatus(call(b.URL, http.MethodPost, "/v1/auth/login", `{"token":"tok"}`, ""), http.StatusOK)
	status(a.URL, first, true)
	status(b.URL, second, true)
	firstCSRF, secondCSRF := cookie(first.browserCSRFCookie), cookie(second.browserCSRFCookie)
	if firstCSRF == "" || secondCSRF == "" || firstCSRF == secondCSRF {
		t.Fatal("both independent CSRF cookies must coexist in the shared jar")
	}
	// The forward may switch before SSE reconnects. Even valid cookies for
	// both backends (or their shared test bearer token) cannot authorize a
	// request still bound to the previous backend.
	secondURL, err := url.Parse(b.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, bearer := range []bool{false, true} {
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/meta", ""},
			{http.MethodGet, "/v1/events", ""},
			{http.MethodPost, "/v1/auth/login", `{"token":"tok"}`},
			{http.MethodPost, "/v1/auth/logout", ""},
			{http.MethodPost, "/v1/sessions", `{"cwd":` + strconv.Quote(t.TempDir()) + `}`},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Ki-Server-ID", first.serverID)
			req.Header.Set("X-Ki-CSRF", secondCSRF)
			if bearer {
				req.Header.Set("Authorization", "Bearer tok")
			} else {
				for _, c := range jar.Cookies(secondURL) {
					req.AddCookie(c)
				}
			}
			recorder := httptest.NewRecorder()
			secondHandler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusMisdirectedRequest {
				t.Fatalf("foreign binding %s %s bearer=%v: %d", tc.method, tc.path, bearer, recorder.Code)
			}
		}
	}
	infos, err := session.List(second.cfg.Sessions.Root)
	if err != nil || len(infos) != 0 {
		t.Fatalf("misdirected create had side effects: %d sessions, %v", len(infos), err)
	}
	second.mu.Lock()
	sessionCount := len(second.browserSessions)
	second.mu.Unlock()
	if sessionCount != 1 {
		t.Fatalf("misdirected login/logout changed browser sessions: %d", sessionCount)
	}
	for _, path := range []string{"/v1/auth/status", "/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Ki-Server-ID", first.serverID)
		recorder := httptest.NewRecorder()
		secondHandler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("unbound discovery %s: %d", path, recorder.Code)
		}
	}
	unbound := httptest.NewRequest(http.MethodGet, "/v1/meta", nil)
	unbound.Header.Set("Authorization", "Bearer tok")
	recorder := httptest.NewRecorder()
	secondHandler.ServeHTTP(recorder, unbound)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unbound CLI bearer: %d", recorder.Code)
	}
	expectStatus(call(a.URL, http.MethodPost, "/v1/auth/logout", "", secondCSRF), http.StatusForbidden)
	status(a.URL, first, true)

	// Renewal on one port must not overwrite the other server's cookie pair.
	secondSession := cookie(second.browserSessionCookie)
	first.mu.Lock()
	first.browserSessions[cookie(first.browserSessionCookie)] = time.Now().Add(browserSessionTTL/2 - time.Minute)
	first.mu.Unlock()
	renewal := call(a.URL, http.MethodGet, "/v1/sessions", "", "")
	cookies := renewal.Cookies()
	expectStatus(renewal, http.StatusOK)
	if len(cookies) != 2 || cookies[0].Name != first.browserSessionCookie || cookies[1].Name != first.browserCSRFCookie {
		t.Fatalf("renewal cookie names: %+v", cookies)
	}
	if cookie(second.browserSessionCookie) != secondSession || cookie(second.browserCSRFCookie) != secondCSRF {
		t.Fatal("renewal changed the other server's cookies")
	}

	// The ready frame must let a client detect a port-forward retargeting.
	events := call(a.URL, http.MethodGet, "/v1/events", "", "")
	scanner := bufio.NewScanner(events.Body)
	var ready readyFrame
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			if err := json.Unmarshal([]byte(data), &ready); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	_ = events.Body.Close()
	if ready.Type != "ready" || ready.ServerID != first.serverID {
		t.Fatalf("ready identity: %+v", ready)
	}

	expectStatus(call(a.URL, http.MethodPost, "/v1/auth/logout", "", firstCSRF), http.StatusOK)
	status(a.URL, first, false)
	status(b.URL, second, true)
	if cookie(first.browserSessionCookie) != "" || cookie(first.browserCSRFCookie) != "" || cookie(second.browserSessionCookie) != secondSession {
		t.Fatal("logout must clear only its own cookies")
	}
	expectStatus(call(b.URL, http.MethodPost, "/v1/auth/logout", "", secondCSRF), http.StatusOK)
}

func TestJSONDefaultsToNoStoreAndPreservesRoutePolicy(t *testing.T) {
	for _, policy := range []string{"", "private, no-cache"} {
		recorder := httptest.NewRecorder()
		if policy != "" {
			recorder.Header().Set("Cache-Control", policy)
			recorder.Header().Set("ETag", `"catalog"`)
		}
		writeJSON(recorder, http.StatusOK, map[string]bool{"ok": true})
		expected := policy
		if expected == "" {
			expected = "no-store"
		}
		if got := recorder.Header().Get("Cache-Control"); got != expected {
			t.Fatalf("cache policy = %q, want %q", got, expected)
		}
		if policy != "" && recorder.Header().Get("ETag") != `"catalog"` {
			t.Fatal("explicit ETag must remain intact")
		}
	}
	srv, _ := testServer(t)
	for _, path := range []string{"/v1/models", "/v1/meta", "/v1/providers"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d cache=%q", path, recorder.Code, recorder.Header().Get("Cache-Control"))
		}
	}
}
