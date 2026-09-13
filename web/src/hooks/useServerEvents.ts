import { useEffect, useRef } from 'react'
import type { Client } from '../api/client'
import type { PushEvent } from '../api/types'

/**
 * One long-lived `GET /v1/events` subscription per tab. Every push frame
 * (invalidate hints and session sideband events) goes to the latest callback
 * without re-subscribing, so a re-rendered handler never drops or duplicates
 * frames.
 *
 * The stream replays nothing, so the callback must treat `ready` as "refetch
 * everything": that is also how a reconnect catches up on whatever changed
 * while the tab was disconnected. A dropped stream (proxy idle timeout, server
 * restart, laptop sleep) is retried on a short backoff; the previous
 * per-session notification streams had the same retry loop.
 */
export function useServerEvents(api: Client, onEvent: (ev: PushEvent) => void): void {
  const handler = useRef(onEvent)
  useEffect(() => { handler.current = onEvent }, [onEvent])

  useEffect(() => {
    const ac = new AbortController()
    void (async () => {
      while (!ac.signal.aborted) {
        try {
          for await (const ev of api.serverEvents(ac.signal)) {
            handler.current(ev)
          }
        } catch {
          // Aborted on unmount; anything else is a dropped stream, retried below.
        }
        if (ac.signal.aborted) return
        await new Promise<void>(resolve => {
          const onAbort = () => { window.clearTimeout(timer); resolve() }
          const timer = window.setTimeout(() => { ac.signal.removeEventListener('abort', onAbort); resolve() }, 1000)
          ac.signal.addEventListener('abort', onAbort, { once: true })
        })
      }
    })()
    return () => ac.abort()
  }, [api])
}
