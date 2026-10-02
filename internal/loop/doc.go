// Package loop is the agent main loop: emit events, await hooks.
//
// It does not persist sessions, speak HTTP, or assemble prompt files. Subscribers
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
// Compaction start/end events carry lifecycleEntryId, parentId (including an
// explicit empty root), and the persisted timestamp for the same reason.
// Their entryId remains the committed checkpoint identity, not the lifecycle.
//
// Events follow pi names: agent_*, turn_*, message_*, tool_execution_*,
// compaction_start/end (reason + ok), request_header (system + tools snapshot
// after turn_start, before stream), and patch_apply_updated for non-executing
// previews of streamed freeform patch arguments.
// Tool execution start/end events carry Unix-millisecond timestamps and the
// end event carries durationMs; the same duration is persisted on toolResult.
// turn_start carries its start timestamp and turn_end carries the completion
// timestamp plus durationMs, so a consumer can time a turn from the server's
// own boundaries.
// Tools are normally JSON functions; grammar-backed Responses custom tools
// may override ToolSpec and receive raw freeform input. Results follow the tool
// contract (internal/tool/doc.go).
// Config.OutputStore bounds every model-facing tool result at the single
// boundary after the AfterTool hook: oversized text is written to the session
// spill store and replaced by a bounded preview plus a reference in details, so
// built-in, extension, and sidecar tools share one policy.
// Tools run in two phases (pi prepare/execute): synchronous prepare resolves
// the tool, runs the optional tool.Validator schema check (internal/tool), and
// the BeforeTool hook; failures become immediate error results. Then execute
// runs the prepared calls (parallel by default). BeforeTool/tool.Result may
// set Terminate: when every call in a batch terminates, the loop stops.
// Transient provider errors retry up to 5 times with exponential backoff from
// 2s. Deterministic request/protocol errors and context-overflow errors are not
// retried; overflow returns ErrContextOverflow and Run recovers once via
// Hooks.OnContextOverflow (the failed Request is supplied; server returns both
// portable messages and an optional canonical Responses prefix; same Run, so
// events are not replayed). For standalone-only compaction, ShouldCompact and
// OnContextThreshold run after a completed tool batch and replace live history
// before the next provider request; agent_end never triggers compaction.
// Request may also carry context_management for
// provider-managed compaction. stopReason "length" rejects tool calls
// (truncated arguments) instead of executing them.
// Cancellation retains already-emitted partial content in its aborted message.
// RunMessage accepts provider-neutral structured user content. TextOnly
// removes image blocks at the final model-facing boundary.
// Request.TurnID is stable for an entire RunMessage, including retries and tool
// continuations, and is distinct from the per-round turn_start/turn_end events.
// Provider extensions can use it for logical-turn transport affinity.
// Config.Inbox injects extra user messages into the same Run after the
// current stream and tools finish; it does not cancel an in-flight HTTP
// request. Completions, Responses, and Anthropic all see a normal extra user
// message on the next request. CommitUserMessage arbitrates runtime completion
// ownership around persistence; rejected messages never enter model history.
// clientRequestId/completion/ContextOnly are stripped at the provider boundary.
// QueueOnly input is context, so it does not extend a naturally finishing turn;
// explicit work still steers/continues it. Inbox wait checks subscription and
// pending input under one lock to avoid lost wakeups.
// ToolRegistry resolves canonical snake_case and PascalCase/native aliases to
// one tool/schema. Hooks/events/telemetry use canonical names; protocol calls
// and results retain requested spelling. tool.ExecutionIdentity is host-only
// context that attributes a terminal process to its originating call/generation.
// ToolDispatcher snapshots a run's allowed registry and shares the full
// prepare/execute policy with Code Mode callbacks. Nested calls cannot invoke
// exec/wait recursively. They receive the full post-AfterTool intermediate
// result under the caller's RPC budget, while separately bounded audit
// tool_execution_* events carry parentCallId/cellId and a host-assigned call ID.
// These events never create provider toolResult messages. Audit limits cover
// arguments, progress, Details and non-text result payloads, independent of
// model-facing output spool. Emit failures stop nested dispatch; progress
// failures cancel its context. Nested dispatch Go errors are cell-fatal, not
// merely catchable JavaScript rejections. Nested AfterTool failures fail closed.
// Nested BeforeTool/Result.Terminate is returned to the enclosing code runtime,
// which must stop accepting work and propagate the outer terminate signal.
//
// ProcessUpdated/AgentUpdated, QueueChanged, RunAborted, ExtensionError, ExtensionNotice, and
// ExtensionUIPrompt are session sideband notifications. RuntimeReady is
// process-local (not jsonl): one session's open-time Prepare finished,
// success or failure. AgentSettled is post-agent_end wrap-up for lifecycle
// subscribers, not ordinary run SSE. SteerAccepted is live-run only
// (Inbox accepted human input, including extension relays; drain later emits
// message_*). Runtime-authored/context-only inputs have no optimistic acceptance.
// Event catalog:
// docs/events.md. Event order: docs/architecture.md.
// Config.ModelTools optionally separates model-advertised schemas from the
// fixed Config.Tools execution registry. It runs for each request after
// BeforeRun/TransformContext and final content filtering, with the actual
// provider history. Only canonical members of the registry may be advertised;
// hiding a declaration does not revoke a capability or bypass tool policy.
// search_tool, like nested JS calls, fails closed if AfterTool returns an error:
// original discovery content and identities must not bypass output policy.
package loop
