// Package types is the shared message / usage IR (pi-shaped).
//
// Content: text, image, file, workspace_file, thinking, toolCall. Image/file
// Path keeps host references out of browser URLs; Data is materialized only at
// the provider boundary. Responses replay metadata (ItemID, ArgumentsRaw,
// ThinkingSignature, ThinkingData, TextSignature, and Message.ResponseID) is
// persisted as opaque provider-owned state. ModelContext separates portable
// messages from a binding-scoped canonical Responses prefix. ResponsesItems is
// transient server-side compaction output promoted to a session checkpoint
// before message persistence or client delivery. StreamIndex is transient
// provider parsing state and is not persisted. Message roles: user, assistant,
// toolResult. Tool results persist their completion timestamp and durationMs
// for diagnostics and UI replay.
// This package imports no other internal packages.
// Message clientRequestId and completion identity are transport metadata,
// persisted for reconciliation but excluded from provider prompts.
package types
