// Package shelltools adapts exec_command and write_stdin to internal/process.
// Tools validate model arguments, select the shell, translate private execution
// attribution into an explicit process.Identity, and format bounded results and
// diagnostics. Process lifetime, PTY ownership, incremental output and cleanup
// belong to the process runtime, independently of an observing tool or turn.
// Observation times clamp before duration conversion; token budgets must be
// positive and terminal handles must fit the runtime's positive 53-bit range.
// A running snapshot, even without output, is not a command failure; callers
// needing completion continue observing its handle with write_stdin.
// Incremental-output tests synchronize on readiness and an explicit release,
// not a shell startup deadline or a sleep between output chunks.
// Cross-package contracts: docs/tools.md.
package shelltools
