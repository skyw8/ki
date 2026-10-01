# Extension replies lost when stdin closes

During the extension binary migration, a source-free smoke check piped one
`initialize` request into an executable and closed stdin. The process sometimes
exited without writing its reply. Normal host tests kept stdin open until
shutdown, so they did not expose this ordering problem.

The Rust sidecar, shared Go transport, and Telegram transport dispatched requests
to asynchronous workers. Their readers treated EOF as permission to return from
the executable immediately, even when a dispatched worker had not yet serialized
its response. Waiting indefinitely would create a different failure: a worker
could await a host callback after the host had disconnected, or block on stdout.

The transports now close pending reverse calls and cancel outstanding work on
EOF, then allow a bounded drain for request replies. Quick handlers can flush
before the process exits; an unresponsive handler or output pipe cannot keep the
sidecar alive. Request tracking covers response serialization, not just handler
completion. Lifecycle notification ordering remains unchanged.

Regression checks launch every distributable executable without source files or
language tools, send initialization, and close stdin before reading its reply.
Transport tests also cover cancellation, pending host callbacks, stalled
handlers, and blocked stdout. The Rust live test additionally builds and queries
a semantic index from a copied executable with empty PATH and library variables,
and verifies that its native library is loaded from the extracted runtime cache.
