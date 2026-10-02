// Package process owns session-scoped shell processes, shell discovery and
// cross-platform process-tree control. Manager accepts explicit Identity values
// and publishes typed snapshots without depending on tools or loop events.
//
// Unix PTY and Windows ConPTY support interactive terminals; pipes reject ordinary
// stdin but accept explicit interruption. Canceling an observation never kills
// a process. Explicit stop, session deletion and shutdown own termination. Raw
// stdout/stderr is spooled; memory, model observations and live previews are
// bounded. Failed spools report diagnostics without stopping output draining.
// Preview publication coalesces slow consumers, caps progress per process, and
// never drops initial/final lifecycle transitions. Close waits for all writers
// and final publications; listeners must not synchronously close their manager.
// OutputSpool is supplied by the host, with manager-owned temporary
// files as fallback. Numeric handles are session-scoped and cannot be restored
// after restart. Live capacity never evicts live processes.
// Per-process interactions serialize input and incremental output consumption;
// cancellation while queued preserves the handle without consuming output.
// Completed handles release consumed retention windows while keeping bounded
// immutable runtime previews and their incremental-output cursor.
//
// Windows defaults to PowerShell then Git Bash; Unix uses discovered Bash. Proxy
// variables, available bundled rg/fd executables, and extension PATH directories
// are inherited. Child-local pager/color defaults keep agent terminals out of
// inherited interactive pagers. Bash uses a BASH_ENV shim after login profiles to retain these
// directories. Sidecar launchers share process-group control without importing
// tool implementations.
// Cross-package contracts: docs/tools.md and docs/extension.md.
package process
