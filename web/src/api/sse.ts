import { yieldToMain } from '../lib/stream-batch'

// Browsers never surface a stalled streaming response. `fetch` has no idle
// deadline (WHATWG fetch #180, open since 2015) and TCP keepalive is invisible
// to JS, so after a phone suspends or changes network the reader can sit on a
// half-open socket forever — no error, no reconnect. The server heartbeats
// every SSE response with a `: ping` comment every 15s (see docs/events.md), so
// a gap this long means the connection is dead. Ending the generator lets the
// caller's reconnect loop run instead of hanging on a zombie socket.
//
// Three missed heartbeats: long enough that a slow model round or a busy proxy
// is never mistaken for a stall, short enough to recover while the user is
// still looking at the screen.
export const SSE_IDLE_TIMEOUT_MS = 45_000

export type ReadSSEOptions = {
  /**
   * End the stream when no bytes arrive for this long; 0 disables the guard.
   * Overridden in tests to observe the stall without waiting out the
   * production interval.
   */
  idleTimeoutMs?: number
}

/** Incremental line decoding, with a task budget even when a whole replay is
 * already buffered. Async-generator yields alone can starve frames in a chain
 * of resolved promises. UTF-8 and CRLF may both straddle network chunks.
 *
 * A read-idle guard (see `SSE_IDLE_TIMEOUT_MS`) ends the generator when the
 * stream goes silent, which the callers already treat as a dropped stream: the
 * push stream refetches everything on `ready`, the run stream re-listens from
 * its cursor.
 */
export async function* readSSE<T extends { type: string }>(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
  opts: ReadSSEOptions = {},
): AsyncGenerator<T> {
  const idleTimeoutMs = opts.idleTimeoutMs ?? SSE_IDLE_TIMEOUT_MS
  const reader = body.getReader()
  const abort = () => { void reader.cancel(signal?.reason).catch(() => {}) }
  signal?.addEventListener('abort', abort, { once: true })
  const decoder = new TextDecoder()
  let buffer = ''
  let searched = 0
  let event = ''
  let data: string[] = []
  let sliceAt = performance.now()
  let count = 0

  // Read-idle guard. It must not end the stream while the page is hidden: a
  // frozen tab cannot read a new connection, its timers are throttled, and
  // reconnecting there only churns server subscriptions. `visibilitychange`
  // (and the sweep, once timers resume) covers the resume instead. A sweep
  // interval, rather than a timeout reset on every chunk, keeps a
  // high-throughput run from churning a timer per token.
  let lastActivity = performance.now()
  // Wall clock additionally counts suspend time, which a monotonic clock
  // excludes on Linux/Android: a socket that died during a phone lock is caught
  // the moment the tab is visible again, not after another timeout of awake
  // time. The sweep keeps using the monotonic clock so an NTP jump cannot fake
  // a stall.
  let lastActivityWall = Date.now()
  let stalled = false
  const hasDocument = typeof document !== 'undefined'
  const noteActivity = () => { lastActivity = performance.now(); lastActivityWall = Date.now() }
  const markStalled = () => {
    if (stalled) return
    stalled = true
    // Cancel, don't throw: a stalled stream is "the stream ended", which both
    // callers already handle by reconnecting without a user-facing error.
    void reader.cancel().catch(() => {})
  }
  const watchdog = idleTimeoutMs > 0
    ? setInterval(() => {
        if (stalled) return
        if (hasDocument && document.visibilityState === 'hidden') return
        if (performance.now() - lastActivity >= idleTimeoutMs) markStalled()
      }, Math.max(50, Math.floor(idleTimeoutMs / 3)))
    : undefined
  // Resume handling, separate from the idle timeout on purpose: a phone that
  // was backgrounded for a few seconds may lose the socket
  // long before 45s of silence elapse, and waiting out the idle timeout leaves
  // the open transcript stale until a manual refresh. A frozen tab reads no
  // bytes while hidden, so "hidden long enough and nothing read since" means
  // the UI is behind: reconnect now. A desktop tab keeps reading heartbeats
  // while hidden, so its `lastActivityWall` moves past `hiddenSince` and it is
  // left alone here. App still reconciles the transcript on every resume:
  // receiving bytes is not evidence that the rendered snapshot is current.
  let hiddenSince: number | null = hasDocument && document.visibilityState === 'hidden' ? Date.now() : null
  const onVisibility = () => {
    if (stalled) return
    if (hasDocument && document.visibilityState === 'hidden') {
      if (hiddenSince === null) hiddenSince = Date.now()
      return
    }
    const since = hiddenSince
    hiddenSince = null
    const resumeGrace = Math.min(2000, idleTimeoutMs)
    if (since !== null && Date.now() - since >= resumeGrace && lastActivityWall <= since) {
      markStalled()
      return
    }
    // Foreground staleness: the socket died with the tab visible.
    if (Date.now() - lastActivityWall >= idleTimeoutMs) markStalled()
  }
  const watching = watchdog !== undefined && hasDocument
  if (watching) document.addEventListener('visibilitychange', onVisibility)

  const flush = (): T | null => {
    const name = event
    event = ''
    if (!data.length) return null
    const raw = data.join('\n')
    data = []
    // A malformed message must fail the connection: silently skipping it can
    // advance the resume cursor past a lifecycle event the view never saw.
    const value = JSON.parse(raw) as T
    if (!value.type && name) value.type = name
    return value
  }
  try {
    while (true) {
      signal?.throwIfAborted()
      const { value, done } = await reader.read()
      signal?.throwIfAborted()
      // Any chunk, including a `: ping` comment, proves the socket is alive.
      if (value !== undefined) noteActivity()
      buffer += decoder.decode(value, { stream: !done })
      let consumed = 0
      while (true) {
        const nl = buffer.indexOf('\n', searched)
        if (nl === -1) { searched = buffer.length; break }
        const line = buffer.slice(consumed, nl).replace(/\r$/, '')
        consumed = searched = nl + 1
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
        else if (line === '') {
          const value = flush()
          if (value) { yield value; count++ }
        }
        signal?.throwIfAborted()
        if (count >= 128 || performance.now() - sliceAt >= 4) {
          await yieldToMain(signal)
          count = 0
          sliceAt = performance.now()
        }
      }
      buffer = buffer.slice(consumed)
      searched -= consumed
      if (done) {
        if (buffer.startsWith('data:')) data.push(buffer.slice(5).replace(/^ /, '').replace(/\r$/, ''))
        const value = flush()
        if (value) yield value
        return
      }
    }
  } finally {
    if (watchdog !== undefined) clearInterval(watchdog)
    if (watching) document.removeEventListener('visibilitychange', onVisibility)
    signal?.removeEventListener('abort', abort)
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
