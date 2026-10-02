// Package codemode runs bounded JavaScript cells in fresh goja runtimes and
// exposes a same-binary worker transport.
//
// Manager owns session-scoped worker processes; workers have no filesystem,
// network, module loader or exported Go service objects in their JavaScript
// environments. All nested tool calls and notifications return to the parent.
// image and generatedImage accept inline base64 only; audio is not exposed
// because Ki's content IR has no audio output contract.
// The parent remains responsible for capability filtering, hooks, identity,
// persistence and normal tool-result publication.
//
// Each cell has a single VM owner goroutine. Callback completions are marshaled
// back to that owner; cancellation interrupts CPU-bound JavaScript. Source,
// transport, retained output, store, cells and callback work have explicit
// budgets. These are not hard process heap limits or an OS security sandbox.
//
// Cells snapshot session JSON values at admission. Local writes are visible
// within the cell and commit atomically on ordinary completion, including a
// script exception; cancellation and termination discard writes. Concurrent
// commits are last-completion-wins per key. Yield is observation timing, not
// the execution deadline. Observations consume all accumulated output, trimming
// text with an explicit note to the observation budget; omitted text is not
// retained. Wait consumes only new output and closes terminal cells.
//
// Completion drains notifications, cancels pending nested tool observations,
// and joins their cancellation cleanup before publishing the final result.
// Exceeding a cleanup budget is reported, never mistaken for joined ownership.
// A parent watchdog may kill a stuck worker, but parent callbacks are still
// joined unconditionally and must honor cancellation. Terminal Execute/Wait
// fence and join only their own cell's parent callbacks, not other live cells.
// Session termination acknowledges stopped worker cells before fencing parent
// callbacks; otherwise a cancellation error could commit a failed cell's writes.
// Session closure discards cells and values; no store state is persisted.
// notify reports immediate bounded parent progress and also appends text for the
// next exec/wait observation, rather than injecting provider-specific outputs.
package codemode
