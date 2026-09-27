import type { LoopEvent } from '../api/types'

export type FrameClock = {
  now: () => number
  frame: (run: () => void) => number
  cancelFrame: (id: number) => void
  delay: (run: () => void, ms: number) => number
  cancelDelay: (id: number) => void
}

const browserClock: FrameClock = {
  now: () => performance.now(),
  frame: run => requestAnimationFrame(run),
  cancelFrame: id => cancelAnimationFrame(id),
  delay: (run, ms) => window.setTimeout(run, ms),
  cancelDelay: id => window.clearTimeout(id),
}

/** Decode first, then replace only consecutive snapshots of the same message.
 * One clock owns display pacing. New input never postpones an existing frame
 * or deadline, and a terminal event cannot wait behind a playback animation.
 */
export function streamBatch(apply: (events: LoopEvent[]) => void, clock: FrameClock = browserClock) {
  let queue: LoopEvent[] = []
  let frame = 0
  let timer = 0
  let since = 0
  let disposed = false
  const cancel = () => {
    if (frame) clock.cancelFrame(frame)
    if (timer) clock.cancelDelay(timer)
    frame = timer = 0
  }
  const flush = () => {
    cancel()
    const events = queue
    queue = []
    if (!disposed && events.length) apply(events)
  }
  return {
    enqueue(event: LoopEvent) {
      if (disposed) return
      if (!queue.length) since = clock.now()
      const prev = queue.at(-1)
      if (event.type === 'message_update' && prev?.type === 'message_update'
        && event.runId === prev.runId && event.messageStream === prev.messageStream) {
        // Preserve the age of work still owed to the screen, not just the newest
        // token's time; resetting it would hide starvation under constant input.
        if (event.display && prev.display) event = { ...event, display: { ...event.display, pendingSince: prev.display.pendingSince } }
        queue[queue.length - 1] = event
      } else queue.push(event)
      if (['message_end', 'agent_end', 'run_aborted'].includes(event.type)
        || queue.length >= 128 || clock.now() - since >= 100) {
        flush()
      } else {
        if (!frame) frame = clock.frame(flush)
        // rAF pauses in hidden tabs. Bound retained lifecycle events there too.
        if (!timer) timer = clock.delay(flush, 100)
      }
    },
    flush,
    dispose() { disposed = true; cancel(); queue = [] },
  }
}

/** A real task boundary, unlike Promise.resolve(), lets paint and input run. */
export function yieldToMain(signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return }
    const abort = () => { clearTimeout(timer); reject(signal?.reason) }
    const timer = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve() }, 0)
    signal?.addEventListener('abort', abort, { once: true })
  })
}

export function reconnectDelay(attempt: number, random = Math.random()): number {
  return Math.min(10_000, Math.round(250 * 2 ** Math.min(attempt, 6) * (0.8 + 0.4 * random)))
}

export function waitForReconnect(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(signal.reason); return }
    const abort = () => { clearTimeout(timer); reject(signal.reason) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, ms)
    signal.addEventListener('abort', abort, { once: true })
  })
}
