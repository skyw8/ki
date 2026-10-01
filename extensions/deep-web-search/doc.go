// The deep-web-search executable implements a language-independent Ki sidecar.
// It aggregates independent providers under nested deadlines, returns successful
// partial results, and hydrates content under a separate bounded background pass.
// Source URLs, ranking, tool schemas, settings, and deterministic evidence remain
// extension-owned. WHATWG URL parsing normalizes identities and hosts before
// content safety checks. Cache eviction retains insertion order across restarts.
// Credentials never enter tool results or progress notifications.
// Versioned private configuration/cache and shared OAuth credentials use
// internal/state; newer documents are never overwritten. Tool schemas are embedded
// so the executable can run without repository source files or an interpreter.
package main
