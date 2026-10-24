package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"ki/internal/provider"
)

func TestRefreshMagpieProviderModels(t *testing.T) {
	var mu sync.Mutex
	catalog := []byte(`{"data":[
		{"id":"vendor/current","display_name":"Current","context_window":262144,"max_output_tokens":32768,"modalities":["text","image"],"reasoning":true,"reasoning_levels":["low","high"]},
		{"id":"vendor/retired","display_name":"Retired"}
	]}`)
	var authMu sync.Mutex
	var seenAuth []string
	magpie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		authMu.Unlock()
		switch r.URL.Path {
		case "/api/hello":
			_, _ = io.WriteString(w, `{"name":"magpie","version":"test"}`)
		case "/v1/models":
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(catalog)
		default:
			http.NotFound(w, r)
		}
	}))
	defer magpie.Close()

	srv, hs := testServer(t)
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers["magpie"]
		pc.BaseURL = magpie.URL + "/v1"
		cfg.Providers["magpie"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.registry.SetCredential("magpie", ptrString("magpie-ki")); err != nil {
		t.Fatal(err)
	}

	refresh := func() (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/magpie/models/refresh", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}

	resp, body := refresh()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", resp.StatusCode, body)
	}
	if bytes.Contains(body, []byte("magpie-ki")) {
		t.Fatalf("provider response leaked key: %s", body)
	}
	authMu.Lock()
	gotAuth := append([]string(nil), seenAuth...)
	authMu.Unlock()
	if !slices.Equal(gotAuth, []string{"Bearer magpie-ki", "Bearer magpie-ki"}) {
		t.Fatalf("Magpie auth headers = %v", seenAuth)
	}
	magpieProvider := findProvider(t, srv.registry.Providers(), "magpie")
	if len(magpieProvider.Models) != 2 || magpieProvider.Models[0].ID != "vendor/current" {
		t.Fatalf("first synced models = %+v", magpieProvider.Models)
	}
	current := magpieProvider.Models[0]
	if current.Name != "Current" || current.ContextWindow != 262144 || current.MaxTokens != 32768 ||
		!slices.Contains(current.Input, "image") || !current.Reasoning ||
		current.ThinkingLevelMap["low"] == nil || current.ThinkingLevelMap["medium"] != nil || current.Cost != nil {
		t.Fatalf("mapped model = %+v", current)
	}

	disabled := false
	overrideName := "Local display override"
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers["magpie"]
		pc.Models = append(pc.Models, provider.ModelSeed{
			ID: "local/custom", Name: "Manual", ContextWindow: 1000, MaxTokens: 500, Input: []string{"text"},
		})
		pc.ModelOverrides = map[string]provider.ModelOverride{
			"vendor/current": {Name: &overrideName},
			"vendor/retired": {Enabled: &disabled},
		}
		cfg.Providers["magpie"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	catalog = []byte(`{"data":[
		{"id":"vendor/current","display_name":"Renamed upstream"},
		{"id":"group/daily"}
	]}`)
	mu.Unlock()
	resp, body = refresh()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second refresh status=%d body=%s", resp.StatusCode, body)
	}
	magpieProvider = findProvider(t, srv.registry.Providers(), "magpie")
	modelIDs := make([]string, 0, len(magpieProvider.Models))
	for _, model := range magpieProvider.Models {
		modelIDs = append(modelIDs, model.ID)
		if model.ID == "vendor/current" && model.Name != overrideName {
			t.Fatalf("same-ID override not preserved: %+v", model)
		}
	}
	if !slices.Contains(modelIDs, "group/daily") || !slices.Contains(modelIDs, "local/custom") ||
		slices.Contains(modelIDs, "vendor/retired") || slices.Contains(modelIDs, "vendor/current") == false {
		t.Fatalf("reconciled models = %v", modelIDs)
	}
	if _, ok := srv.registry.UserConfig().Providers["magpie"].ModelOverrides["vendor/retired"]; ok {
		t.Fatal("removed remote model override was not pruned")
	}

	before := append([]provider.Model(nil), magpieProvider.Models...)
	mu.Lock()
	catalog = []byte(`{"data":[{"id":"duplicate"},{"id":"duplicate"}]}`)
	mu.Unlock()
	resp, body = refresh()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("invalid catalog status=%d body=%s", resp.StatusCode, body)
	}
	after := findProvider(t, srv.registry.Providers(), "magpie").Models
	if !slices.EqualFunc(before, after, func(a, b provider.Model) bool { return a.ID == b.ID && a.Name == b.Name }) {
		t.Fatalf("failed refresh changed catalog: before=%+v after=%+v", before, after)
	}

	unauthenticated, err := http.Post(hs.URL+"/v1/providers/magpie/models/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated refresh status = %d", unauthenticated.StatusCode)
	}
	notMagpie, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/openai/models/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	notMagpie.Header.Set("Authorization", "Bearer tok")
	notMagpieResponse, err := http.DefaultClient.Do(notMagpie)
	if err != nil {
		t.Fatal(err)
	}
	_ = notMagpieResponse.Body.Close()
	if notMagpieResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("non-Magpie refresh status = %d", notMagpieResponse.StatusCode)
	}
}

