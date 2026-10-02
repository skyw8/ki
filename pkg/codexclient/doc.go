// Package codexclient prepares Codex Responses client identity and routing
// metadata shared by provider and direct-search clients. Identity is private
// versioned Ki state; it makes no claim of device attestation or indistinguishable
// transport fingerprints.
// Session metadata is scoped by provider binding and logical turn, with bounded
// retention. Ad-hoc requests retain no session state; successful checkpoints
// advance windows. Prepare returns auth-independent headers; Encode and Compress
// implement routing-first JSON and Codex HTTP zstd request bodies.
package codexclient
