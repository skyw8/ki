// Package llmprotocol provides reusable streaming clients for common LLM
// provider wire protocols. It deliberately contains no model catalog,
// credential store, retry policy, session state, or agent-loop behavior. It
// does classify deterministic client and wire-protocol failures so a caller's
// retry policy can avoid replaying an identical invalid request, and it adapts
// replayed history to each protocol's tool-call model (Responses custom calls
// are downgraded for Completions and Anthropic).
// Client.IdleTimeout bounds blocked response-body reads, counting heartbeat
// bytes and excluding consumer time. NewClient defaults to five minutes; zero
// disables the bound. An idle failure after partial content is non-retryable,
// preserving that partial without silently replaying its answer/tool calls.
package llmprotocol
