import { Fragment, useEffect, useMemo, useRef, useState, type WheelEvent } from 'react'
import { applyFollowTail } from './follow-tail'
import { useI18n, type TFn } from './i18n'
import { IChev, IClock, IClose, ICompact, IFold, ISearch, ISpark, ITail, IUser, IWrench } from './icons'
import { renderMarkdown } from './markdown'
import type { ToolSchema, TrajKind, TrajRecord } from './types'

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
  if (kind === 'user') return t('traj.group.message')
  if (kind === 'system') return t('traj.group.system')
  if (kind === 'compacted') return t('traj.group.compacted')
  return t('traj.group.step', { n: step ?? 1 })
}

function locate(records: TrajRecord[], t: TFn): Map<string, { turn: number; step?: number; group: string }> {
  const out = new Map<string, { turn: number; step?: number; group: string }>()
  const stepByTurn = new Map<number, number>()
  for (const r of records) {
    if (r.kind === 'assistant') {
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
    const bits = [r.name ? `# ${r.name}` : '', pretty(r.input), pretty(r.output)].filter(Boolean)
    return bits.join('\n\n')
  }
  if (typeof r.output === 'string' && r.output) return r.output
  if (typeof r.input === 'string' && r.input) return r.input
  return r.preview || ''
}

function MarkdownBody({ text }: { text: string }) {
  if (!text) return <p className="insp-empty">—</p>
  return <div className="insp-md md" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
}

const KIND_LABEL: Record<TrajKind, string> = {
  user: 'USER',
  assistant: 'ASSISTANT',
  tool: 'TOOL',
  compacted: 'COMPACTED',
  system: 'SYSTEM',
}

function KindIcon({ kind }: { kind: TrajKind }) {
  if (kind === 'user') return <IUser />
  if (kind === 'tool') return <IWrench />
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
            {tool.description ? <p className="tool-cat-desc">{tool.description}</p> : null}
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

export function TrajectoryView({
  records, onSelect, selectId,
}: {
  records: TrajRecord[]
  onSelect?: (r: TrajRecord | null) => void
  selectId?: string | null
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
  const tableRef = useRef<HTMLDivElement>(null)
  const drag = useRef<{ i: number } | null>(null)

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

  const visible = useMemo(() => {
    return filtered.filter(r => {
      if (foldedTurns.has(r.turn) && r.kind !== 'user' && r.kind !== 'system') return false
      if (foldTools && r.kind === 'tool') return false
      return true
    })
  }, [filtered, foldedTurns, foldTools])

  useEffect(() => {
    applyFollowTail(tableRef.current, follow)
  }, [follow, records, visible])

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
    const row = tableRef.current?.querySelector(`[data-rid="${sel}"]`)
    row?.scrollIntoView({ block: 'nearest' })
  }, [sel, visible])

  const grouped: { turn: number; rows: { rec: TrajRecord; index: number }[] }[] = []
  visible.forEach((rec) => {
    const index = records.indexOf(rec)
    const last = grouped[grouped.length - 1]
    if (!last || last.turn !== rec.turn) grouped.push({ turn: rec.turn, rows: [{ rec, index }] })
    else last.rows.push({ rec, index })
  })

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

  return (
    <div className="traj" data-testid="trajectory">
      <div className="traj-toolbar">
        <ISearch />
        <input className="traj-search" placeholder={t('traj.search')} value={q} onChange={e => setQ(e.target.value)} />
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
            setFollow(v => {
              const next = !v
              if (next) applyFollowTail(tableRef.current, true)
              return next
            })
          }}
        >
          <ITail />
        </button>
        <span className="asst-meta">{t('traj.records', { n: visible.length })}</span>
      </div>
      <div
        className="timeline"
        data-testid="traj-timeline"
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
        {records.map((r, i) => (
          <button
            key={r.id}
            type="button"
            data-i={i}
            className={`tl-bar ${r.kind}${sel === r.id ? ' on' : ''}${q && !filtered.includes(r) ? ' dim' : ''}`}
            style={{ flexGrow: actualDur ? Math.max(1, Math.round(((r.durationMs ?? 400) * zoom) / 200)) : Math.max(1, zoom) }}
            title={`${KIND_LABEL[r.kind]} ${r.preview}`}
            onClick={() => pick(r)}
          />
        ))}
      </div>
      {range ? <button type="button" className="chip" onClick={() => setRange(null)}>{t('traj.clearRange')}</button> : null}
      <div className={`traj-split${selected ? '' : ' no-insp'}`}>
        <div
          className="traj-table-wrap"
          data-testid="traj-table-wrap"
          ref={tableRef}
          onScroll={e => {
            const el = e.currentTarget
            const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 8
            setFollow(atBottom)
          }}
        >
          <table className="traj-table">
            <tbody>
              {grouped.map(g => (
                <Fragment key={`t-${g.turn}`}>
                  <tr className="turn-h">
                    <td>
                      <button type="button" className="turn-fold" onClick={() => setFoldedTurns(s => {
                        const n = new Set(s)
                        if (n.has(g.turn)) n.delete(g.turn)
                        else n.add(g.turn)
                        return n
                      })}
                      >
                        <IChev open={!foldedTurns.has(g.turn)} /> TURN {g.turn}
                      </button>
                    </td>
                  </tr>
                  {g.rows.map(({ rec, index }) => (
                    <tr
                      key={rec.id}
                      data-rid={rec.id}
                      className={`traj-row${sel === rec.id ? ' sel' : ''}`}
                      data-testid="traj-row"
                      data-kind={rec.kind}
                      onClick={() => pick(rec)}
                    >
                      <td>
                        <div className="cell">
                          <span className="idx">{index + 1}</span>
                          <span className={`tag ${rec.kind}`}><KindIcon kind={rec.kind} />&nbsp;{KIND_LABEL[rec.kind]}</span>
                          <span className="preview">{rec.running ? '… ' : ''}{rec.preview || rec.name || '—'}</span>
                          <span className="dur">{rec.running ? '' : fmtDur(rec.durationMs)}</span>
                        </div>
                      </td>
                    </tr>
                  ))}
                </Fragment>
              ))}
              {visible.length === 0 ? (
                <tr><td><div className="cell"><span className="preview">{t('traj.empty')}</span></div></td></tr>
              ) : null}
            </tbody>
          </table>
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
