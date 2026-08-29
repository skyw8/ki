// Package loop is the agent main loop: emit events, await hooks.
//
// It does not write disk, speak HTTP, or assemble prompt files. Subscribers
// (persist, SSE) are attached by the server via emit.
//
// Events follow pi names: agent_*, turn_*, message_*, tool_execution_*,
// compaction_start/end (reason + ok), plus request_header (system + tools
// snapshot after turn_start, before stream) and patch_apply_updated for
// syntax-only previews of streamed apply_patch arguments.
// Tool execution start/end events carry Unix-millisecond timestamps and the
// end event carries durationMs; the same duration is persisted on toolResult.
// Tool specs default to JSON functions; ToolSpecProvider and FreeformTool add
// grammar-backed custom tools without changing existing function executors.
// Tools run in two phases (pi prepare/execute): synchronous prepare resolves
// the tool, runs the optional ToolValidator schema check (validate.go), and
// the BeforeTool hook; failures become immediate error results. Then execute
// runs the prepared calls (parallel by default). BeforeTool/ToolResult may
// set Terminate: when every call in a batch terminates, the loop stops.
// Transient provider errors retry up to 5 times with exponential backoff from
// 2s. Deterministic request/protocol errors and context-overflow errors are not
// retried; overflow returns ErrContextOverflow and Run recovers once via
// Hooks.OnContextOverflow (server compacts and returns the new context; same
// Run, so events are not replayed). stopReason "length" rejects tool calls
// (truncated arguments) instead of executing them.
// RunMessage accepts provider-neutral structured user content. TextOnly
// removes image blocks at the final model-facing boundary.
// Config.Inbox injects extra user messages into the same Run after the
// current stream and tools finish; it does not cancel an in-flight HTTP
// request. Completions, Responses, and Anthropic all see a normal extra user
// message on the next request.
//
// QueueChanged, RunAborted, ExtensionError, and RuntimeReady are session
// sideband notifications. RuntimeReady is process-local (not jsonl): one
// session's open-time Prepare finished, success or failure.
// SteerAccepted is live-run only (Inbox accepted a user; drain later emits
// message_*). Event order: docs/architecture.md.
package loop
