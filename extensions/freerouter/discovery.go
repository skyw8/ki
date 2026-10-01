package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type modelInfo struct {
	ID, Name                 string
	ContextLength, MaxTokens uint64
}

var fastProviderPrefixes = []string{"groq/", "cerebras/", "fireworks/", "together/", "mistralai/"}
var nonAssistantMarkers = []string{"content-safety", "moderation", "guard", "-vl", "/vl", "vision"}

func speedScore(id string) int {
	id = strings.ToLower(id)
	for i, p := range fastProviderPrefixes {
		if strings.HasPrefix(id, p) {
			return i
		}
	}
	return len(fastProviderPrefixes)
}
func isGeneralAssistant(id string) bool {
	id = strings.ToLower(id)
	for _, m := range nonAssistantMarkers {
		if strings.Contains(id, m) {
			return false
		}
	}
	return true
}
func fetchFreeModels(ctx context.Context, client *http.Client, key, baseURL string) ([]modelInfo, error) {
	if key == "" {
		return nil, fmt.Errorf("OpenRouter API key is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL(baseURL)+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "freerouter/0.1")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch OpenRouter models: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("Failed to fetch OpenRouter models: %d %s", res.StatusCode, http.StatusText(res.StatusCode))
	}
	var payload struct {
		Data []struct {
			ID            *string `json:"id"`
			Name          *string `json:"name"`
			ContextLength *uint64 `json:"context_length"`
			TopProvider   *struct {
				MaxTokens *uint64 `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("Invalid models response: %w", err)
	}
	models := []modelInfo{}
	for _, m := range payload.Data {
		if m.ID == nil || !strings.Contains(*m.ID, ":free") || !isGeneralAssistant(*m.ID) {
			continue
		}
		info := modelInfo{ID: *m.ID, Name: *m.ID, ContextLength: 128000, MaxTokens: 4096}
		if m.Name != nil {
			info.Name = *m.Name
		}
		if m.ContextLength != nil {
			info.ContextLength = *m.ContextLength
		}
		if m.TopProvider != nil && m.TopProvider.MaxTokens != nil {
			info.MaxTokens = *m.TopProvider.MaxTokens
		}
		models = append(models, info)
	}
	sort.SliceStable(models, func(i, j int) bool {
		a, b := models[i], models[j]
		if speedScore(a.ID) != speedScore(b.ID) {
			return speedScore(a.ID) < speedScore(b.ID)
		}
		return a.ContextLength < b.ContextLength
	})
	if len(models) == 0 {
		return nil, fmt.Errorf("No free models found on OpenRouter")
	}
	return models, nil
}

type modelDiscovery struct {
	mu       sync.Mutex
	models   []modelInfo
	inflight chan struct{}
	client   *http.Client
}

func (d *modelDiscovery) cached() []modelInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]modelInfo(nil), d.models...)
}
func (d *modelDiscovery) ensure(ctx context.Context, key, baseURL string) ([]modelInfo, error) {
	d.mu.Lock()
	if len(d.models) > 0 {
		m := append([]modelInfo(nil), d.models...)
		d.mu.Unlock()
		return m, nil
	}
	if pending := d.inflight; pending != nil {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending:
		}
		if m := d.cached(); len(m) > 0 {
			return m, nil
		}
		return nil, fmt.Errorf("No free models found on OpenRouter")
	}
	pending := make(chan struct{})
	d.inflight = pending
	d.mu.Unlock()
	models, err := fetchFreeModels(ctx, d.client, key, baseURL)
	d.mu.Lock()
	if err == nil {
		d.models = models
	}
	d.inflight = nil
	close(pending)
	d.mu.Unlock()
	return models, err
}
func (d *modelDiscovery) refresh(ctx context.Context, key, baseURL string) error {
	models, err := fetchFreeModels(ctx, d.client, key, baseURL)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.models = models
	d.mu.Unlock()
	return nil
}
