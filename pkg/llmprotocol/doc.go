// Package llmprotocol provides reusable streaming clients for common LLM
// provider wire protocols. It deliberately contains no model catalog,
// credential store, retry policy, session state, or agent-loop behavior. It
// does classify deterministic client and wire-protocol failures so a caller's
// retry policy can avoid replaying an identical invalid request.
package llmprotocol
