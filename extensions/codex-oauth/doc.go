// Codex OAuth is a standalone Ki extension speaking NDJSON RPC over stdin and
// stdout. Browser and device authorization, token refresh, Responses streams,
// and Remote Compaction V2 use embedded provider metadata. Generation prefers
// reusable Responses WebSockets with safe HTTP fallback; standalone compaction
// uses the Codex HTTP protocol.
// Thinking selections pair every supported base with a Fast variant. The
// request builder splits these client IDs before mapping effort, including off,
// and encodes Fast as service_tier:"priority", not as reasoning effort.
// Common Codex identity, metadata and routing headers come from pkg/codexclient;
// installation identity is versioned durable state, while turn/window routing
// state and connection pools are bounded process-local data.
//
// Provider item IDs correlate SSE slots; output indexes and call IDs are
// fallback identities. Reasoning signatures exclude tool fields, and opaque
// compaction output remains complete and ordered throughout replay. Independent
// request contexts make cancellation local to its authentication or response.
// Request metadata replaces only a copied top-level map; nested history and
// tool schemas remain read-only during serialization, avoiding redundant copies.
//
// Embedded model costs match OpenAI Standard USD rates, including cache writes
// and long-context tiers. They estimate API-equivalent usage; OAuth subscription
// limits and purchased Codex credits are not dollar charges computed by Ki.
package main
