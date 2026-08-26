// Package types is the shared message / usage IR (pi-shaped).
//
// Content: text, image, workspace_file, thinking, toolCall. Image/file Path
// keeps host references out of browser URLs; Data is materialized only at the
// provider boundary. Responses replay metadata (ItemID, ArgumentsRaw,
// ThinkingSignature, ThinkingData, TextSignature, and Message.ResponseID) is
// persisted as opaque provider-owned state. StreamIndex is transient provider
// parsing state and is not persisted. Message roles: user, assistant,
// toolResult.
// This package imports no other internal packages.
package types
