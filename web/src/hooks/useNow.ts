import { useSyncExternalStore } from 'react'

const TICK_MS = 200

const listeners = new Set<() => void>()
let timer: number | undefined
let now = Date.now()

function tick() {
  now = Date.now()
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

export function useNow(active: boolean): number {
  return useSyncExternalStore(active ? subscribe : noopSubscribe, getSnapshot, getSnapshot)
}
