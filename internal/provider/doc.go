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
// images in function_call_output. See docs/provider.md.
package provider