func TestRefreshMagpieRejectsWrongGatewayAndMissingCredential(t *testing.T) {
	srv, hs := testServer(t)
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers["magpie"]
		pc.BaseURL = "http://127.0.0.1:1/v1"
		cfg.Providers["magpie"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func() *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/magpie/models/refresh", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "API key") {
		t.Fatalf("missing credential status=%d body=%s", res.StatusCode, body)
	}
	if err := srv.registry.SetCredential("magpie", ptrString("key")); err != nil {
		t.Fatal(err)
	}
	res = call()
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "request Magpie") {
		t.Fatalf("unavailable gateway status=%d body=%s", res.StatusCode, body)
	}
}

func TestRefreshMagpieSupportsAnthropicRootBaseURL(t *testing.T) {
	paths := make(chan string, 2)
	magpie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		switch r.URL.Path {
		case "/api/hello":
			_, _ = io.WriteString(w, `{"name":"magpie"}`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"group/anthropic"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer magpie.Close()
	models, err := fetchMagpieModels(t.Context(), magpie.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "group/anthropic" {
		t.Fatalf("models = %+v", models)
	}
	got := []string{<-paths, <-paths}
	if !slices.Contains(got, "/api/hello") || !slices.Contains(got, "/v1/models") {
		t.Fatalf("requested paths = %v", got)
	}
}

func TestRefreshMagpieDoesNotCommitIfCredentialChangesDuringFetch(t *testing.T) {
	srv, hs := testServer(t)
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers["magpie"]
		pc.BaseURL = "http://placeholder/v1"
		cfg.Providers["magpie"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.registry.SetCredential("magpie", ptrString("old-key")); err != nil {
		t.Fatal(err)
	}
	previous := findProvider(t, srv.registry.Providers(), "magpie").Models
	magpie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer old-key" {
			t.Errorf("request authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/hello":
			_, _ = io.WriteString(w, `{"name":"magpie"}`)
		case "/v1/models":
			if err := srv.registry.SetCredential("magpie", ptrString("new-key")); err != nil {
				t.Errorf("rotate credential: %v", err)
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"stale/model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer magpie.Close()
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers["magpie"]
		pc.BaseURL = magpie.URL + "/v1"
		cfg.Providers["magpie"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/magpie/models/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("refresh status=%d body=%s", resp.StatusCode, body)
	}
	after := findProvider(t, srv.registry.Providers(), "magpie").Models
	if !slices.EqualFunc(previous, after, func(a, b provider.Model) bool { return a.ID == b.ID && a.Name == b.Name }) {
		t.Fatalf("configuration race changed models: before=%+v after=%+v", previous, after)
	}
}

func findProvider(t *testing.T, providers []provider.View, id string) provider.Provider {
	t.Helper()
	for _, view := range providers {
		if view.ID == id {
			return view.Provider
		}
	}
	t.Fatalf("provider %q not found", id)
	return provider.Provider{}
}

func ptrString(value string) *string { return &value }
