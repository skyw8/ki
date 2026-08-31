import { useEffect, useMemo, useRef, useState, type UIEvent, type WheelEvent } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { applyFollowTail, followFromGap } from './follow-tail'
import { useI18n, type TFn } from './i18n'
import { IChev, IClock, IClose, ICompact, ICopy, IFold, ISearch, ISpark, ITail, IUser, IWrench } from './icons'
import { Markdown } from './Markdown'
import type { RequestView, ToolSchema, TrajKind, TrajRecord } from './types'

type InspTab = 'summary' | 'preview' | 'raw' | 'system-prompt' | 'tools' | 'context'

type TabItem = { id: InspTab; label: string }

function tabsFor(kind: TrajKind, t: TFn): TabItem[] {
  if (kind === 'system') {
    return [
      { id: 'system-prompt', label: t('traj.tab.system') },
      { id: 'tools', label: t('traj.tab.tools') },
      { id: 'context', label: t('traj.tab.context') },
    ]
  }
  return [
    { id: 'summary', label: t('traj.tab.summary') },
    { id: 'preview', label: t('traj.tab.preview') },
    { id: 'raw', label: t('traj.tab.raw') },
  ]
}

function groupOf(kind: TrajKind, t: TFn, step?: number): string {
  if (kind === 'user' || kind === 'context' || kind === 'system') return t('traj.group.message')
  if (kind === 'compacted') return t('traj.group.compacted')
  return t('traj.group.step', { n: step ?? 1 })
}

function locate(records: TrajRecord[], t: TFn): Map<string, { turn: number; step?: number; group: string }> {
  const out = new Map<string, { turn: number; step?: number; group: string }>()
  const stepByTurn = new Map<number, number>()
  for (const r of records) {
    if (r.step !== undefined) {
      out.set(r.id, { turn: r.turn, step: r.step, group: groupOf(r.kind, t, r.step) })
      continue
    }
    if (r.kind === 'assistant' && !r.requestOnly) {
      const n = (stepByTurn.get(r.turn) ?? 0) + 1
      stepByTurn.set(r.turn, n)
      out.set(r.id, { turn: r.turn, step: n, group: groupOf(r.kind, t, n) })
      continue
    }
    if (r.kind === 'tool') {
      const n = stepByTurn.get(r.turn) ?? 1
      out.set(r.id, { turn: r.turn, step: n, group: groupOf(r.kind, t, n) })
      continue
    }
    out.set(r.id, { turn: r.turn, group: groupOf(r.kind, t) })
  }
  return out
}

function recordText(r: TrajRecord): string {
  if (r.kind === 'system') return r.system || ''
  if (r.kind === 'tool') {
	const bits = [r.name ? `# ${r.name}` : '', pretty(r.input), pretty(r.output), r.details ? `Details\n${pretty(r.details)}` : ''].filter(Boolean)
    return bits.join('\n\n')
  }
  if (typeof r.output === 'string' && r.output) return r.output
  if (typeof r.input === 'string' && r.input) return r.input
  return r.preview || ''
}

function MarkdownBody({ text }: { text: string }) {
  if (!text) return <p className="insp-empty">—</p>
  return <Markdown className="insp-md" text={text} />
}

const KIND_LABEL: Record<TrajKind, string> = {
  user: 'USER',
  context: 'CONTEXT',
  assistant: 'ASSISTANT',
  tool: 'TOOL',
  subtool: 'SUBTOOL',
  compacted: 'COMPACTED',
  compact: 'COMPACT',
  system: 'SYSTEM',
}

function KindIcon({ kind }: { kind: TrajKind }) {
  if (kind === 'user') return <IUser />
  if (kind === 'context') return <ISpark />
  if (kind === 'tool' || kind === 'subtool') return <IWrench />
  if (kind === 'compacted') return <ICompact />
  if (kind === 'system') return <ISpark />
  return <ISpark />
}

export function fmtDur(ms?: number): string {
  if (ms == null) return ''
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
}

