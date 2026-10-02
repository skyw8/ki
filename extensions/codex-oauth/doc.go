// Codex OAuth is a standalone Ki extension speaking NDJSON RPC over stdin and
// stdout. Browser and device authorization, token refresh, Responses streams,
// and Remote Compaction V2 use only HTTP and embedded provider metadata.
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
