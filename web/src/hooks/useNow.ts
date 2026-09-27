import { useSyncExternalStore } from 'react'

/**
 * One shared clock for every live duration readout.
 *
 * Why a module-level store instead of an interval per row: a long turn can hold
 * many running tools plus its divider, and one timer each would wake the main
 * thread N times per tick. Here a single interval feeds every subscriber, and
 * it only runs while at least one live reader is mounted — an idle transcript
 * schedules nothing. The snapshot is a fresh wall-clock read on each tick
 * rather than an incremented counter, so a suspended or backgrounded tab
 * catches up on resume instead of drifting behind.
 */
const TICK_MS = 200

const listeners = new Set<() => void>()
let timer: number | undefined
let now = Date.now()

function tick() {
  now = Date.now()
  // React may unsubscribe synchronously while we notify; iterate a copy.
  for (const listener of [...listeners]) listener()
}

function subscribe(listener: () => void): () => void {
  if (!listeners.size) {
    now = Date.now()
    timer = window.setInterval(tick, TICK_MS)
  }
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
    if (!listeners.size && timer !== undefined) {
      window.clearInterval(timer)
      timer = undefined
    }
  }
}

function noopSubscribe(): () => void {
  return () => {}
}

function getSnapshot(): number {
  return now
}

/**
 * useNow returns the current wall clock, re-rendering roughly every 200ms while
 * `active`. An inactive caller gets a static value and subscribes to nothing,
 * so callers can pass their running flag directly: `useNow(!!node.running)`.
 */
export function useNow(active: boolean): number {
  return useSyncExternalStore(active ? subscribe : noopSubscribe, getSnapshot, getSnapshot)
}
