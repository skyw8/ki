import { useEffect, useRef } from 'react'
import type { Client } from '../api/client'
import type { PushEvent } from '../api/types'
import { reconnectDelay } from '../lib/stream-batch'

/**
 * One long-lived `GET /v1/events` subscription per tab. Every push frame
 * (invalidate hints and session sideband events) goes to the latest callback
 * without re-subscribing, so a re-rendered handler never drops or duplicates
 * frames.
 *
 * The stream replays nothing, so the callback must treat `ready` as "refetch
 * everything": that is also how a reconnect catches up on whatever changed
 * while the tab was disconnected.
 *
 * A dropped stream (server restart, proxy idle timeout, laptop sleep, a phone
 * that suspended the tab and left a half-open socket) is retried with the same
 * exponential backoff + jitter as the run stream. The `readSSE` read-idle guard
 * is what turns a silently dead socket into a dropped stream, because a browser
 * need not report a stalled streaming `fetch` as an error.
 *
 * The listeners here cover the signals that guard cannot see:
 *   - `online`: the device rejoined a network, so even a "live" socket is stale;
 *   - `visibilitychange` to visible: retire the old stream regardless of
 *     heartbeats; transport activity cannot prove the UI received every event;
 *   - `pagehide`/`pageshow`: close subscriptions while cached, then recover.
 */
export function useServerEvents(api: Client, onEvent: (ev: PushEvent) => void): void {
  const handler = useRef(onEvent)
  useEffect(() => { handler.current = onEvent }, [onEvent])

  useEffect(() => {
    const ac = new AbortController()
    let conn: AbortController | null = null
    let attempt = 0
    let pageHidden = false
    let immediate = false
    // `wake` is non-null only while a backoff delay is pending, which is how a
    // resume cuts the wait short instead of waiting out the timer.
    let phase: 'stream' | 'backoff' = 'stream'
    let wake: (() => void) | null = null
    const wakeBackoff = () => { if (phase === 'backoff') wake?.() }
    const dropStream = () => { if (phase === 'stream') conn?.abort() }

    void (async () => {
      while (!ac.signal.aborted) {
        if (pageHidden) {
          await new Promise<void>(resolve => {
            const done = () => { ac.signal.removeEventListener('abort', done); wake = null; resolve() }
            phase = 'backoff'
            wake = done
            ac.signal.addEventListener('abort', done, { once: true })
          })
          if (ac.signal.aborted) return
          continue
        }
        immediate = false
        conn = new AbortController()
        phase = 'stream'
        const onAbort = () => conn?.abort()
        ac.signal.addEventListener('abort', onAbort, { once: true })
        const connectedAt = performance.now()
        try {
          for await (const ev of api.serverEvents(conn.signal)) {
            handler.current(ev)
          }
        } catch {
          // Unmount or a forced reconnect; anything else is a dropped stream,
          // retried below.
        }
        ac.signal.removeEventListener('abort', onAbort)
        if (ac.signal.aborted) return
        // Lifecycle reconnects must not earn exponential backoff, and pagehide
        // must stay disconnected until pageshow instead of reopening in cache.
        if (immediate || pageHidden) { immediate = false; attempt = 0; continue }
        // A stream that stayed up for a while is a healthy restart, not a
        // failing loop: reset the backoff so a long-lived tab reconnects at
        // once instead of waiting out an escalation it no longer earned.
        attempt = performance.now() - connectedAt >= 5000 ? 0 : attempt + 1
        await new Promise<void>(resolve => {
          phase = 'backoff'
          const done = () => {
            window.clearTimeout(timer)
            ac.signal.removeEventListener('abort', done)
            wake = null
            resolve()
          }
          const timer = window.setTimeout(done, reconnectDelay(attempt))
          wake = done
          ac.signal.addEventListener('abort', done, { once: true })
        })
      }
    })()

    const reconnect = () => { pageHidden = false; immediate = true; wakeBackoff(); dropStream() }
    const onVisible = () => { if (document.visibilityState === 'visible') reconnect() }
    const onOnline = () => { if (document.visibilityState === 'visible') reconnect() }
    const onPageHide = () => { pageHidden = true; dropStream(); wakeBackoff() }
    const onPageShow = (event: PageTransitionEvent) => { if (event.persisted) reconnect() }
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('online', onOnline)
    window.addEventListener('pagehide', onPageHide)
    window.addEventListener('pageshow', onPageShow)
    return () => {
      ac.abort()
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('online', onOnline)
      window.removeEventListener('pagehide', onPageHide)
      window.removeEventListener('pageshow', onPageShow)
    }
  }, [api])
}
