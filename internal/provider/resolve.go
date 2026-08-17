package provider

import (
	"ki/internal/config"
	"strings"
)

// Resolve picks provider/model following pi: session/request → toml defaults → default table → first with key.
func Resolve(cfg config.Config, sessionProvider, sessionModel, requestModel string) (provider, model string) {
	if requestModel != "" {
		p, m := splitModel(requestModel, sessionProvider, cfg.Defaults.Provider)
		return p, m
	}
	if sessionProvider != "" && sessionModel != "" {
		return sessionProvider, sessionModel
	}
	if cfg.Defaults.Provider != "" && cfg.Defaults.Model != "" && cfg.HasKey(cfg.Defaults.Provider) {
		return cfg.Defaults.Provider, cfg.Defaults.Model
	}
	for _, p := range ProviderOrder {
		if cfg.HasKey(p) {
			return p, DefaultModel[p]
		}
	}
	if sessionProvider != "" {
		return sessionProvider, sessionModel
	}
	if cfg.Defaults.Provider != "" && cfg.Defaults.Model != "" {
		return cfg.Defaults.Provider, cfg.Defaults.Model
	}
	return ProviderOrder[0], DefaultModel[ProviderOrder[0]]
}

func splitModel(spec, fallbackP, defaultP string) (string, string) {
	// strings.Cut is assembly-optimized (SIMD) and clearer than a hand-rolled
	// byte scan; split at the first '/' only, model names may contain more.
	if p, m, ok := strings.Cut(spec, "/"); ok {
		return p, m
	}
	if fallbackP != "" {
		return fallbackP, spec
	}
	if defaultP != "" {
		return defaultP, spec
	}
	return "", spec
}
