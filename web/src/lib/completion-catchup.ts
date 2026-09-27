// Catch-up for completions missed while this tab was suspended.
//
// A completion notification is raised from the live push stream's agent_end
// sideband. A phone that locks its screen (or a laptop that sleeps) stops the
// page and the stream with it; a run that finishes during that window is never
// observed, and the push stream deliberately replays nothing on reconnect. The
// resume refresh is the one signal that can recover it: the session was known
// to be running and, after the refetch, is not.
//
// Pure so the decision is testable without a browser or a live server:
// `known` is the tab's set of sessions it has seen running, mutated in place so
// a later resume cannot announce the same completion twice.
export type CatchupSession = { id: string; running?: boolean }

export function reconcileFinishedRuns(opts: {
  known: Set<string>
  sessions: ReadonlyArray<CatchupSession>
  aborted: Set<string>
  announce: (id: string) => void
}): void {
  if (opts.known.size === 0) return
  const present = new Set(opts.sessions.map(s => s.id))
  const running = new Set(opts.sessions.filter(s => s.running).map(s => s.id))
  for (const id of [...opts.known]) {
    if (running.has(id)) continue
    opts.known.delete(id)
    // A session deleted while away has no completion to announce, and an abort
    // the tab issued before sleeping was already suppressed.
    if (!present.has(id)) continue
    if (opts.aborted.delete(id)) continue
    opts.announce(id)
  }
}
