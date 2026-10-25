package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"ki/internal/provider"
)

const (
	magpieProviderID  = "magpie"
	modelCatalogLimit = 16 << 20
)

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": provider.CatalogVersion, "default": s.registry.Default(), "providers": s.registry.Providers()})
}

func (s *Server) writeProviders(w http.ResponseWriter, r *http.Request) {
	s.publishInvalidation(scopeProviders)
	s.providers(w, r)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func registryError(w http.ResponseWriter, err error) {
	code := http.StatusUnprocessableEntity
	if strings.Contains(err.Error(), "not found") {
		code = http.StatusNotFound
	}
	if strings.Contains(err.Error(), "already exists") {
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
		provider.Config
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	for _, p := range s.registry.Providers() {
		if p.ID == body.ID {
			registryError(w, errProviderAlreadyExists)
			return
		}
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error { cfg.Providers[body.ID] = body.Config; return nil })
	if err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

type providerPatch struct {
	Name    *string `json:"name"`
	API     *string `json:"api"`
	BaseURL *string `json:"baseUrl"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) patchProvider(w http.ResponseWriter, r *http.Request) {
	var body providerPatch
	if !decodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	builtinFound := false
	for _, p := range s.registry.Providers() {
		if p.ID == id && p.Builtin {
			builtinFound = true
			break
		}
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error {
		pc, ok := cfg.Providers[id]
		if !ok && !builtinFound {
			return errProviderNotFound
		}
		if body.Name != nil {
			pc.Name = *body.Name
		}
		if body.API != nil {
			pc.API = *body.API
		}
		if body.BaseURL != nil {
			pc.BaseURL = *body.BaseURL
		}
		if body.Enabled != nil {
			pc.Enabled = body.Enabled
		}
		cfg.Providers[id] = pc
		return nil
	})
	if err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	builtin := false
	for _, p := range s.registry.Providers() {
		if p.ID == id && p.Builtin {
			builtin = true
			break
		}
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error {
		if _, ok := cfg.Providers[id]; !ok {
			if builtin {
				return errProviderNoCustomConfig
			}
			return errProviderNotFound
		}
		delete(cfg.Providers, id)
		return nil
	})
	if err != nil {
		registryError(w, err)
		return
	}
	if builtin {
		s.writeProviders(w, r)
		return
	}
	s.publishInvalidation(scopeProviders)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putProviderCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey json.RawMessage   `json:"apiKey"`
		Type   provider.AuthKind `json:"type"`
		Value  json.RawMessage   `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Value != nil {
		if body.APIKey != nil {
			http.Error(w, "apiKey and value are mutually exclusive", http.StatusBadRequest)
			return
		}
		if bytes.Equal(bytes.TrimSpace(body.Value), []byte("null")) {
			if err := s.registry.SetCredential(r.PathValue("id"), nil); err != nil {
				registryError(w, err)
				return
			}
			s.writeProviders(w, r)
			return
		}
		if body.Type == "" {
			http.Error(w, "type required with value", http.StatusBadRequest)
			return
		}
		if err := s.registry.SetCredentialValue(r.PathValue("id"), body.Type, body.Value); err != nil {
			registryError(w, err)
			return
		}
		s.writeProviders(w, r)
		return
	}
	if body.APIKey == nil {
		http.Error(w, "apiKey or value required", http.StatusBadRequest)
		return
	}
	var key *string
	if !bytes.Equal(bytes.TrimSpace(body.APIKey), []byte("null")) {
		var value string
		if err := json.Unmarshal(body.APIKey, &value); err != nil {
			http.Error(w, "apiKey must be a string or null", http.StatusBadRequest)
			return
		}
		key = &value
	}
	if err := s.registry.SetCredential(r.PathValue("id"), key); err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

func (s *Server) createProviderModel(w http.ResponseWriter, r *http.Request) {
	var seed provider.ModelSeed
	if !decodeJSON(w, r, &seed) {
		return
	}
	id := r.PathValue("id")
	providerFound, modelExists := false, false
	for _, p := range s.registry.Providers() {
		if p.ID == id {
			providerFound = true
			for _, m := range p.Models {
				if m.ID == seed.ID {
					modelExists = true
				}
			}
		}
	}
	if !providerFound {
		registryError(w, errProviderNotFound)
		return
	}
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	if modelExists {
		registryError(w, errModelAlreadyExists)
		return
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers[id]
		pc.Models = append(pc.Models, seed)
		cfg.Providers[id] = pc
		return nil
	})
	if err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

func (s *Server) patchProviderModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
		provider.ModelOverride
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	found := false
	for _, p := range s.registry.Providers() {
		if p.ID == id {
			for _, m := range p.Models {
				if m.ID == body.ID {
					found = true
					break
				}
			}
		}
	}
	if !found {
		registryError(w, errModelNotFound)
		return
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error {
		pc := cfg.Providers[id]
		if pc.ModelOverrides == nil {
			pc.ModelOverrides = map[string]provider.ModelOverride{}
		}
		pc.ModelOverrides[body.ID] = body.ModelOverride
		cfg.Providers[id] = pc
		return nil
	})
	if err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

func (s *Server) deleteProviderModel(w http.ResponseWriter, r *http.Request) {
	id, modelID := r.PathValue("id"), r.URL.Query().Get("model")
	if modelID == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	err := s.registry.Update(func(cfg *provider.ModelsFile) error {
		pc, ok := cfg.Providers[id]
		if !ok {
			return errModelNotFound
		}
		before := len(pc.Models)
		pc.Models = slices.DeleteFunc(pc.Models, func(m provider.ModelSeed) bool { return m.ID == modelID })
		pc.SyncedModels = slices.DeleteFunc(pc.SyncedModels, func(id string) bool { return id == modelID })
		_, hadOverride := pc.ModelOverrides[modelID]
		delete(pc.ModelOverrides, modelID)
		if len(pc.Models) == before && !hadOverride {
			return errModelNotFound
		}
		cfg.Providers[id] = pc
		return nil
	})
	if err != nil {
		registryError(w, err)
		return
	}
	s.publishInvalidation(scopeProviders)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshProviderModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.registry.ExtensionProvider(id) {
		registryError(w, errProviderReadOnly)
		return
	}
	current, credential, status, err := s.registry.ProviderModelCatalogCredential(id)
	if err != nil {
		registryError(w, err)
		return
	}
	if !status.Configured || credential.APIKey == "" {
		http.Error(w, "configure an API key before fetching models", http.StatusUnprocessableEntity)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := fetchProviderModels(ctx, current, credential.APIKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	err = s.registry.SyncModelsIfCurrent(id, current.API, current.BaseURL, credential.APIKey, models)
	if err != nil {
		if errors.Is(err, provider.ErrProviderConfigChanged) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}

func fetchProviderModels(ctx context.Context, current provider.Provider, apiKey string) ([]provider.ModelSeed, error) {
	if current.ID == magpieProviderID {
		return fetchMagpieModels(ctx, current.BaseURL, apiKey)
	}
	endpoint, err := providerModelsURL(current.BaseURL, current.API)
	if err != nil {
		return nil, err
	}
	if dashScopeCatalog(endpoint) {
		return fetchDashScopeModels(ctx, endpoint, current.Models, apiKey)
	}
	client := modelCatalogClient()
	entries := make([]json.RawMessage, 0)
	seenCursors := map[string]bool{}
	remaining := int64(modelCatalogLimit)
	for page := 0; page < 100; page++ {
		raw, err := modelCatalogGET(ctx, client, endpoint.String(), current.API, apiKey, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(raw))
		var list struct {
			Data    json.RawMessage `json:"data"`
			HasMore *bool           `json:"has_more"`
			LastID  string          `json:"last_id"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode model catalog: %w", err)
		}
		if len(list.Data) == 0 || bytes.Equal(bytes.TrimSpace(list.Data), []byte("null")) {
			return nil, fmt.Errorf("model catalog is missing data")
		}
		var items []json.RawMessage
		if err := json.Unmarshal(list.Data, &items); err != nil {
			return nil, fmt.Errorf("decode model catalog data: %w", err)
		}
		if current.API == "anthropic" && list.HasMore == nil {
			return nil, fmt.Errorf("Anthropic model catalog is missing has_more")
		}
		entries = append(entries, items...)
		if list.HasMore == nil || !*list.HasMore {
			catalog, err := json.Marshal(struct {
				Data []json.RawMessage `json:"data"`
			}{Data: entries})
			if err != nil {
				return nil, err
			}
			return provider.ParseProviderModels(catalog, current.Models)
		}
		cursor := list.LastID
		if cursor == "" && len(items) > 0 {
			var last struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(items[len(items)-1], &last); err != nil {
				return nil, fmt.Errorf("decode model catalog cursor: %w", err)
			}
			cursor = last.ID
		}
		if len(items) == 0 || cursor == "" || seenCursors[cursor] {
			return nil, fmt.Errorf("model catalog pagination did not advance")
		}
		seenCursors[cursor] = true
		query := endpoint.Query()
		if current.API == "anthropic" {
			query.Set("after_id", cursor)
		} else {
			query.Set("after", cursor)
		}
		endpoint.RawQuery = query.Encode()
	}
	return nil, fmt.Errorf("model catalog exceeds 100 pages")
}

func dashScopeCatalog(endpoint *url.URL) bool {
	host := strings.ToLower(endpoint.Hostname())
	official := host == "dashscope.aliyuncs.com" || host == "dashscope-intl.aliyuncs.com" || host == "dashscope-us.aliyuncs.com" || host == "cn-hongkong.dashscope.aliyuncs.com" || strings.HasSuffix(host, ".maas.aliyuncs.com")
	return official && (strings.HasSuffix(endpoint.Path, "/compatible-mode/v1/models") || endpoint.Path == "/api/v1/models")
}

func providerModelsURL(baseURL, api string) (*url.URL, error) {
	base, err := url.Parse(baseURL)
	if err != nil || !base.IsAbs() || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid provider base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	switch api {
	case "completions", "responses":
		base.Path = path + "/models"
	case "anthropic":
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		base.Path = path + "/models"
	default:
		return nil, fmt.Errorf("model discovery is unavailable for API protocol %q", api)
	}
	base.RawPath = ""
	return base, nil
}

func fetchDashScopeModels(ctx context.Context, endpoint *url.URL, existing []provider.Model, apiKey string) ([]provider.ModelSeed, error) {
	endpoint.Path = "/api/v1/models"
	endpoint.RawPath = ""
	client := modelCatalogClient()
	remaining := int64(modelCatalogLimit)
	models := make([]json.RawMessage, 0)
	for page := 1; page <= 100; page++ {
		query := endpoint.Query()
		query.Set("page_no", fmt.Sprint(page))
		query.Set("page_size", "100")
		query.Del("capabilities")
		query.Add("capabilities", "TG")
		query.Add("capabilities", "Reasoning")
		endpoint.RawQuery = query.Encode()
		raw, err := modelCatalogGET(ctx, client, endpoint.String(), "completions", apiKey, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(raw))
		var result struct {
			Success *bool `json:"success"`
			Output  *struct {
				Total  *int `json:"total"`
				Models []struct {
					ID   string `json:"model"`
					Name string `json:"name"`
					Info struct {
						ContextWindow int `json:"context_window"`
						MaxOutput     int `json:"max_output_tokens"`
					} `json:"model_info"`
					Inference struct {
						Input []string `json:"request_modality"`
					} `json:"inference_metadata"`
				} `json:"models"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode DashScope model catalog: %w", err)
		}
		if result.Success != nil && !*result.Success || result.Output == nil || result.Output.Total == nil || *result.Output.Total < 0 || result.Output.Models == nil {
			return nil, fmt.Errorf("invalid DashScope model catalog")
		}
		for _, item := range result.Output.Models {
			for i := range item.Inference.Input {
				item.Inference.Input[i] = strings.ToLower(item.Inference.Input[i])
			}
			mapped := struct {
				ID            string   `json:"id"`
				Name          string   `json:"name,omitempty"`
				ContextWindow int      `json:"context_window,omitempty"`
				MaxOutput     int      `json:"max_output_tokens,omitempty"`
				Input         []string `json:"input_modalities,omitempty"`
			}{item.ID, item.Name, item.Info.ContextWindow, item.Info.MaxOutput, item.Inference.Input}
			model, err := json.Marshal(mapped)
			if err != nil {
				return nil, err
			}
			models = append(models, model)
		}
		if len(models) >= *result.Output.Total {
			catalog, err := json.Marshal(struct {
				Data []json.RawMessage `json:"data"`
			}{models})
			if err != nil {
				return nil, err
			}
			return provider.ParseProviderModels(catalog, existing)
		}
		if len(result.Output.Models) == 0 {
			return nil, fmt.Errorf("DashScope model catalog pagination did not advance")
		}
	}
	return nil, fmt.Errorf("DashScope model catalog exceeds 100 pages")
}

func modelCatalogClient() *http.Client {
	return &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func fetchMagpieModels(ctx context.Context, baseURL, apiKey string) ([]provider.ModelSeed, error) {
	base, err := url.Parse(baseURL)
	if err != nil || !base.IsAbs() || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid Magpie base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path = strings.TrimSuffix(path, "/v1")
	}
	modelsURL := *base
	modelsURL.Path = path + "/v1/models"
	modelsURL.RawPath = ""
	helloURL := *base
	helloURL.Path = path + "/api/hello"
	helloURL.RawPath = ""

	client := modelCatalogClient()
	hello, err := modelCatalogGET(ctx, client, helloURL.String(), "completions", apiKey, 64<<10)
	if err != nil {
		return nil, err
	}
	var identity struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(hello, &identity); err != nil || !strings.EqualFold(identity.Name, "magpie") {
		return nil, fmt.Errorf("configured gateway did not identify itself as Magpie")
	}
	raw, err := modelCatalogGET(ctx, client, modelsURL.String(), "completions", apiKey, modelCatalogLimit)
	if err != nil {
		return nil, err
	}
	models, err := provider.ParseMagpieModels(raw)
	if err != nil {
		return nil, err
	}
	return models, nil
}

func modelCatalogGET(ctx context.Context, client *http.Client, endpoint, api, apiKey string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create model catalog request: %w", err)
	}
	if api == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model catalog response: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("model catalog response exceeds %d bytes", maxBytes)
	}
	return raw, nil
}

func (s *Server) putDefaultModel(w http.ResponseWriter, r *http.Request) {
	var ref provider.ModelRef
	if !decodeJSON(w, r, &ref) {
		return
	}
	if _, _, ok := s.registry.FindModel(ref.Provider, ref.Model); !ok {
		http.Error(w, fmt.Sprintf("model %q/%q is unavailable", ref.Provider, ref.Model), http.StatusUnprocessableEntity)
		return
	}
	if err := s.registry.RememberDefault(ref); err != nil {
		registryError(w, err)
		return
	}
	s.writeProviders(w, r)
}
