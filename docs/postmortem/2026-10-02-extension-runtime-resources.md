# Extension runtime resource boundaries

A process audit on a 96-CPU host found almost one hundred idle threads in the
native search sidecar. Its default Tokio runtime eagerly allocated one async
worker per CPU even though the sidecar admits only four searches. The worker
count was not evidence that Go sidecars needed the same limit: their idle
threads were runtime-managed, with no extension-owned CPU-count worker pool.

The search sidecar now bounds async workers to four search slots plus one control
worker, capped by available CPUs. Native model-compute pools and management
CLI/index runtimes retain their parallelism. A synchronous native-search barrier
test verifies that control replies and cancellation remain responsive under
search occupancy, rather than testing only asynchronously sleeping handlers.

The audit also found repeated work across resource boundaries:

- Lifecycle fan-out marshaled the same growing message payload for each
  subscriber. It now selects exact subscribers and shares one marshaled payload,
  keeping synchronous ordered writes instead of adding another snapshot queue.
- Codex copied the entire nested history only to insert top-level metadata.
  A shallow copy protects that mutation without duplicating read-only history.
- Telegram retained a whole attachment in memory before saving it. Downloads
  now stream into a temporary file, published only on success. Oversized or
  incomplete responses never publish a truncated attachment.

Process-tree ownership was already required for sidecars, but install hooks
still canceled only their launcher. `go run` can leave its installer/compiler
descendants building and holding locks after that launcher exits. Install hooks
now use the same process-group/job ownership and release platform handles after
reaping the command. A portable regression waits for a descendant's readiness,
cancels installation, and verifies that its connection closes.

The runtime watcher also retried successful-but-crashing sidecars without any
pause, unlike failed initialization. Bounded fast-exit backoff now prevents
spawn/status storms; catalog changes wake waits and a publication-generation
guard closes obsolete launches instead of retaining disabled sidecars.
Injected clock/start/wait tests cover these paths without real retry sleeps.

The lesson is to size orchestration separately from native computation, avoid
copying immutable payloads at every boundary, and apply process ownership to
build launchers as well as long-lived runtimes. Allocation benchmarks accompany
the Go changes; native tests use isolated fixtures, never the user's index.
