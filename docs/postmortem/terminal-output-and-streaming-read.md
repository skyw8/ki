# Terminal output quotas and bounded text reads

## What happened

Terminal output used the ordinary output store only to obtain an `os.File`. Writes bypassed the ordinary spill path and its accounting. The 1 MiB terminal ring bounded memory but not disk usage; the ordinary 8 MiB/file and 256 MiB/session values were not terminal quotas.

Text Read also loaded and split the complete file before applying line pagination. Reading twenty lines from a large terminal spool therefore allocated the complete log and its line index again. An overlong line was truncated with a line-offset hint that could not recover the rest of that line.

## Why the implementation is structured this way

Terminal files now use a separate metered writer, with independent file/session/service limits. Space is reserved under the store lock, but disk writes run outside it so unrelated processes do not serialize on filesystem I/O. A failed or quota-limited write retains its raw prefix, reports its byte count and incomplete state, and does not stop draining the command's pipes or PTY. Session removal releases accounting and rejects further writes through outstanding writers.

Incremental terminal output advances its cursor only by bytes actually returned in that field. Optional head/tail summaries are separate from cursor consumption. Complete logs inside quota remain recoverable through Read; an exhausted quota only guarantees the recorded raw prefix.

Text Read opens a stream, classifies its short signature, and scans text in bounded chunks. It still scans the whole file to validate UTF-8 and report exact totals, but retains only a bounded page and line fragment. Long-line continuation uses a raw byte offset at a UTF-8 boundary rather than claiming that the next line recovers omitted bytes. Image, PDF, and notebook processing keep their format-specific path.

Shell snapshots are scoped to a process manager and identity of shell, cwd, environment, and known profile files. Capture and replay are restricted to POSIX Bash login calls; unsupported environments and failed captures use the original launch path. Reload invalidates the cache; a short TTL bounds staleness for arbitrary sourced dependencies that are not individually tracked. Captures with profile output are rejected rather than silently suppressing that output. Profile initialization may have side effects, so this cache is not a persistent interactive shell and does not promise replay of background jobs, traps, or external mutable state.

## Regression coverage

Tests cover quota scopes and release, incomplete raw-prefix recovery while draining, incremental cursor and summary separation, UTF-8 split across chunks, late binary data, CRLF and exact totals, cancellation, long-line byte continuation, and shell profile/cwd/reload/TTL invalidation. Isolated profile fixtures avoid reading or changing the running user's profile for snapshot behavior tests.
