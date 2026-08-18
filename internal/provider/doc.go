// Package provider is the model IR adapters for three protocols:
// OpenAI Completions, OpenAI Responses, and Anthropic Messages.
//
// Request shapes must not be mixed. Responses input uses function_call and
// function_call_output items — never Completions role:tool, or the next
// turn after a tool result is HTTP 400.
// Streamed tool arguments are concatenated as raw JSON then Unmarshal'd.
//
// Completions tool images: consecutive toolResults stay adjacent; one
// follow-up user carries that group's images (pi). Responses embed
// images in function_call_output.
//
// Replay skips aborted/error/empty assistants (and their toolResults)
// and synthesizes missing tool results. Session jsonl is unchanged.
// Registry merges the embedded offline catalog with {KI_HOME}/models.json;
// credentials.json and provider environment variables supply secrets. There
// is deliberately no remote model-list refresh.
// See docs/provider.md.
package provider
