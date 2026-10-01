# Cancelled freerouter streams reported as stalled

The full extension migration regression timed out while waiting for a provider
stream cancellation event. The same unchanged sidecar test reproduced twice in
200 focused runs, so this was an ordering bug rather than an insufficient test
deadline.

Cancelling the request both closes its context and unblocks the upstream HTTP
reader. The race coordinator can then receive either the context notification
or the reader's queued completion. Go's `select` does not prioritize cancellation.
If completion won, the coordinator could label the winning stream as stalled and
return without the required `aborted` event. A simultaneously ready deadline
could similarly classify cancellation as a model timeout.

Both candidate selection and winner streaming now recheck the request context
at their shared receive boundary before classifying an event or deadline. An
observed cancellation produces `aborted` and does not trigger model cooldowns.
Normal stream completion and timeout handling retain their existing behavior.

Regression tests queue completion and deadline signals with an already cancelled
request. The original sidecar initialization, streaming, cancellation, reload,
and shutdown test keeps its existing timeout and assertions.
