// The deep-web-search executable implements a language-independent Ki sidecar.
// It aggregates independent providers under nested deadlines, returns successful
// partial results, and hydrates content under a separate bounded background pass.
// Source URLs, ranking, tool schemas, settings, and deterministic evidence remain
// extension-owned. WHATWG URL parsing normalizes identities and hosts before
// content safety checks. Cache eviction retains insertion order across restarts.
// Credentials never enter tool results or progress notifications.
// Direct Codex search/summary requests split every advertised "<level> fast"
// selection before encoding effort (including off) and use priority routing.
// They share Codex metadata, durable installation identity and zstd encoding
// with the OAuth provider through pkg/codexclient, without retaining ephemeral
// search sessions. Public OpenAI summaries reject Codex-only Fast choices.
// Versioned private configuration/cache and shared OAuth credentials use
// internal/state; newer documents are never overwritten. Tool schemas are embedded
// so the executable can run without repository source files or an interpreter.
package main
