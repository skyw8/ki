// Package loop is the agent main loop: emit events, await hooks.
//
// It does not write disk, speak HTTP, or assemble prompt files. Subscribers
// (persist, SSE) are attached by the server via emit.
//
// Events follow pi names: agent_*, turn_*, message_*, tool_execution_*,
// compaction_start/end (reason + ok), plus request_header (system + tools
// snapshot after turn_start, before stream).
// BeforeTool errors fail closed (block the tool). Tools default to parallel.
// Provider errors retry up to 5 times with exponential backoff from 2s, except
// context-overflow errors (IsContextOverflow, patterns in overflow.go): those
// return ErrContextOverflow without retry, and Run recovers once via
// Hooks.OnContextOverflow (server compacts and returns the new context; same
// Run, so events are not replayed). stopReason "length" rejects tool calls
// (truncated arguments) instead of executing them.
//
// Event order: docs/architecture.md.
package loop
