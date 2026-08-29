// Package compact summarizes older turns when context is full.
//
// Prepare is a pure cut: it never splits a tool result, virtually expands the
// previous retained tail, and returns ErrNothingToCompact when the recent-token
// budget already covers the conversation. Execute calls the model.
// session.AppendCompaction writes the summary plus retainedTail; old jsonl
// without a tail falls back to firstKeptEntryId. Run is the convenience
// wrapper. Auto: preflight, overflow recovery, and agent_end when tokens >
// contextWindow - reserveTokens (default reserve 16384). Manual: HTTP / CLI
// compact. Keep about keepRecentTokens (default 20000). The next model call
// must rebuild the layered prompt.
package compact
