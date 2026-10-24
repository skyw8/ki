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
    if (!present.has(id)) continue
    if (opts.aborted.delete(id)) continue
    opts.announce(id)
  }
}
