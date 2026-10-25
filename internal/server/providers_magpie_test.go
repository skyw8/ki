package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	notMagpie, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/missing-provider/models/refresh", nil)
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
		t.Fatalf("missing provider refresh status = %d", notMagpieResponse.StatusCode)
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
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "request model catalog") {
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

func TestFetchProviderModelsProtocols(t *testing.T) {
	for _, tc := range []struct {
		name      string
		api       string
		basePath  string
		modelPath string
		cursor    string
	}{
		{"openai", "responses", "/v1", "/v1/models", "after"},
		{"compatible", "completions", "/gateway/v1", "/gateway/v1/models", "after"},
		{"anthropic", "anthropic", "", "/v1/models", "after_id"},
		{"minimax", "anthropic", "/anthropic", "/anthropic/v1/models", "after_id"},
		{"anthropic-versioned", "anthropic", "/proxy/v1", "/proxy/v1/models", "after_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != tc.modelPath || r.Header.Get("Accept") != "application/json" {
					t.Errorf("model list request = %s %s, headers=%v", r.Method, r.URL, r.Header)
				}
				if tc.api == "anthropic" {
					if r.Header.Get("x-api-key") != "catalog-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Errorf("Anthropic headers = %v", r.Header)
					}
				} else if r.Header.Get("Authorization") != "Bearer catalog-key" || r.Header.Get("x-api-key") != "" {
					t.Errorf("OpenAI-compatible headers = %v", r.Header)
				}
				switch r.URL.Query().Get(tc.cursor) {
				case "":
					_, _ = io.WriteString(w, `{"data":[{"id":"existing"}],"has_more":true,"last_id":"existing"}`)
				case "existing":
					_, _ = io.WriteString(w, `{"data":[{"id":"new","display_name":"New model","max_input_tokens":120000,"max_tokens":9000}],"has_more":false}`)
				default:
					t.Errorf("unexpected pagination: %s", r.URL)
				}
			}))
			defer upstream.Close()
			current := provider.Provider{ID: tc.name, API: tc.api, BaseURL: upstream.URL + tc.basePath, Models: []provider.Model{
				{ID: "existing", Name: "Existing model", ContextWindow: 200000, MaxTokens: 32000, Input: []string{"text", "image"}},
			}}
			models, err := fetchProviderModels(t.Context(), current, "catalog-key")
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || len(models) != 2 || models[0].Name != "Existing model" || models[0].ContextWindow != 200000 || !slices.Contains(models[0].Input, "image") || models[1].Name != "New model" || models[1].ContextWindow != 120000 || models[1].MaxTokens != 9000 {
				t.Fatalf("requests=%d models=%+v", requests, models)
			}
		})
	}
}

func TestProviderModelsURL(t *testing.T) {
	for _, tc := range []struct{ base, api, path string }{
		{"https://api.deepseek.com", "completions", "/models"},
		{"https://openrouter.ai/api/v1", "completions", "/api/v1/models"},
		{"https://generativelanguage.googleapis.com/v1beta/openai/", "completions", "/v1beta/openai/models"},
		{"https://api.z.ai/api/coding/paas/v4", "completions", "/api/coding/paas/v4/models"},
		{"https://api.minimax.io/anthropic", "anthropic", "/anthropic/v1/models"},
	} {
		endpoint, err := providerModelsURL(tc.base, tc.api)
		if err != nil || endpoint.Path != tc.path {
			t.Fatalf("base=%q endpoint=%v err=%v", tc.base, endpoint, err)
		}
	}
	for _, base := range []string{"relative", "file:///models", "https://user:password@upstream/v1", "https://upstream/v1?key=secret", "https://upstream/v1#fragment"} {
		if _, err := providerModelsURL(base, "completions"); err == nil {
			t.Fatalf("accepted invalid base %q", base)
		}
	}
}