function fmtTime(ms?: number): string {
  if (ms == null) return '—'
  const d = new Date(ms)
  const p = (n: number, w = 2) => String(n).padStart(w, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`
}

function pretty(v: unknown): string {
  if (v == null || v === '') return ''
  if (typeof v === 'string') return v
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function schemaType(schema: unknown): string {
  if (!schema || typeof schema !== 'object') return typeof schema
  const s = schema as Record<string, unknown>
  if (typeof s.type === 'string') return s.type
  if (Array.isArray(s.type)) return s.type.map(String).join(' | ')
  if (Array.isArray(s.enum)) return 'enum'
  if (s.anyOf) return 'anyOf'
  return 'object'
}

function SchemaView({ schema }: { schema: unknown }) {
  const { t } = useI18n()
  if (!schema || typeof schema !== 'object') return <pre>{pretty(schema) || '—'}</pre>
  const s = schema as Record<string, unknown>
  const props = s.properties && typeof s.properties === 'object' ? s.properties as Record<string, unknown> : null
  const required = new Set(Array.isArray(s.required) ? s.required.map(String) : [])
  if (!props) return <pre>{pretty(schema)}</pre>
  return (
    <div className="schema">
      {Object.entries(props).map(([name, raw]) => {
        const p = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
        return (
          <div key={name} className="schema-prop">
            <div className="schema-h">
              <code>{name}</code>
              <span className="schema-type">{schemaType(p)}</span>
              {required.has(name) ? <span className="schema-req">{t('traj.required')}</span> : null}
            </div>
            {typeof p.description === 'string' ? <p className="schema-d">{p.description}</p> : null}
          </div>
        )
      })}
    </div>
  )
}

function copyText(text: string) {
  void navigator.clipboard?.writeText(text)
}

function ToolCatalog({ tools }: { tools?: ToolSchema[] }) {
  const { t } = useI18n()
  if (!tools?.length) return <p className="insp-empty" data-testid="system-tools">{t('traj.noTools')}</p>
  return (
    <div className="tool-cat" data-testid="system-tools">
      {tools.map((tool, i) => (
        <details key={`${tool.name}-${i}`} className="tool-cat-item">
          <summary className="tool-cat-sum">
            <IChev open={false} />
            <IWrench />
            <span className="tool-cat-name">{tool.name}</span>
            {tool.description ? <span className="tool-cat-preview">{tool.description}</span> : null}
          </summary>
          <div className="tool-cat-body">
            {tool.description ? (
              <div className="tool-cat-desc-row">
                <p className="tool-cat-desc">{tool.description}</p>
                <button
                  type="button"
                  className="msg-icon"
                  data-testid="copy-tool-desc"
                  aria-label={t('chat.copy')}
                  onClick={e => { e.preventDefault(); copyText(tool.description || '') }}
                >
                  <ICopy />
                </button>
              </div>
            ) : null}
            <div className="tool-cat-label">{t('traj.params')}</div>
            <SchemaView schema={tool.parameters} />
          </div>
        </details>
      ))}
    </div>
  )
}

function lineDiff(a: string, b: string): { kind: 'same' | 'del' | 'add'; text: string }[] {
  const al = a.split('\n')
  const bl = b.split('\n')
  const out: { kind: 'same' | 'del' | 'add'; text: string }[] = []
  const max = Math.max(al.length, bl.length)
  for (let i = 0; i < max; i++) {
    if (i < al.length && i < bl.length && al[i] === bl[i]) out.push({ kind: 'same', text: al[i] })
    else {
      if (i < al.length) out.push({ kind: 'del', text: al[i] })
      if (i < bl.length) out.push({ kind: 'add', text: bl[i] })
    }
  }
  return out
}

function timelineLane(kind: TrajKind): 'input' | 'model' | 'tools' {
  if (kind === 'tool' || kind === 'subtool') return 'tools'
  if (kind === 'assistant' || kind === 'compacted' || kind === 'compact') return 'model'
  return 'input'
}

const VIRTUALIZE_AFTER = 48
const TIMELINE_MAX_BARS = 240

type TimelineBar = {
  id: string
  kind: TrajKind
  lane: 'input' | 'model' | 'tools'
  left: number
  width: number
  index: number
  dim: boolean
  label: string
}

function layoutTimeline(records: TrajRecord[], actualDur: boolean, zoom: number, match: Set<string> | null): TimelineBar[] {
  const items = records.filter(r => !r.requestOnly)
  const indexOf = new Map(records.map((r, i) => [r.id, i]))
  const weights = items.map(r => actualDur ? Math.max(1, Math.round(((r.durationMs ?? 0) * zoom) / 200)) : Math.max(1, Math.round(zoom)))
  const total = weights.reduce((a, b) => a + b, 0) || 1
  const prefix = [0]
  for (const w of weights) prefix.push(prefix[prefix.length - 1] + w)
  const bar = (r: TrajRecord, left: number, width: number): TimelineBar => ({
    id: r.id,
    kind: r.kind,
    lane: timelineLane(r.kind),
    left,
    width,
    index: indexOf.get(r.id) ?? 0,
    dim: !!match && !match.has(r.id),
    label: `${KIND_LABEL[r.kind]} ${r.preview || r.name || ''}`.trim(),
  })
  if (items.length <= TIMELINE_MAX_BARS) {
    return items.map((r, i) => bar(r, prefix[i] / total, weights[i] / total))
  }
  const bucket = Math.ceil(items.length / TIMELINE_MAX_BARS)
  const bars: TimelineBar[] = []
  for (let start = 0; start < items.length; start += bucket) {
    const end = Math.min(items.length, start + bucket)
    let best = start
    for (let i = start + 1; i < end; i++) {
      if (weights[i] > weights[best]) best = i
    }
    bars.push(bar(items[best], prefix[start] / total, (prefix[end] - prefix[start]) / total))
  }
  return bars
}

type TrajLine =
  | { key: string; kind: 'turn'; turn: number }
  | { key: string; kind: 'step'; step: number }
  | { key: string; kind: 'row'; rec: TrajRecord; index: number }

function TrajLineView({
  line, selected, foldedTurns, setFoldedTurns, onPick, asTable,
}: {
  line: TrajLine
  selected: string | null
  foldedTurns: Set<number>
  setFoldedTurns: (update: (s: Set<number>) => Set<number>) => void
  onPick: (r: TrajRecord) => void
  asTable?: boolean
}) {
  if (line.kind === 'turn') {
    const body = (
      <button type="button" className="turn-fold" onClick={() => setFoldedTurns(s => {
        const n = new Set(s)
        if (n.has(line.turn)) n.delete(line.turn)
        else n.add(line.turn)
        return n
      })}
      >
        <IChev open={!foldedTurns.has(line.turn)} /> TURN {line.turn}
      </button>
    )
    if (asTable) return <tr className="turn-h"><td>{body}</td></tr>
    return <div className="turn-h">{body}</div>
  }
  if (line.kind === 'step') {
    if (asTable) return <tr className="step-h"><td>STEP {line.step}</td></tr>
    return <div className="step-h">STEP {line.step}</div>
  }
  const rec = line.rec
  const inner = (
    <div className="cell">
      <span className="idx">{line.index + 1}</span>
      <span className={`tag ${rec.kind}`}><KindIcon kind={rec.kind} />&nbsp;{KIND_LABEL[rec.kind]}</span>
      <span className="preview">{rec.running ? '… ' : ''}{rec.preview || rec.name || '—'}</span>
      <span className="dur">{rec.running ? '' : fmtDur(rec.durationMs)}</span>
    </div>
  )
  if (asTable) {
    return (
      <tr
        data-rid={rec.id}
        className={`traj-row${selected === rec.id ? ' sel' : ''}`}
        data-testid="traj-row"
        data-kind={rec.kind}
        data-request-id={rec.requestId}
        onClick={() => onPick(rec)}
      >
        <td>{inner}</td>
      </tr>
    )
  }
  return (
    <div
      data-rid={rec.id}
      className={`traj-row${selected === rec.id ? ' sel' : ''}`}
      data-testid="traj-row"
      data-kind={rec.kind}
      data-request-id={rec.requestId}
      onClick={() => onPick(rec)}
      role="button"
    >
      {inner}
    </div>
  )
}

export function TrajectoryView({
  records, requests = [], onSelect, selectId, onHydrate,
}: {
  records: TrajRecord[]
  requests?: RequestView[]
  onSelect?: (r: TrajRecord | null) => void
  selectId?: string | null
  onHydrate?: (id: string) => void
}) {
  const { t } = useI18n()
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  useEffect(() => {
    if (selectId) setSel(selectId)
  }, [selectId])
  const [tab, setTab] = useState<InspTab>('summary')
  const [actualDur, setActualDur] = useState(true)
  const [foldedTurns, setFoldedTurns] = useState<Set<number>>(new Set())
  const [foldTools, setFoldTools] = useState(false)
  const [zoom, setZoom] = useState(1)
  const [range, setRange] = useState<[number, number] | null>(null)
  const [follow, setFollow] = useState(true)
  const followRef = useRef(true)
  const tableRef = useRef<HTMLDivElement>(null)
  const drag = useRef<{ i: number } | null>(null)
  const zoomPercent = Math.round(zoom * 100)

  const setFollowOn = (next: boolean) => {
    followRef.current = next
    setFollow(next)
  }

  const filtered = useMemo(() => {
    const s = q.trim().toLowerCase()
    let list = records
    if (s) {
      list = list.filter(r =>
        r.preview.toLowerCase().includes(s)
        || r.kind.includes(s)
        || (r.name ?? '').toLowerCase().includes(s)
        || (r.system ?? '').toLowerCase().includes(s),
      )
    }
    if (range) {
      const [a, b] = range[0] < range[1] ? range : [range[1], range[0]]
      list = list.filter((_, i) => i >= a && i <= b)
    }
    return list
  }, [records, q, range])

  const selected = records.find(r => r.id === sel) ?? null
  const locations = useMemo(() => locate(records, t), [records, t])
  const selectedLoc = selected ? locations.get(selected.id) : undefined
  const selectedTabs = selected ? tabsFor(selected.kind, t) : []
  const selectedRequest = selected?.requestId
    ? requests.find(request => request.id === selected.requestId)
    : undefined

  const visible = useMemo(() => {
    return filtered.filter(r => {
      if (r.requestOnly) return false
      if (foldedTurns.has(r.turn) && r.kind !== 'user' && r.kind !== 'system') return false
      if (foldTools && r.kind === 'tool') return false
      return true
    })
  }, [filtered, foldedTurns, foldTools])

  const recordIndex = useMemo(() => {
    const map = new Map<string, number>()
    records.forEach((r, i) => map.set(r.id, i))
    return map
  }, [records])

  const matchIds = useMemo(() => q.trim() ? new Set(filtered.map(r => r.id)) : null, [filtered, q])
  const timelineBars = useMemo(() => layoutTimeline(records, actualDur, zoom, matchIds), [actualDur, matchIds, records, zoom])

  const lines = useMemo(() => {
    const out: TrajLine[] = []
    let prevTurn = -1
    let prevStep: number | undefined
    for (const rec of visible) {
      if (rec.turn !== prevTurn) {
        out.push({ key: `t-${rec.turn}`, kind: 'turn', turn: rec.turn })
        prevTurn = rec.turn
        prevStep = undefined
      }
      if (rec.step !== undefined && rec.step !== prevStep) {
        out.push({ key: `s-${rec.turn}-${rec.step}`, kind: 'step', step: rec.step })
        prevStep = rec.step
      }
      out.push({ key: rec.id, kind: 'row', rec, index: recordIndex.get(rec.id) ?? 0 })
    }
    return out
  }, [recordIndex, visible])

  const virtualize = lines.length > VIRTUALIZE_AFTER
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => tableRef.current,
    estimateSize: () => 38,
    overscan: 12,
    getItemKey: index => lines[index]?.key ?? index,
    enabled: virtualize,
  })

  useEffect(() => {
    // Why: follow state lags a render behind a wheel/scroll pause. Read the
    // ref so a live run cannot snap the ledger back before React commits.
    if (virtualize && followRef.current && lines.length) {
      virtualizer.scrollToIndex(lines.length - 1, { align: 'end' })
      return
    }
    applyFollowTail(tableRef.current, followRef.current)
  }, [follow, records, visible, virtualize, lines.length])

  useEffect(() => {
    if (!sel) return
    const rec = records.find(r => r.id === sel)
    if (!rec) return
    setFoldedTurns(s => {
      if (s.has(rec.turn)) {
        const n = new Set(s)
        n.delete(rec.turn)
        return n
      }
      return s
    })
  }, [sel, records])

  useEffect(() => {
    if (!sel) return
    if (virtualize) {
      const idx = lines.findIndex(line => line.kind === 'row' && line.rec.id === sel)
      if (idx >= 0) virtualizer.scrollToIndex(idx, { align: 'auto' })
      return
    }
    const wrap = tableRef.current
    const row = wrap?.querySelector(`[data-rid="${sel}"]`) as HTMLElement | null
    if (!wrap || !row) return
    const wrapBox = wrap.getBoundingClientRect()
    const rowBox = row.getBoundingClientRect()
    const off = rowBox.bottom < wrapBox.top + 4 || rowBox.top > wrapBox.bottom - 4
    if (!off) return
    setFollowOn(false)
    row.scrollIntoView({ block: 'nearest' })
  }, [sel, virtualize, lines, virtualizer])

  useEffect(() => {
    if (!sel) return
    const rec = records.find(r => r.id === sel)
    if (!rec) return
    const needsBody = (rec.kind === 'system' && !rec.system) || ((rec.kind === 'tool' || rec.kind === 'assistant') && rec.output == null && rec.input == null)
    if (needsBody) onHydrate?.(rec.id)
  }, [onHydrate, records, sel])

  function pick(r: TrajRecord) {
    setSel(r.id)
    const next = tabsFor(r.kind, t)
    setTab(t => (next.some(x => x.id === t) ? t : next[0].id))
    onSelect?.(r)
  }

  function onWheel(e: WheelEvent) {
    if (!e.ctrlKey && !e.metaKey) return
    e.preventDefault()
    setZoom(z => Math.min(4, Math.max(0.4, z * (e.deltaY > 0 ? 0.9 : 1.1))))
  }

  function adjustZoom(factor: number) {
    setZoom(z => Math.min(4, Math.max(0.4, Number((z * factor).toFixed(2)))))
  }

  function resetTimeline() {
    setRange(null)
    setZoom(1)
  }

  return (
    <div className="traj" data-testid="trajectory">
      <div className="traj-toolbar">
        <label className="traj-search-box">
          <ISearch aria-hidden="true" />
          <input
            className="traj-search"
            aria-label={t('traj.search')}
            placeholder={t('traj.search')}
            value={q}
            onChange={e => setQ(e.target.value)}
          />
        </label>
        <div className="traj-toolbar-controls">
          <div className="traj-view-controls">
            <button
              type="button"
              className={`chip-icon${actualDur ? ' active' : ''}`}
              data-testid="traj-duration"
              aria-pressed={actualDur}
              aria-label={actualDur ? t('traj.actualDur') : t('traj.equalDur')}
              title={actualDur ? t('traj.actualDur') : t('traj.equalDur')}
              onClick={() => setActualDur(v => !v)}
            >
              <IClock />
            </button>
            <button
              type="button"
              className={`chip-icon${foldTools ? ' active' : ''}`}
              data-testid="traj-fold-tools"
              aria-pressed={foldTools}
              aria-label={foldTools ? t('traj.expandTools') : t('traj.foldTools')}
              title={foldTools ? t('traj.expandTools') : t('traj.foldTools')}
              onClick={() => setFoldTools(v => !v)}
            >
              <IFold />
            </button>
            <button
              type="button"
              className={`chip-icon${follow ? ' active' : ''}`}
              data-testid="traj-follow"
              aria-pressed={follow}
              aria-label={follow ? t('traj.follow') : t('traj.unfollow')}
              title={follow ? t('traj.follow') : t('traj.unfollow')}
              onClick={() => {
                const next = !followRef.current
                setFollowOn(next)
                if (next) applyFollowTail(tableRef.current, true)
              }}
            >
              <ITail />
            </button>
          </div>
          {/* Why: wheel and context-menu gestures have no discoverable touch
              equivalent, so zoom and reset stay visible on every input type. */}
          <div className="traj-zoom" role="group" aria-label={t('traj.zoom')}>
            <button
              type="button"
              data-testid="traj-zoom-out"
              aria-label={t('traj.zoomOut')}
              title={t('traj.zoomOut')}
              disabled={zoom <= 0.4}
              onClick={() => adjustZoom(0.8)}
            >
              −
            </button>
            <button
              type="button"
              className="traj-zoom-reset"
              data-testid="traj-zoom-reset"
              aria-label={`${t('traj.resetTimeline')} (${zoomPercent}%)`}
              title={t('traj.resetTimeline')}
              onClick={resetTimeline}
            >
              {zoomPercent}%
            </button>
            <button
              type="button"
              data-testid="traj-zoom-in"
              aria-label={t('traj.zoomIn')}
              title={t('traj.zoomIn')}
              disabled={zoom >= 4}
              onClick={() => adjustZoom(1.25)}
            >
              +
            </button>
          </div>
          <span className="asst-meta traj-record-count">{t('traj.records', { n: visible.length })}</span>
        </div>
      </div>
      <div
        className="timeline"
        data-testid="traj-timeline"
        aria-label={t('traj.timeline')}
        onWheel={onWheel}
        onMouseDown={e => {
          const i = Number((e.target as HTMLElement).dataset.i)
          if (Number.isFinite(i)) drag.current = { i }
        }}
        onMouseUp={e => {
          if (!drag.current) return
          const i = Number((e.target as HTMLElement).dataset.i)
          if (Number.isFinite(i) && i !== drag.current.i) setRange([drag.current.i, i])
          drag.current = null
        }}
        onContextMenu={e => { e.preventDefault(); setRange(null); setZoom(1) }}
      >
        <div className="timeline-labels" aria-hidden="true">
          <span>{t('traj.lane.input')}</span>
          <span>{t('traj.lane.model')}</span>
          <span>{t('traj.lane.tools')}</span>
        </div>
        <div className="timeline-tracks">
          {(['input', 'model', 'tools'] as const).map(lane => (
            <div key={lane} className={`timeline-track timeline-${lane}`}>
              {timelineBars.filter(bar => bar.lane === lane).map(bar => (
                <button
                  key={bar.id}
                  type="button"
                  data-i={bar.index}
                  className={`tl-bar ${bar.kind}${sel === bar.id ? ' on' : ''}${bar.dim ? ' dim' : ''}`}
                  style={{ left: `${bar.left * 100}%`, width: `${Math.max(bar.width * 100, 0.4)}%` }}
                  title={bar.label}
                  aria-label={bar.label}
                  aria-current={sel === bar.id ? 'true' : undefined}
                  onClick={() => {
                    const rec = records.find(r => r.id === bar.id)
                    if (rec) pick(rec)
                  }}
                />
              ))}
            </div>
          ))}
        </div>
      </div>
      {range ? <button type="button" className="chip traj-range-reset" data-testid="traj-clear-range" onClick={() => setRange(null)}>{t('traj.clearRange')}</button> : null}
      <div className={`traj-split${selected ? '' : ' no-insp'}`}>
        <div
          className="traj-table-wrap"
          data-testid="traj-table-wrap"
          ref={tableRef}
          onWheel={e => {
            if (e.deltaY < 0) setFollowOn(false)
          }}
          onScroll={(e: UIEvent<HTMLDivElement>) => {
            const el = e.currentTarget
            const gap = el.scrollHeight - el.scrollTop - el.clientHeight
            setFollowOn(followFromGap(gap, followRef.current))
          }}
        >
          {virtualize ? (
            <div className="traj-table traj-virtual" style={{ height: virtualizer.getTotalSize() }}>
              {virtualizer.getVirtualItems().map(item => {
                const line = lines[item.index]
                if (!line) return null
                return (
                  <div
                    key={item.key}
                    data-index={item.index}
                    ref={virtualizer.measureElement}
                    className="traj-virtual-item"
                    style={{ transform: `translateY(${item.start}px)` }}
                  >
                    <TrajLineView line={line} selected={sel} foldedTurns={foldedTurns} setFoldedTurns={setFoldedTurns} onPick={pick} />
                  </div>
                )
              })}
            </div>
          ) : (
          <table className="traj-table">
            <tbody>
              {lines.map(line => (
                <TrajLineView key={line.key} line={line} selected={sel} foldedTurns={foldedTurns} setFoldedTurns={setFoldedTurns} onPick={pick} asTable />
              ))}
              {visible.length === 0 ? (
                <tr><td><div className="cell"><span className="preview">{t('traj.empty')}</span></div></td></tr>
              ) : null}
            </tbody>
          </table>
          )}
        </div>
        {selected ? (
          <aside className="insp" data-testid="traj-inspector">
            <div className="insp-head">
              <span className={`tag ${selected.kind}`}>{KIND_LABEL[selected.kind]}</span>
              <div className="grow insp-loc" data-testid="insp-loc">
                {selectedLoc
                  ? t('traj.loc', { turn: selectedLoc.turn, group: selectedLoc.group })
                  : t('traj.locTurn', { turn: selected.turn })}
              </div>
              <button type="button" className="icon-btn" onClick={() => { setSel(null); onSelect?.(null) }} aria-label={t('close')}><IClose /></button>
            </div>
            <div className="insp-tabs">
              {selectedTabs.map(item => (
                <button key={item.id} type="button" className={tab === item.id ? 'on' : ''} data-testid={`insp-tab-${item.id}`} onClick={() => setTab(item.id)}>
                  {item.label}
                </button>
              ))}
            </div>
            <div className="insp-body">
              {tab === 'summary' ? (
                <dl>
                  <div><dt>{t('traj.status')}</dt><dd>{selected.running ? t('traj.statusRunning') : selected.error ? t('traj.statusError') : t('traj.statusDone')}</dd></div>
                  {selected.name ? <div><dt>{t('traj.name')}</dt><dd>{selected.name}</dd></div> : null}
                  {selected.step != null ? <div><dt>Step</dt><dd>{selected.step}</dd></div> : null}
                  {selectedRequest ? (
                    <>
                      <div><dt>Request</dt><dd>{selectedRequest.status}</dd></div>
                      {selectedRequest.provider || selectedRequest.model ? <div><dt>Model</dt><dd>{[selectedRequest.provider, selectedRequest.model].filter(Boolean).join(' / ')}</dd></div> : null}
                      {selectedRequest.promptChange ? <div><dt>Prompt</dt><dd>{selectedRequest.promptChange.kind}</dd></div> : null}
                    </>
                  ) : null}
                  <div><dt>{t('traj.started')}</dt><dd>{fmtTime(selected.startedAt)}</dd></div>
                  <div><dt>{t('traj.duration')}</dt><dd>{selected.running ? t('traj.statusRunning') : (fmtDur(selected.durationMs) || '—')}</dd></div>
                  {selected.ttftMs != null ? <div><dt>TTFT</dt><dd data-testid="traj-ttft">{fmtDur(selected.ttftMs)}</dd></div> : null}
                  {selected.usage ? (
                    <>
                      <div><dt>Input tokens</dt><dd>{selected.usage.input ?? 0}</dd></div>
                      <div><dt>Output tokens</dt><dd>{selected.usage.output ?? 0}</dd></div>
                      <div><dt>Cache read</dt><dd>{selected.usage.cacheRead ?? 0}</dd></div>
                      <div><dt>Cache write</dt><dd>{selected.usage.cacheWrite ?? 0}</dd></div>
                    </>
                  ) : null}
                </dl>
              ) : null}
              {tab === 'preview' ? (
                selected.kind === 'tool' ? (
                  <div className="insp-split">
                    {selected.input != null && selected.input !== '' ? (
                      <section>
                        <div className="ctx-label">{t('traj.payload')}</div>
                        <pre>{pretty(selected.input)}</pre>
                      </section>
                    ) : null}
                    <section>
                      <div className="ctx-label">{t('traj.result')}</div>
					  {selected.name === 'Edit' && selected.details && typeof selected.details === 'object' && typeof (selected.details as Record<string, unknown>).diff === 'string'
						? <pre className="sys-diff">{String((selected.details as Record<string, unknown>).diff)}</pre>
						: selected.name === 'apply_patch' && selected.details && typeof selected.details === 'object'
						  ? <pre className="sys-diff">{(((selected.details as Record<string, unknown>).changes as Array<Record<string, unknown>> | undefined) ?? []).map(c => String(c.unified_diff ?? '')).filter(Boolean).join('\n')}</pre>
						  : null}
                      {typeof selected.output === 'string' && selected.output
                        ? <MarkdownBody text={selected.output} />
                        : <pre>{pretty(selected.output) || '—'}</pre>}
                    </section>
                  </div>
                ) : (
                  <div className="insp-split">
                    {selected.kind === 'assistant' && typeof selected.input === 'string' && selected.input ? (
                      <section>
                        <div className="ctx-label">{t('traj.thinking')}</div>
                        <pre>{selected.input}</pre>
                      </section>
                    ) : null}
                    <MarkdownBody text={typeof selected.output === 'string' ? selected.output : recordText(selected)} />
                  </div>
                )
              ) : null}
              {tab === 'raw' ? <pre>{recordText(selected) || '—'}</pre> : null}
              {tab === 'system-prompt' ? <pre data-testid="system-prompt">{selected.system || '—'}</pre> : null}
              {tab === 'tools' ? <ToolCatalog tools={selected.tools} /> : null}
              {tab === 'context' ? (
                <div className="ctx-panel" data-testid="system-diff">
                  <div className="ctx-label">System</div>
                  <pre className="sys-diff">
                    {lineDiff(selected.previousSystem ?? '', selected.system ?? '').map((l, i) => (
                      <div key={i} className={`diff-line ${l.kind}`}>{l.kind === 'del' ? '- ' : l.kind === 'add' ? '+ ' : '  '}{l.text}</div>
                    ))}
                  </pre>
                  {selected.previousTools || selected.tools ? (
                    <>
                      <div className="ctx-label">Tools</div>
                      <pre className="sys-diff">
                        {lineDiff(pretty(selected.previousTools) || '', pretty(selected.tools) || '').map((l, i) => (
                          <div key={i} className={`diff-line ${l.kind}`}>{l.kind === 'del' ? '- ' : l.kind === 'add' ? '+ ' : '  '}{l.text}</div>
                        ))}
                      </pre>
                    </>
                  ) : null}
                </div>
              ) : null}
            </div>
          </aside>
        ) : null}
      </div>
    </div>
  )
}
