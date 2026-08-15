// Package compact summarizes older turns when context is full.
//
// Auto: on agent_end when contextTokens > contextWindow - reserveTokens
// (default reserve 16384). Manual: HTTP / CLI compact. Keeps about
// keepRecentTokens (default 20000) and appends a compaction jsonl entry
// (summary, firstKeptEntryId, tokensBefore). The next model call must
// rebuild the layered prompt.
package compact
