// Package provider contains model IR adapters for three built-in protocols:
// OpenAI Completions, OpenAI Responses, and Anthropic Messages, plus the
// runtime/catalog contracts used by provider extensions.
//
// Request shapes must not be mixed. Responses input uses paired
// function_call/function_call_output or custom_tool_call/custom_tool_call_output
// items — never Completions role:tool, or the next turn after a tool result is
// HTTP 400. Streamed function arguments are accumulated as JSON; custom tool
// input is accumulated as raw text, while Responses raw deltas are also
// surfaced to the loop for non-executing argument previews.
//
// Completions tool images: consecutive toolResults stay adjacent; one
// follow-up user carries that group's images (pi). Responses embed
// images in function_call_output.
//
// Provider-capable extensions register an offline model/auth descriptor and
// execute custom streamers in a process-level sidecar; the Registry only
// resolves catalog entries and opaque credentials, while the host adapter
// reconstructs loop deltas from compact provider events.
// Auth login, manual-code input, cancellation, and refresh are private RPCs;
// the HTTP server exposes only redacted auth status and persists the returned
// credential atomically. Responses replay metadata is carried by types.Content
// and types.Message so provider runtimes can restore opaque item state.
//
// Replay skips aborted/error/empty assistants (and their toolResults)
// and synthesizes missing tool results while preserving their call kind.
// Registry merges the embedded offline catalog with {KI_HOME}/models.json;
// credentials.json and provider environment variables supply secrets. There
// is deliberately no remote model-list refresh. models.json default is
// last-used, not a pinned setting: if it is missing or disabled, Default
// falls back to the first available model. DefaultThinking is the
// per-model fallback (prefer medium) when effort is omitted; ClampThinking
// maps an unsupported level onto the nearest remaining one.
// See docs/provider.md.
package provider
