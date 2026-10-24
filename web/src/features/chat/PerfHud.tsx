import { useEffect, useRef, useState } from 'react'
import { streamMetrics } from '../../lib/stream-metrics'

type Sample = {
  frames: number[]
  longTasks: number[]
  longTotal: number
  drift: number
}

const WINDOW = 120

export function PerfHud({ target }: { target: React.RefObject<HTMLElement | null> }) {
  const [enabled] = useState(() => {
    try {
      return localStorage.getItem('ki-perf-hud') === '1'
    } catch {
      return false
    }
  })
  const [view, setView] = useState({ fps: 0, worst: 0, longTasks: 0, longTotal: 0, drift: 0, rows: 0, top: 0, streamP95: 0, streamPending: 0 })
  const sample = useRef<Sample>({ frames: [], longTasks: [], longTotal: 0, drift: 0 })

  useEffect(() => {
    if (!enabled) return
    const s = sample.current
    let raf = 0
    let last = performance.now()
    let lastTop = -1
    let lastRow = -1
    const observer = new PerformanceObserver(list => {
      for (const entry of list.getEntries()) {
        s.longTasks.push(entry.duration)
        s.longTotal += entry.duration
      }
    })
    if (PerformanceObserver.supportedEntryTypes.includes('longtask')) observer.observe({ type: 'longtask', buffered: false })
    const tick = (t: number) => {
      const dt = t - last
      last = t
      s.frames.push(dt)
      if (s.frames.length > WINDOW) s.frames.shift()
      const el = target.current
      if (el) {
        const row = el.querySelector('[data-item-key]') as HTMLElement | null
        const rowTop = row ? Math.round(row.getBoundingClientRect().top) : -1
        if (lastTop >= 0 && rowTop >= 0 && lastRow >= 0) {
          const drift = Math.abs((rowTop - lastRow) + (el.scrollTop - lastTop))
          if (drift > Math.abs(s.drift)) s.drift = drift
        }
        lastTop = Math.round(el.scrollTop)
        lastRow = rowTop
      }
      if (s.frames.length % 30 === 0) {
        const sorted = [...s.frames].sort((a, b) => a - b)
        const p50 = sorted[Math.floor(sorted.length / 2)] ?? 0
        setView({
          streamP95: Math.round(streamMetrics().p95),
          streamPending: Math.round(streamMetrics().pendingMax),
          fps: p50 > 0 ? Math.round(1000 / p50) : 0,
          worst: Math.round(sorted[sorted.length - 1] ?? 0),
          longTasks: s.longTasks.length,
          longTotal: Math.round(s.longTotal),
          drift: Math.round(s.drift),
          rows: el ? el.querySelectorAll('[data-item-key]').length : 0,
          top: el ? Math.round(el.scrollTop) : 0,
        })
        s.longTasks = []
        s.longTotal = 0
        s.drift = 0
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => {
      cancelAnimationFrame(raf)
      observer.disconnect()
    }
  }, [enabled, target])

  if (!enabled) return null
  return (
    <div className="perf-hud" data-testid="perf-hud">
      <span>fps {view.fps}</span>
      <span>worst frame {view.worst}ms</span>
      <span>long {view.longTasks}/{view.longTotal}ms</span>
      <span>drift {view.drift}px</span>
      <span>rows {view.rows}</span>
      <span>top {view.top}</span>
      <span>stream p95 {view.streamP95}ms</span>
      <span>pending max {view.streamPending}ms</span>
      <button
        type="button"
        onClick={() => {
          try {
            localStorage.removeItem('ki-perf-hud')
          } catch {}
          window.location.reload()
        }}
      >
        off
      </button>
    </div>
  )
}
