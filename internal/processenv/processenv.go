package processenv

import (
	"os"
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

// ChildEnvironment returns a copy of Ki's current environment for explicit
// use as an exec.Cmd environment.
func ChildEnvironment() []string {
	return append([]string(nil), os.Environ()...)
}

// ProxyEnvironment returns the proxy variables currently visible to Ki.
func ProxyEnvironment() []string {
	return ProxyEnvironmentFrom(os.Environ())
}

// ProxyEnvironmentFrom filters an environment down to standard proxy keys.
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

// WithProxyEnvironment adds inherited proxy variables unless the child
// environment already defines the same key.
func WithProxyEnvironment(env []string) []string {
	return WithProxyEnvironmentFrom(env, os.Environ())
}

// WithProxyEnvironmentFrom adds proxy variables from source to env unless the
// child environment already defines the same key.
func WithProxyEnvironmentFrom(env, source []string) []string {
	result := append([]string(nil), env...)
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
