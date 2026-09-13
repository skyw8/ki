import { useEffect, useRef } from 'react'
import type { Client } from '../api/client'

/**
 * Report every running session's terminal event, regardless of which session is
 * open. The view stream is torn down when the user switches sessions, so
 * completion awareness cannot ride on it: this keeps one lightweight
 * `?notifications=1` subscription per running session instead. `onComplete` is
 * called for a normal run end and never for an abort (which still emits
 * agent_end, but after run_aborted).
 */
export function useRunCompletion(api: Client, runningIds: string[], onComplete: (sessionId: string) => void): void {
  const subs = useRef(new Map<string, AbortController>())
  const aborted = useRef(new Set<string>())
  // Long-lived subscriptions must call the latest callback (the preference can
  // change while a run is in flight), without tearing down and re-subscribing.
  const onCompleteRef = useRef(onComplete)
  useEffect(() => { onCompleteRef.current = onComplete }, [onComplete])

  useEffect(() => {
    const wanted = new Set(runningIds)
    for (const [id, ac] of subs.current) {
      if (wanted.has(id)) continue
      ac.abort()
      subs.current.delete(id)
      aborted.current.delete(id)
    }
    for (const id of wanted) {
      if (subs.current.has(id)) continue
      const ac = new AbortController()
      subs.current.set(id, ac)
      void (async () => {
        try {
          for await (const ev of api.events(id, ac.signal, true)) {
            if (ev.type === 'run_aborted') {
              aborted.current.add(id)
            } else if (ev.type === 'agent_end') {
              // A queued message may start the next run immediately; the stream
              // stays open so that run's completion is reported too.
              if (!aborted.current.delete(id)) onCompleteRef.current(id)
            }
          }
        } catch {
          // Aborted on unwatch/unmount; nothing to report.
        }
      })()
    }
  }, [api, runningIds])

  useEffect(() => () => {
    for (const ac of subs.current.values()) ac.abort()
    subs.current.clear()
  }, [])
}
