// Package mcp adapts the official Go MCP SDK to Ki's ordinary tool dispatcher.
//
// A server-owned Manager connects lazily, caches immutable per-occupy tool
// snapshots, and refreshes after tools/list_changed notifications. Catalog is
// read-only and never starts a process or network connection. Required startup
// failures wrap ErrRequired; optional failures return partial tools and errors.
//
// The SDK owns transport framing, discovery pagination, notifications, call
// cancellation, and stdio process shutdown. Manager closure fences new work,
// cancels and joins owned calls/discovery, then closes all SDK sessions. Individual
// canceled calls do not close shared connections or replay side effects.
// MCP cancellation notification delivery is best effort: joining a local call
// does not claim the host can force remote server code to terminate.
//
// Raw MCP names are retained for protocol calls and allow/deny filtering;
// normalized, bounded model aliases are collision-checked including Ki aliases.
// Input validation uses the SDK's JSON Schema library, with remote references
// rejected rather than fetched. Structured results and content metadata remain
// in Details; payloads live only in Content so post-hook redaction has no shadow
// copy. Text/image/audio are projected directly, other content is inert JSON.
// No resource link, embedded URI, image URL, or filesystem path is auto-fetched.
// SourceInfo exposes only the server identity and bounded instructions for search.
package mcp
