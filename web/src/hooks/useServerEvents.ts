import { useEffect, useRef } from 'react'
import { ApiError, type Client } from '../api/client'
import type { PushEvent } from '../api/types'
import { reconnectDelay } from '../lib/stream-batch'

export function useServerEvents(api: Client, onEvent: (ev: PushEvent) => void, onUnauthorized?: () => void): void {
  const handler = useRef(onEvent)
  const unauthorized = useRef(onUnauthorized)
  useEffect(() => { handler.current = onEvent }, [onEvent])
  useEffect(() => { unauthorized.current = onUnauthorized }, [onUnauthorized])

  useEffect(() => {
    const ac = new AbortController()
    let conn: AbortController | null = null
    let attempt = 0
    let pageHidden = false
    let immediate = false
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
        } catch (error) {
          if (!ac.signal.aborted && error instanceof ApiError && [401, 403, 421].includes(error.status) && unauthorized.current) {
            unauthorized.current()
            return
          }
        }
        ac.signal.removeEventListener('abort', onAbort)
        if (ac.signal.aborted) return
        if (immediate || pageHidden) { immediate = false; attempt = 0; continue }
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
