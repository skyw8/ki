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
package main
