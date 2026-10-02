# Code Mode caller cancellation bypassed the worker stop barrier

## Symptom

The immutable v0.0.11 tag's macOS CI failed
`TestCodeModeServerCommittedStoreSurvivesOccupiesAndCancellationDrainsCells`:
recovery loaded both `kept: committed` and `discarded: uncommitted`.
This was a production ordering bug, not an incorrect test observation.
The existing server test already waited for real parent callback dispatch and
joined the canceled occupy before checking recovery.

## Cause

The earlier [stop-acknowledgement fix](2026-10-02-release-ci-portability-and-ordering.md)
ordered `TerminateAll`'s explicit parent callback cancellation after the worker's
`stopAll` acknowledgement. However, each callback binding still inherited the
original `Execute` context. Canceling the occupy canceled that binding
independently, before either the yielded cell's terminating `wait` request or
`TerminateAll` reached the worker.

A fast parent callback cancellation error could therefore return over IPC while
the worker cell's context was still live. The runtime classified it as a normal
failed completion, which intentionally commits writes for script errors.
The later worker stop could not undo that committed state.

## Fix and ordering contract

Detach the callback binding's cancellation/deadline from its original caller,
retaining context values. Original caller cancellation still requests worker
termination through the existing cleanup paths. Stopping a worker cell cancels
its callback IPC request; terminal binding retirement also cancels and joins
owned parent callbacks. Thus caller cancellation cannot independently cross the
worker-before-callback stop barrier. The explicit stop acknowledgement, cleanup
watchdog, capability fencing and unconditional callback joins remain intact.

No worker/store locking change is included: this correction targets the
deterministically reproduced lifetime ownership bug.

## Regression and validation

A cheap in-process bidirectional-pipe regression cancels the original yielded
`Execute` context and gates the worker's stop handler at the IPC boundary.
If that cancellation already escaped into the binding, the gate lets its error
finish the cell before worker stop, exposing the old leaked store deterministically.
Otherwise the gate releases worker stop first. There are no fixed sleeps,
subprocess builds, retries or weakened recovery assertions.

The new regression failed 20/20 times on the old code (0.030s package / 1.24s
wall) and passed 20/20 after the fix (0.030s / 0.76s). Existing stop/server checks
passed 20 repetitions before (0.051s Code Mode, 2.701s server / 6.09s wall);
the same checks plus the new regression passed after (0.053s, 2.436s / 6.24s).
The small wall-time difference includes recompilation, not an added wait.

Twenty race repetitions of termination/cancellation/join/store checks passed
(3.811s Code Mode, 19.735s server / 28.33s wall). The complete worker/transport
and server Code Mode check set also passed (0.106s, 0.574s / 1.79s wall).
The complete Code Mode package race suite passed (1.416s / 2.36s), as did all
server Code Mode race checks (4.092s / 5.92s).
These are Linux results; native release CI remains the three-platform gate.
Keep v0.0.11 immutable and publish the corrected code as v0.0.12.
