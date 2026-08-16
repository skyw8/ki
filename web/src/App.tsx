import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ApiError, Client, boot } from './api'
import { ChatView } from './Chat'
import { TrajectoryView } from './Trajectory'
import { IFork, IMoon, IPanel, IPlus, ISend, IStop, ISun } from './icons'
import { appendOptimisticUser, applyEvent, emptyView, loadHistory, usageTotals } from './model'
import type { SessionInfo, ViewState } from './types'

type Page = 'chat' | 'settings'
type Tab = 'conversation' | 'trajectory'

function basename(p: string): string {
  const s = p.replace(/[\\/]+$/, '')
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return i >= 0 ? s.slice(i + 1) : s
}

function Composer({
  value, onChange, onSend, onStop, busy, disabled, hero, cwd, model, err,
}: {
  value: string
  onChange: (v: string) => void
  onSend: () => void
  onStop: () => void
  busy: boolean
  disabled?: boolean
  hero?: boolean
  cwd?: string
  model?: string
  err?: string | null
}) {
  const ref = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`
  }, [value])
  return (
    <div className={`composer-wrap${hero ? ' hero-pos' : ''}`}>
      {err ? <div className="notice" data-testid="notice">{err}</div> : null}
      <div className="composer">
        <textarea
          ref={ref}
          data-testid="composer-input"
          rows={1}
          placeholder={disabled ? '选择或新建会话' : '给 ki 发送消息'}
          value={value}
          disabled={disabled}
          onChange={e => onChange(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              if (busy) onStop()
              else onSend()
            }
          }}
        />
        <div className="composer-row">
          {cwd ? <span className="cwd-chip" title={cwd}>{basename(cwd)}</span> : null}
          {model ? <span className="model-chip">{model}</span> : null}
          <span className="grow" />
          {busy ? (
            <button type="button" className="send stop" data-testid="composer-stop" onClick={onStop} aria-label="停止"><IStop /></button>
          ) : (
            <button type="button" className="send" data-testid="composer-send" disabled={disabled || !value.trim()} onClick={onSend} aria-label="发送"><ISend /></button>
          )}
        </div>
      </div>
    </div>
  )
}

export function App() {
  const cfg = useMemo(() => boot(), [])
  const api = useMemo(() => new Client(cfg.token), [cfg.token])
  const [dark, setDark] = useState(true)
  const [collapsed, setCollapsed] = useState(false)
  const [page, setPage] = useState<Page>('chat')
  const [tab, setTab] = useState<Tab>('conversation')
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [filter, setFilter] = useState('')
  const [currentId, setCurrentId] = useState<string | null>(null)
  const [view, setView] = useState<ViewState>(emptyView)
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    document.body.dataset.theme = dark ? 'dark' : 'light'
    document.body.toggleAttribute('data-ds-dark-theme', dark)
  }, [dark])

  const refreshList = useCallback(async () => {
    try {
      setSessions(await api.list())
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api])

  useEffect(() => { void refreshList() }, [refreshList])

  const listen = useCallback(async (id: string) => {
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    try {
      for await (const ev of api.events(id, ac.signal)) {
        setView(v => (abortRef.current === ac ? applyEvent(v, ev) : v))
      }
    } catch (e) {
      if ((e as { name?: string }).name === 'AbortError') return
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      if (abortRef.current === ac) {
        setView(v => ({ ...v, busy: false }))
        void refreshList()
      }
    }
  }, [api, refreshList])

  const openSession = useCallback(async (id: string) => {
    setPage('chat')
    setCurrentId(id)
    setErr(null)
    abortRef.current?.abort()
    try {
      const detail = await api.get(id)
      const next = loadHistory(detail)
      setView(next)
      if (detail.running) void listen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, listen])

  const newSession = useCallback(async () => {
    setErr(null)
    try {
      const s = await api.create(cfg.cwd)
      await refreshList()
      setCurrentId(s.id)
      setView({ ...emptyView(), cwd: s.cwd, model: s.model, provider: s.provider })
      setPage('chat')
      setTab('conversation')
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, cfg.cwd, refreshList])

  const send = useCallback(async () => {
    const text = draft.trim()
    if (!text) return
    setErr(null)
    try {
      let id = currentId
      if (!id) {
        const s = await api.create(cfg.cwd)
        id = s.id
        setCurrentId(id)
        setView(v => ({ ...v, cwd: s.cwd, model: s.model, provider: s.provider }))
        await refreshList()
      }
      setDraft('')
      setView(v => appendOptimisticUser(v, text))
      try {
        await api.prompt(id, text)
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) throw e
      }
      void listen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, cfg.cwd, currentId, draft, listen, refreshList])

  const stop = useCallback(async () => {
    if (!currentId) return
    try { await api.abort(currentId) } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId])

  const fork = useCallback(async () => {
    if (!currentId) return
    try {
      const s = await api.fork(currentId)
      await refreshList()
      await openSession(s.id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId, openSession, refreshList])

  const groups = useMemo(() => {
    const q = filter.trim().toLowerCase()
    const rows = sessions.filter(s => {
      if (!q) return true
      return (s.title || '').toLowerCase().includes(q) || s.cwd.toLowerCase().includes(q) || s.id.includes(q)
    })
    const map = new Map<string, SessionInfo[]>()
    for (const s of rows) {
      const arr = map.get(s.cwd) ?? []
      arr.push(s)
      map.set(s.cwd, arr)
    }
    return [...map.entries()]
  }, [sessions, filter])

  const empty = view.nodes.length === 0
  const usage = usageTotals(view)

  return (
    <div className={`app${collapsed ? ' sidebar-collapsed' : ''}`}>
      <aside className="sidebar">
        <div className="sidebar-top">
          {!collapsed ? <div className="wordmark">ki</div> : null}
          <button type="button" className="icon-btn" onClick={() => setCollapsed(v => !v)} aria-label="折叠侧栏"><IPanel /></button>
        </div>
        <button type="button" className="new-session" data-testid="new-session" onClick={() => void newSession()}>
          <IPlus />{!collapsed ? '新会话' : null}
        </button>
        {!collapsed ? (
          <input className="session-search" placeholder="搜索会话" value={filter} onChange={e => setFilter(e.target.value)} />
        ) : null}
        <div className="session-list">
          {!collapsed && groups.map(([cwd, rows]) => (
            <div key={cwd}>
              <div className="cwd-label" title={cwd}>{basename(cwd)}</div>
              {rows.map(s => (
                <button
                  key={s.id}
                  type="button"
                  className={`session-row${s.id === currentId ? ' active' : ''}`}
                  data-testid="session-row"
                  onClick={() => void openSession(s.id)}
                >
                  <span className={`dot${s.running ? ' on' : ''}`} />
                  <span className="meta">
                    <div className="title" data-testid="session-title">{s.title || '新会话'}</div>
                    <div className="sub">{s.model}</div>
                  </span>
                </button>
              ))}
            </div>
          ))}
        </div>
        <div className="sidebar-foot">
          <button type="button" className="icon-btn" onClick={() => setDark(v => !v)} aria-label="主题">
            {dark ? <ISun /> : <IMoon />}
          </button>
          {!collapsed ? <span className="grow" /> : null}
          <button type="button" className="icon-btn" data-testid="open-settings" onClick={() => setPage('settings')} aria-label="设置">⚙</button>
        </div>
      </aside>

      <main className="main">
        {page === 'settings' ? (
          <div className="settings" data-testid="settings">
            <h2>设置</h2>
            <p>设置项稍后接入</p>
          </div>
        ) : (
          <>
            <header className="conv-header">
              <div className="conv-title">{view.title || (currentId ? '新会话' : 'ki')}</div>
              <div className="tabs">
                <button type="button" className={`tab${tab === 'conversation' ? ' active' : ''}`} data-testid="tab-conversation" onClick={() => setTab('conversation')}>对话</button>
                <button type="button" className={`tab${tab === 'trajectory' ? ' active' : ''}`} data-testid="tab-trajectory" onClick={() => setTab('trajectory')}>轨迹</button>
              </div>
              <div className="header-actions">
                <button type="button" className="icon-btn" disabled={!currentId} onClick={() => void fork()} aria-label="派生"><IFork /></button>
              </div>
            </header>

            {tab === 'trajectory' ? (
              <div className="conv-body">
                <TrajectoryView records={view.records} />
                <Composer value={draft} onChange={setDraft} onSend={() => void send()} onStop={() => void stop()} busy={view.busy} cwd={view.cwd || cfg.cwd} model={view.model} err={err} />
              </div>
            ) : (
              <div className="conv-body">
                {empty ? (
                  <div className="hero" data-testid="hero">
                    <h1>开始对话</h1>
                    <Composer hero value={draft} onChange={setDraft} onSend={() => void send()} onStop={() => void stop()} busy={view.busy} cwd={view.cwd || cfg.cwd} model={view.model} err={err} />
                  </div>
                ) : (
                  <>
                    <div className="scroll">
                      <ChatView nodes={view.nodes} busy={view.busy} />
                      {(usage.input || usage.output) ? (
                        <div className="chat-col"><div className="status-line">{usage.input} in · {usage.output} out{usage.cacheRead ? ` · cache ${usage.cacheRead}` : ''}</div></div>
                      ) : null}
                    </div>
                    <Composer value={draft} onChange={setDraft} onSend={() => void send()} onStop={() => void stop()} busy={view.busy} cwd={view.cwd || cfg.cwd} model={view.model} err={err} />
                  </>
                )}
              </div>
            )}
          </>
        )}
      </main>
    </div>
  )
}
