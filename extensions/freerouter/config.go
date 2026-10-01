package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ki/internal/state"
)

const defaultListen = "127.0.0.1:18427"
const defaultBaseURL = "https://openrouter.ai/api/v1"

type config struct {
	APIKey              string `json:"apiKey"`
	BaseURL             string `json:"baseUrl"`
	Listen              string `json:"listen"`
	RaceWidth           uint64 `json:"raceWidth"`
	MaxBatches          uint64 `json:"maxBatches"`
	ExhaustedTTLMS      uint64 `json:"exhaustedTtlMs"`
	SlowTTLMS           uint64 `json:"slowTtlMs"`
	FirstTokenTimeoutMS uint64 `json:"firstTokenTimeoutMs"`
	IdleTimeoutMS       uint64 `json:"idleTimeoutMs"`
	RefreshIntervalMS   uint64 `json:"refreshIntervalMs"`
}

func defaultConfig() config {
	return config{BaseURL: defaultBaseURL, Listen: defaultListen, RaceWidth: 2, MaxBatches: 3, ExhaustedTTLMS: 90000, SlowTTLMS: 15000, FirstTokenTimeoutMS: 10000, IdleTimeoutMS: 30000, RefreshIntervalMS: 3600000}
}
func clampOr(n, fallback, lo, hi uint64) uint64 {
	if n == 0 {
		return fallback
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func envAPIKey() string {
	value, ok := os.LookupEnv("OPENROUTER_API_KEY")
	if !ok {
		value = os.Getenv("FREEROUTER_API_KEY")
	}
	return strings.TrimSpace(value)
}
func cleanURL(s string) string  { return strings.TrimRight(strings.TrimSpace(s), "/") }
func envUint(key string) uint64 { n, _ := strconv.ParseUint(os.Getenv(key), 10, 64); return n }
func loadStandalone(listen, baseURL string) config {
	c := defaultConfig()
	c.APIKey = envAPIKey()
	c.RaceWidth = clampOr(envUint("FREEROUTER_RACE_WIDTH"), c.RaceWidth, 1, 8)
	c.MaxBatches = clampOr(envUint("FREEROUTER_MAX_BATCHES"), c.MaxBatches, 1, 6)
	if u := cleanURL(os.Getenv("OPENROUTER_BASE_URL")); u != "" {
		c.BaseURL = u
	}
	if l := strings.TrimSpace(os.Getenv("FREEROUTER_LISTEN")); l != "" {
		c.Listen = l
	}
	if u := cleanURL(baseURL); u != "" {
		c.BaseURL = u
	}
	if l := strings.TrimSpace(listen); l != "" {
		c.Listen = l
	}
	return c
}
func mergeConfig(c *config, raw config) {
	if raw.APIKey != "" {
		c.APIKey = raw.APIKey
	}
	if u := cleanURL(raw.BaseURL); u != "" {
		c.BaseURL = u
	}
	if l := strings.TrimSpace(raw.Listen); l != "" {
		c.Listen = l
	}
	c.RaceWidth = clampOr(raw.RaceWidth, c.RaceWidth, 1, 8)
	c.MaxBatches = clampOr(raw.MaxBatches, c.MaxBatches, 1, 6)
	c.ExhaustedTTLMS = clampOr(raw.ExhaustedTTLMS, c.ExhaustedTTLMS, 1000, 30*60000)
	c.SlowTTLMS = clampOr(raw.SlowTTLMS, c.SlowTTLMS, 1000, 10*60000)
	c.FirstTokenTimeoutMS = clampOr(raw.FirstTokenTimeoutMS, c.FirstTokenTimeoutMS, 1000, 5*60000)
	c.IdleTimeoutMS = clampOr(raw.IdleTimeoutMS, c.IdleTimeoutMS, 1000, 10*60000)
	c.RefreshIntervalMS = clampOr(raw.RefreshIntervalMS, c.RefreshIntervalMS, 60000, 24*60*60000)
}
func loadSidecar(root string) config {
	c := defaultConfig()
	if b, _, err := state.ReadFile(filepath.Join(root, "config.json"), 1, nil); err == nil {
		var raw config
		if json.Unmarshal(b, &raw) == nil {
			mergeConfig(&c, raw)
		}
	}
	if c.APIKey == "" {
		c.APIKey = envAPIKey()
	}
	if u := cleanURL(os.Getenv("OPENROUTER_BASE_URL")); u != "" && (c.BaseURL == defaultBaseURL || c.BaseURL == "") {
		c.BaseURL = u
	}
	if l := strings.TrimSpace(os.Getenv("FREEROUTER_LISTEN")); l != "" {
		c.Listen = l
	}
	if _, err := strconv.ParseUint(os.Getenv("FREEROUTER_RACE_WIDTH"), 10, 64); err == nil {
		c.RaceWidth = clampOr(envUint("FREEROUTER_RACE_WIDTH"), 2, 1, 8)
	}
	return c
}
func resolveAPIKey(configKey, credential string) string {
	if k := strings.TrimSpace(credential); k != "" {
		return k
	}
	if k := strings.TrimSpace(configKey); k != "" {
		return k
	}
	return envAPIKey()
}
