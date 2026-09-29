// Package loop is the agent main loop: emit events, await hooks.
//
// It does not write disk, speak HTTP, or assemble prompt files. Subscribers
// (persist, SSE) are attached by the server via emit.
// MessageEncoder/MessageDecoder project canonical message updates onto a
// connection-local snapshot/patch SSE stream. Patches name a message stream
// and the previous message frame's seq; global seq gaps are valid. Persistence
// and extension subscribers still receive full canonical messages.
// Encoding shares immutable typed strings; mutable argument/detail values are
// snapshotted before becoming a patch base. BufferedAt/BufferedBytes are local
// server telemetry/retention metadata, never part of wire or persistence.
//
// Persisted message_end events carry entryId plus parentId, including an
// explicit empty root parent, so sparse/replayed clients never infer a parent
// from a leaf that may already include that message.
//
// Events follow pi names: agent_*, turn_*, message_*, tool_execution_*,
// compaction_start/end (reason + ok), plus request_header (system + tools
// snapshot after turn_start, before stream).
// Tool execution start/end events carry Unix-millisecond timestamps and the
// end event carries durationMs; the same duration is persisted on toolResult.
// turn_start carries its start timestamp and turn_end carries the completion
// timestamp plus durationMs, so a consumer can time a turn from the server's
// own boundaries.
// Every built-in tool is a JSON function; Results follow the tool contract
// (internal/tools/doc.go).
// Config.OutputStore bounds every model-facing tool result at the single
// boundary after the AfterTool hook: oversized text is written to the session
// spill store and replaced by a bounded preview plus a reference in details, so
// built-in, extension, and sidecar tools share one policy.
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
// Cancellation retains already-emitted partial content in its aborted message.
// RunMessage accepts provider-neutral structured user content. TextOnly
// removes image blocks at the final model-facing boundary.
// Config.Inbox injects extra user messages into the same Run after the
// current stream and tools finish; it does not cancel an in-flight HTTP
// request. Completions, Responses, and Anthropic all see a normal extra user
// message on the next request.
//
// QueueChanged, RunAborted, ExtensionError, ExtensionNotice, and
// ExtensionUIPrompt are session sideband notifications. RuntimeReady is
// process-local (not jsonl): one session's open-time Prepare finished,
// success or failure. AgentSettled is post-agent_end wrap-up for lifecycle
// subscribers, not ordinary run SSE. SteerAccepted is live-run only
// (Inbox accepted a user; drain later emits message_*). Event catalog:
// docs/events.md. Event order: docs/architecture.md.
package loop