func TestDashScopeCatalogSelection(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", true},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", true},
		{"https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", true},
		{"https://dashscope-intl.aliyuncs.com/api/v1", true},
		{"https://dashscope-intl.aliyuncs.com/custom/v1", false},
		{"https://gateway.example/compatible-mode/v1", false},
		{"https://dashscope-intl.aliyuncs.com.example/compatible-mode/v1", false},
	} {
		endpoint, err := providerModelsURL(tc.base, "completions")
		if err != nil {
			t.Fatal(err)
		}
		if got := dashScopeCatalog(endpoint); got != tc.want {
			t.Fatalf("base=%q selected=%v want=%v", tc.base, got, tc.want)
		}
	}
}

func TestFetchProviderModelsRejectsIncompleteCatalog(t *testing.T) {
	for _, tc := range []struct{ name, first, next string }{
		{"missing data", `{}`, ""},
		{"null data", `{"data":null}`, ""},
		{"invalid data", `{"data":{}}`, ""},
		{"duplicate ids", `{"data":[{"id":"same"}],"has_more":true,"last_id":"same"}`, `{"data":[{"id":"same"}]}`},
		{"repeated cursor", `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`, `{"data":[{"id":"next"}],"has_more":true,"last_id":"first"}`},
		{"empty page", `{"data":[],"has_more":true}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := tc.first
				if r.URL.Query().Get("after") != "" {
					body = tc.next
				}
				_, _ = io.WriteString(w, body)
			}))
			defer upstream.Close()
			if _, err := fetchProviderModels(t.Context(), provider.Provider{ID: "custom", API: "completions", BaseURL: upstream.URL}, "key"); err == nil {
				t.Fatal("invalid model catalog accepted")
			}
		})
	}
}

func TestFetchAnthropicModelsRequiresPaginationFlag(t *testing.T) {
	for _, catalog := range []string{`{"data":[{"id":"partial"}]}`, `{"data":[{"id":"partial"}],"has_more":null}`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, catalog) }))
		_, err := fetchProviderModels(t.Context(), provider.Provider{ID: "anthropic", API: "anthropic", BaseURL: upstream.URL}, "key")
		upstream.Close()
		if err == nil || !strings.Contains(err.Error(), "has_more") {
			t.Fatalf("catalog=%s err=%v", catalog, err)
		}
	}
}

func TestFetchDashScopeModelCatalog(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "Bearer dash-key" || r.URL.Query().Get("page_size") != "100" || len(r.URL.Query()["capabilities"]) != 2 {
			t.Errorf("DashScope request = %s, headers=%v", r.URL, r.Header)
		}
		switch r.URL.Query().Get("page_no") {
		case "1":
			_, _ = io.WriteString(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-current","name":"Qwen Current","model_info":{"context_window":131072,"max_output_tokens":16384},"inference_metadata":{"request_modality":["Text","Image"]}}]}}`)
		case "2":
			_, _ = io.WriteString(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-next","name":"Qwen Next"}]}}`)
		default:
			t.Errorf("unexpected page: %s", r.URL)
		}
	}))
	defer upstream.Close()
	endpoint, _ := url.Parse(upstream.URL + "/compatible-mode/v1/models")
	models, err := fetchDashScopeModels(t.Context(), endpoint, nil, "dash-key")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(models) != 2 || models[0].ID != "qwen-current" || models[0].Name != "Qwen Current" || models[0].ContextWindow != 131072 || models[0].MaxTokens != 16384 || !slices.Contains(models[0].Input, "image") {
		t.Fatalf("requests=%d models=%+v", requests, models)
	}
}

func TestRefreshProviderModelsIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		changeAPI bool
		expected  int
	}{
		{"success", http.StatusOK, `{"data":[{"id":"remote/model","name":"Remote model"}]}`, false, http.StatusOK},
		{"upstream error", http.StatusUnauthorized, `secret upstream body`, false, http.StatusBadGateway},
		{"invalid catalog", http.StatusOK, `{"data":[{"id":"duplicate"},{"id":"duplicate"}]}`, false, http.StatusBadGateway},
		{"protocol changed", http.StatusOK, `{"data":[{"id":"stale/model"}]}`, true, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, hs := testServer(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer provider-key" {
					t.Errorf("request: %s headers=%v", r.URL, r.Header)
				}
				if tc.changeAPI {
					if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
						pc := cfg.Providers["catalog-test"]
						pc.API = "responses"
						cfg.Providers["catalog-test"] = pc
						return nil
					}); err != nil {
						t.Errorf("change API: %v", err)
					}
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
				cfg.Providers["catalog-test"] = provider.Config{Name: "Catalog Test", API: "completions", BaseURL: upstream.URL + "/v1", Models: []provider.ModelSeed{{ID: "local", Name: "Local", ContextWindow: 32000, MaxTokens: 4096, Input: []string{"text"}}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := srv.registry.SetCredential("catalog-test", ptrString("provider-key")); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/providers/catalog-test/models/refresh", nil)
			req.Header.Set("Authorization", "Bearer tok")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.expected || bytes.Contains(body, []byte("provider-key")) || bytes.Contains(body, []byte("secret upstream body")) {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			models := findProvider(t, srv.registry.Providers(), "catalog-test").Models
			if tc.expected == http.StatusOK {
				if len(models) != 2 || models[0].ID != "local" || models[1].ID != "remote/model" {
					t.Fatalf("synced models=%+v", models)
				}
			} else if len(models) != 1 || models[0].ID != "local" {
				t.Fatalf("failed refresh changed models=%+v", models)
			}
		})
	}
}

func TestDeletedSyncedModelCanBeRecreatedAsManual(t *testing.T) {
	srv, hs := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"recreated","name":"Upstream"}]}`)
	}))
	defer upstream.Close()
	if err := srv.registry.Update(func(cfg *provider.ModelsFile) error {
		cfg.Providers["recreate-test"] = provider.Config{Name: "Recreate", API: "completions", BaseURL: upstream.URL + "/v1", Models: []provider.ModelSeed{{ID: "anchor"}, {ID: "recreated", Name: "Synced"}}, SyncedModels: []string{"recreated"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.registry.SetCredential("recreate-test", ptrString("key")); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodDelete, "/v1/providers/recreate-test/models?model=recreated", "", http.StatusNoContent},
		{http.MethodPost, "/v1/providers/recreate-test/models", `{"id":"recreated","name":"Manual replacement","contextWindow":65000,"maxTokens":7000,"input":["text"]}`, http.StatusOK},
		{http.MethodPost, "/v1/providers/recreate-test/models/refresh", "", http.StatusOK},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), call.method, hs.URL+call.path, strings.NewReader(call.body))
		req.Header.Set("Authorization", "Bearer tok")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != call.status {
			t.Fatalf("%s %s status=%d body=%s", call.method, call.path, response.StatusCode, body)
		}
	}
	models := findProvider(t, srv.registry.Providers(), "recreate-test").Models
	if len(models) != 2 || models[1].ID != "recreated" || models[1].Name != "Manual replacement" || models[1].ContextWindow != 65000 {
		t.Fatalf("manual replacement changed: %+v", models)
	}
	if slices.Contains(srv.registry.UserConfig().Providers["recreate-test"].SyncedModels, "recreated") {
		t.Fatal("manual replacement retained sync ownership")
	}
}

func TestModelCatalogGETRejectsRedirectAndOversize(t *testing.T) {
	var forwarded bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, strings.Repeat("x", 65))
	}))
	defer upstream.Close()
	for _, path := range []string{"/redirect", "/large"} {
		if _, err := modelCatalogGET(t.Context(), modelCatalogClient(), upstream.URL+path, "completions", "private-key", 64); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if forwarded {
		t.Fatal("catalog credential request followed redirect")
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
