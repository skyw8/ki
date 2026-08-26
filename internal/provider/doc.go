// Package provider contains Ki's model catalog, registry, credential and
// extension-runtime contracts. The reusable OpenAI Completions, OpenAI
// Responses, and Anthropic Messages wire clients live in pkg/llmprotocol;
// this package adapts them to internal/loop and internal/types.
//
// Request shapes must not be mixed. Responses input uses paired
// function_call/function_call_output or custom_tool_call/custom_tool_call_output
// items — never Completions role:tool, or the next turn after a tool result is
// HTTP 400. Streamed function arguments are accumulated as JSON; custom tool
// input is accumulated as raw text, while Responses raw deltas are also
// surfaced to the loop for non-executing argument previews. Responses replay
// is stateless: encrypted reasoning items are requested with
// reasoning.encrypted_content, and a stream is successful only after a
// terminal response event.
//
// Completions tool images: consecutive toolResults stay adjacent; one
// follow-up user carries that group's images (pi). Responses embed
// images in function_call_output.
// Completions follows the Chat Completions SSE shape: content/refusal and
// indexed tool-call deltas are accumulated, the deprecated single
// function_call shape remains readable for compatible gateways, and the
// provider's prompt_tokens/cache detail counters are preserved until cost
// normalization. Anthropic follows the Messages event lifecycle and requires
// indexed content blocks, message_stop, valid tool JSON objects, and replayable
// thinking signatures/redacted-thinking data.
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
// falls back to the first available model. Extension provider defaultModel is
// only a preference; if it is stale or disabled, the first enabled model is
// used. DefaultThinking is the
// per-model fallback (prefer medium) when effort is omitted; ClampThinking
// maps an unsupported level onto the nearest remaining one.
// See docs/provider.md.
package provider
