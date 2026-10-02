# Release CI portability and observation ordering

## Symptoms

The v0.0.8 release preflight found repeated failed main-branch CI runs. Linux
regression could pass locally while macOS/Windows fixtures failed, and two
Linux concurrency tests failed intermittently.

## Causes and fixes

- Windows checkout CRLF prevented the standalone extension module declaration
  from being rewritten. Normalize line endings and test both LF and CRLF.
- Install validation fixtures used noncanonical temporary roots, unlike
  discovery. Resolve the fixture root to avoid macOS `/var` and Windows short
  path aliases.
- Rust command tests compared normalized paths to temporary-directory strings
  retaining a trailing separator. Compare paths and explicitly cover separators.
- Windows `os.Stat` file identity can be resolved lazily by path. Body-cache
  validation must capture identity from an open file, or equal-size,
  equal-mtime atomic replacements can incorrectly reuse old bodies.
- A 250ms shell observation is not an execution deadline. Git Bash/ConPTY
  startup can exceed it without launching a pager. Continue observing the
  returned handle with a bounded deadline and retain all incremental output.
- Push invalidations coalesce and have writer priority over queued sideband
  events. The activity test discarded an invalidation while waiting for
  `agent_end`, then timed out waiting for a nonexistent second frame. Require
  both signals without assuming their relative order; retain the list/detail
  and ETag assertions.
- Code Mode parent callback cancellation could reach a cell before the worker's
  termination request, allowing a failed cell to commit pending store writes.
  A worker stop acknowledgement must precede parent callback cancellation;
  ownership is still joined before termination returns.

## Validation and prevention

Keep native three-platform CI as the publication gate, rather than treating
Linux-only success as release readiness. Regression fixtures should model
canonical production paths and distinguish observation budgets from deadlines.
Cross-channel tests must follow documented ordering, not scheduler timing.

Focused activity checks (20 repetitions) reproduced the old failure: 11.193s
package time / 15.04s wall, including the 5s timeout. The corrected check passed
in 6.048s / 10.84s. Shell pager checks (20 repetitions) changed from 0.319s to
0.342s package time; healthy completion wakes the observer immediately.

The deterministic Code Mode regression failed 20/20 times against the old
manager and passed after the stop acknowledgement fix; focused cancellation
checks and the package race suite passed. Final preflight passed WebUI
typecheck/build, `go vet ./...`, `go test -tags embed -count=1 ./...` (including
the installed Bun/Chromium harness), and the complete native/launcher Rust unit
entrypoint. Native macOS/Windows execution remains the release workflow's duty.

## Follow-up from the v0.0.8 tag

The tag's native Linux, WebUI, quality and Linux Go jobs passed. Native
Windows/macOS Go jobs exposed two further test synchronization assumptions:

- Incremental shell output expected `one` within the first 250ms, before Git
  Bash necessarily started. Both the main and tag Windows jobs failed there.
  A readiness gate must hold the second output until the first segment is
  observed; a startup deadline is not an output delimiter.
- The disconnect test used a replay buffer count of 12 as a completion fence.
  That count can include a superseded partial, so a live replay differed from
  the exact completed replay. Wait for run release before the unchanged replay
  assertion. Twenty repetitions measured 1.156s before / 1.138s after (4.72s /
  4.74s wall), without an added fixed wait.

No v0.0.8 release assets were published. Keep its pushed tag immutable and use
v0.0.9 for the corrected release rather than force-replacing a public tag.

The native Windows protocol job subsequently passed unit tests and genuine
indexing/search, but its semantic-result assertion accepted only `src/theme.ts`
while the correct host-native anchor was `src\theme.ts`. Accept either host
separator without changing the expected file, semantic search, freshness,
source-line, source-free binary or index lifecycle assertions. Native Linux
and macOS protocol jobs both passed.

The gated shell fixture's ten repetitions improved from 6.285s to 2.619s
package time (6.93s to 3.35s wall); the whole shell suite improved from 1.752s
to 1.395s. Final Linux native protocol validation passed all 31 cases,
including genuine semantic indexing/search and source-free launch.

## Follow-up from the v0.0.11 preflight

Native Windows main CI exposed phase-dependent Codex compaction cancellation.
The isolation fixture flushed headers on the server before cancellation, but
that did not prove the client's `http.Client.Do` had returned. Compaction
translated cancellation during body reads to `Codex compaction was cancelled`,
while the pending-header path leaked `Post ...: context canceled`.

Normalize the pending-header path to the same provider cancellation error and
wrap the context error in every cancellation phase. Preserve the error-text
assertion and additionally require `errors.Is(err, context.Canceled)`. Withhold
all headers in a new deterministic regression. The isolation fixture now waits
for the cancelled response's server context to close, then releases the still
active unrelated request; no fixed sleep substitutes for either ordering fence.

Thirty repetitions of the original isolation check reproduced the old failure
in 4.604s package / 5.21s wall time; the gated replacement passed in 0.057s /
0.75s. The pending-header regression failed 10/10 times against the old code
(0.021s package time). Both cancellation checks passed 30 repetitions after
the fix (0.072s); focused cancellation and compaction RPC race checks passed
30 repetitions (1.688s), and the complete Codex OAuth package passed (0.505s).
The complete package race check also passed (2.068s). These are Linux results;
native Windows/macOS CI remains the publication gate.
