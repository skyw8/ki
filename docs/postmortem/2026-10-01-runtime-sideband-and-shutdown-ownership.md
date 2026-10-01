# Runtime progress must not own the conversation leaf

## Failure

A process or child-agent progress callback can run after the tool that created it
has returned. Appending that callback as a normal session-tree event advances the
conversation leaf while a live loop writer still holds the previous leaf. The
next model message can then fork or fail its append even though the progress
update contained no conversational input.

The same detached lifetime exposed a shutdown race: closing the process managers
before waiting for child runners allows an in-flight child to create another
manager or append progress after cleanup. This can leave a process alive or
write into a session directory that the test has already removed.

## Ownership rules

- Runtime updates use `AppendSidebandEvent`. They are durable, session-wide and
  visible through the existing SSE and session GET projections, but never move
  the active transcript leaf.
- Completion messages enter the structural parent's context mailbox with a
  stable task/generation identity. Progress updates do not enter the model Inbox.
- Shutdown first closes agent admission and waits for runner completion callbacks,
  then closes the collected process managers. Tree abort fences spawn and task
  admission for the subtree before collecting descendants, stops their runners,
  and only then collects and terminates their processes. Cancelling only the
  root would still allow an active descendant to spawn during traversal.
- Turn interruption cancels an observer. Explicit process stop or tree cleanup
  owns OS process termination. A blocked terminal write remains manager-owned
  until it completes or the terminal closes; it cannot hold up turn cancellation.

## Validation

Session tests check that sideband entries preserve the live leaf and appear in
runtime trace regardless of the selected branch. Server tests exercise detached
child completion and shutdown. Process tests check cancelled observations,
blocked PTY input, explicit descendant termination, and incremental output.
Race tests cover the server, loop, session, extension and tool packages.
