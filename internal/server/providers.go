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
	magpieProviderID = "magpie"
	magpieMaxBody    = 4 << 20
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
	if id != magpieProviderID {
		registryError(w, errProviderNotFound)
		return
	}
	current, credential, status, err := s.registry.ProviderCredential(id)
	if err != nil {
		registryError(w, err)
		return
	}
	if !status.Configured || credential.APIKey == "" {
		http.Error(w, "configure a Magpie gateway API key before refreshing models", http.StatusUnprocessableEntity)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := fetchMagpieModels(ctx, current.BaseURL, credential.APIKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	err = s.registry.SyncModelsIfCurrent(id, current.BaseURL, credential.APIKey, models)
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

	client := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	hello, err := magpieGET(ctx, client, helloURL.String(), apiKey, 64<<10)
	if err != nil {
		return nil, err
	}
	var identity struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(hello, &identity); err != nil || !strings.EqualFold(identity.Name, "magpie") {
		return nil, fmt.Errorf("configured gateway did not identify itself as Magpie")
	}
	raw, err := magpieGET(ctx, client, modelsURL.String(), apiKey, magpieMaxBody)
	if err != nil {
		return nil, err
	}
	models, err := provider.ParseMagpieModels(raw)
	if err != nil {
		return nil, err
	}
	return models, nil
}

func magpieGET(ctx context.Context, client *http.Client, endpoint, apiKey string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create Magpie request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Magpie model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Magpie returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Magpie response: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("Magpie response exceeds %d bytes", maxBytes)
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
