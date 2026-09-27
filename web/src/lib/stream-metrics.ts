export type DisplayRevision = { runId: string; seq: number; receivedAt: number; pendingSince: number }
type Commit = { seq: number; lag: number; pending: number; at: number }
const commits: Commit[] = []
let last: DisplayRevision | undefined
let enabled: boolean | undefined

export function streamMetricsEnabled(): boolean {
  if (enabled === undefined) {
    try { enabled = localStorage.getItem('ki-perf-hud') === '1' || localStorage.getItem('ki-stream-metrics') === '1' }
    catch { enabled = false }
  }
  return enabled
}

export function recordStreamCommit(revision: DisplayRevision | undefined, root: HTMLElement | null) {
  if (!revision || !streamMetricsEnabled() || document.hidden || !root
    || last?.runId === revision.runId && last.seq === revision.seq) return
  const rect = root.getBoundingClientRect()
  if (rect.bottom < 0 || rect.top > innerHeight) return
  last = revision
  const at = performance.now()
  commits.push({ seq: revision.seq, at, lag: at - revision.receivedAt, pending: at - revision.pendingSince })
  if (commits.length > 240) commits.shift()
}

export function streamMetrics() {
  const sorted = commits.map(c => c.lag).sort((a, b) => a - b)
  return { commits: [...commits], p95: sorted[Math.floor(sorted.length * .95)] ?? 0, pendingMax: Math.max(0, ...commits.map(c => c.pending)) }
}
