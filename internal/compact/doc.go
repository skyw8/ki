// Package compact creates portable local summaries when context is full.
//
// Prepare is a pure cut: it never splits a tool result, virtually expands the
// previous retained tail, and returns ErrNothingToCompact when the recent-token
// budget already covers the conversation. Execute calls the model.
// Remote Responses checkpoints are orchestrated by the server and stored by
// session.AppendResponsesCompaction; Prepare deliberately ignores them so a
// model switch or remote failure can summarize the durable raw transcript.
// session.AppendCompaction writes the local summary plus retainedTail; old jsonl
// without a tail falls back to firstKeptEntryId. Run is the convenience
// wrapper. Auto: preflight, overflow recovery, and agent_end when tokens >
// contextWindow - reserveTokens (default reserve 16384). Manual: HTTP / CLI
// compact. Keep about keepRecentTokens (default 20000). The next model call
// must rebuild the layered prompt.
package compact
