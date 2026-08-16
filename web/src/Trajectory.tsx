import { Fragment, useMemo, useState } from 'react'
import { IClose, ICompact, ISearch, ISpark, IUser, IWrench } from './icons'
import type { TrajKind, TrajRecord } from './types'

const KIND_LABEL: Record<TrajKind, string> = {
  user: 'USER',
  assistant: 'ASSISTANT',
  tool: 'TOOL',
  compacted: 'COMPACTED',
}

function KindIcon({ kind }: { kind: TrajKind }) {
  if (kind === 'user') return <IUser />
  if (kind === 'tool') return <IWrench />
  if (kind === 'compacted') return <ICompact />
  return <ISpark />
}

function fmtDur(ms?: number): string {
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

export function TrajectoryView({ records }: { records: TrajRecord[] }) {
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  const [tab, setTab] = useState<'overview' | 'input' | 'output'>('overview')

  const filtered = useMemo(() => {
    const s = q.trim().toLowerCase()
    if (!s) return records
    return records.filter(r =>
      r.preview.toLowerCase().includes(s)
      || r.kind.includes(s)
      || (r.name ?? '').toLowerCase().includes(s),
    )
  }, [records, q])

  const selected = records.find(r => r.id === sel) ?? null

  const grouped: { turn: number; rows: { rec: TrajRecord; index: number }[] }[] = []
  filtered.forEach((rec, i) => {
    const last = grouped[grouped.length - 1]
    if (!last || last.turn !== rec.turn) grouped.push({ turn: rec.turn, rows: [{ rec, index: i }] })
    else last.rows.push({ rec, index: i })
  })

  return (
    <div className="traj" data-testid="trajectory">
      <div className="traj-toolbar">
        <ISearch />
        <input className="traj-search" placeholder="搜索轨迹" value={q} onChange={e => setQ(e.target.value)} />
        <span className="asst-meta">{filtered.length} 条记录</span>
      </div>
      <div className="timeline" aria-hidden>
        {records.map(r => (
          <button
            key={r.id}
            type="button"
            className={`tl-bar ${r.kind}${sel === r.id ? ' on' : ''}${q && !filtered.includes(r) ? ' dim' : ''}`}
            style={{ flexGrow: Math.max(1, Math.round((r.durationMs ?? 400) / 200)) }}
            title={`${KIND_LABEL[r.kind]} ${r.preview}`}
            onClick={() => { setSel(r.id); setTab('overview') }}
          />
        ))}
      </div>
      <div className={`traj-split${selected ? '' : ' no-insp'}`}>
        <div className="traj-table-wrap">
          <table className="traj-table">
            <tbody>
              {grouped.map(g => (
                <Fragment key={`t-${g.turn}`}>
                  <tr className="turn-h"><td>TURN {g.turn}</td></tr>
                  {g.rows.map(({ rec, index }) => (
                    <tr
                      key={rec.id}
                      className={`traj-row${sel === rec.id ? ' sel' : ''}`}
                      data-testid="traj-row"
                      data-kind={rec.kind}
                      onClick={() => { setSel(rec.id); setTab('overview') }}
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
              {filtered.length === 0 ? (
                <tr><td><div className="cell"><span className="preview">没有匹配的记录</span></div></td></tr>
              ) : null}
            </tbody>
          </table>
        </div>
        {selected ? (
          <aside className="insp">
            <div className="insp-head">
              <span className={`tag ${selected.kind}`}>{KIND_LABEL[selected.kind]}</span>
              <div className="grow">{selected.name || KIND_LABEL[selected.kind]}</div>
              <button type="button" className="icon-btn" onClick={() => setSel(null)} aria-label="关闭"><IClose /></button>
            </div>
            <div className="insp-tabs">
              {(['overview', 'input', 'output'] as const).map(id => (
                <button key={id} type="button" className={tab === id ? 'on' : ''} onClick={() => setTab(id)}>
                  {id === 'overview' ? '概览' : id === 'input' ? 'Input' : 'Output'}
                </button>
              ))}
            </div>
            <div className="insp-body">
              {tab === 'overview' ? (
                <dl>
                  <div><dt>Started</dt><dd>{fmtTime(selected.startedAt)}</dd></div>
                  <div><dt>Duration</dt><dd>{selected.running ? '进行中' : (fmtDur(selected.durationMs) || '—')}</dd></div>
                  {selected.ttftMs != null ? <div><dt>TTFT</dt><dd>{fmtDur(selected.ttftMs)}</dd></div> : null}
                  {selected.usage ? (
                    <>
                      <div><dt>Input tokens</dt><dd>{selected.usage.input ?? 0}</dd></div>
                      <div><dt>Output tokens</dt><dd>{selected.usage.output ?? 0}</dd></div>
                      <div><dt>Cache read</dt><dd>{selected.usage.cacheRead ?? 0}</dd></div>
                    </>
                  ) : null}
                  {selected.error ? <div><dt>Status</dt><dd>error</dd></div> : null}
                </dl>
              ) : null}
              {tab === 'input' ? <pre>{pretty(selected.input) || '—'}</pre> : null}
              {tab === 'output' ? <pre>{pretty(selected.output) || '—'}</pre> : null}
            </div>
          </aside>
        ) : null}
      </div>
    </div>
  )
}
