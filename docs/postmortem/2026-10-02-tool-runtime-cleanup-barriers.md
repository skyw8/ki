# Tool owners need cleanup barriers, not just cancellation

## Recurrence

The [previous runtime ownership fix](2026-10-01-runtime-sideband-and-shutdown-ownership.md)
waited for child-agent callbacks before closing process managers. It did not cover
a root run that had occupied its session but had not yet assembled tools, or the
release work that followed the run's public `done` notification. Those writers
could create a manager or write sideband state after cleanup had collected owners.
The public `done` boundary also allowed a replacement occupy to overwrite the
old run in the registry before its release callbacks finished. Waiting only for
the latest registry entry could therefore miss a still-live older writer.

Deletion had the same boundary mistake: it collected descendants before fencing
admission. An already admitted spawn could materialize a child after traversal;
queued work could reoccupy a session after abort but before directory removal.
Controller close also used per-run observation timeouts as if they proved cleanup
had completed.

## Correct boundary

Cancellation is a request. Public run completion is not necessarily callback
completion. Close must fence new work, cancel existing work, and wait for the
actual writer/release/spawn cleanup barriers before removing files and owners.

An observer's deadline bounds its wait, not the owned cleanup. Concurrent
observers share one completion barrier; timeout does not authorize closing
resources underneath a runner. Subtree deletion fences both logical-agent
admission and session occupy/queue admission before traversal.
Per-session writer accounting includes overwritten generations, warmups and
dispatch; a registry snapshot alone is not a writer-completion proof.
Once manager admission is fenced, OS termination can begin concurrently with
runner cleanup. Shared spool/files/extensions still wait for both barriers; a
stalled provider must not prevent explicit shutdown from requesting process stop.

Process output follows the same rule: final sanitized bytes and the final
listener callback belong to the process owner. A failed spool still drains the
external command's output and reports its I/O failure independently.

## Regression coverage

Tests exercise scheduled-but-not-started root runs, post-`done` release,
in-flight spawn reservations, queued work during deletion, and multiple close
observers with separate deadlines. Gates and virtual time expose these ordering
windows without timing-dependent sleeps. Ordinary successful child completion
alone cannot validate server or subtree teardown.
