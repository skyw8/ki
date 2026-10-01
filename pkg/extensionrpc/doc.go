// Package extensionrpc implements the bidirectional NDJSON JSON-RPC transport
// used by bundled Go extension executables. It has no dependency on the host's
// runtime: extensions written in any language can implement the same protocol.
// Calls use a sidecar-specific ID namespace, writes are serialized, notifications
// preserve arrival order, and the reader stays available while handlers call
// back into the host. EOF cancels outstanding calls and requests immediately,
// then allows up to one second for request replies to flush before returning.
package extensionrpc
