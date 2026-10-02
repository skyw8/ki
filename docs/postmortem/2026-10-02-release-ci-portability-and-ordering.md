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
