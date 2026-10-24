package processenv

import (
	"os"
	"slices"
	"strings"
)

var proxyKeys = map[string]struct{}{
	"ALL_PROXY":   {},
	"FTP_PROXY":   {},
	"HTTP_PROXY":  {},
	"HTTPS_PROXY": {},
	"NO_PROXY":    {},
}

func isProxyKey(key string) bool {
	_, ok := proxyKeys[strings.ToUpper(key)]
	return ok
}

func ChildEnvironment() []string {
	return slices.Clone(os.Environ())
}

func ProxyEnvironment() []string {
	return ProxyEnvironmentFrom(os.Environ())
}

func ProxyEnvironmentFrom(env []string) []string {
	result := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if isProxyKey(key) {
			result = append(result, item)
		}
	}
	return result
}

func WithProxyEnvironment(env []string) []string {
	return WithProxyEnvironmentFrom(env, os.Environ())
}

func WithProxyEnvironmentFrom(env, source []string) []string {
	result := slices.Clone(env)
	present := make(map[string]struct{}, len(result))
	for _, item := range result {
		key, _, ok := strings.Cut(item, "=")
		if ok && isProxyKey(key) {
			present[strings.ToUpper(key)] = struct{}{}
		}
	}
	for _, item := range ProxyEnvironmentFrom(source) {
		key, _, _ := strings.Cut(item, "=")
		canonical := strings.ToUpper(key)
		if _, ok := present[canonical]; ok {
			continue
		}
		result = append(result, item)
		present[key] = struct{}{}
	}
	return result
}
