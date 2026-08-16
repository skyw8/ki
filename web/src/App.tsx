import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client, boot } from './api'
import { ChatView } from './Chat'
import { TrajectoryView } from './Trajectory'
import { IClose, IGear, IPanel, IPlus, ISend, IStop } from './icons'
import { appendOptimisticUser, applyEvent, emptyView, loadHistory } from './model'
import type { ModelInfo, SessionInfo, ViewState } from './types'

type Tab = 'conversation' | 'trajectory'

function basename(p: string): string {
  const s = p.replace(/[\\/]+$/, '')
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return i >= 0 ? s.slice(i + 1) : s
}

function csv(list?: string[]): string {
  return (list ?? []).join(', ')
}

function parseCsv(s: string): string[] {
  return s.split(/[, \n]+/).map(x => x.trim()).filter(Boolean)
}

function Modal({ title, onClose, children, testid }: { title: string; onClose: () => void; children: ReactNode; testid?: string }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', onKey)
      document.body.style.overflow = ''
    }
  }, [onClose])
  return createPortal(
    <div className="modal-mask" onClick={onClose} data-testid={testid ? `${testid}-mask` : undefined}>
      <div className="modal" data-testid={testid} onClick={e => e.stopPropagation()} role="dialog" aria-label={title}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button type="button" className="icon-btn" onClick={onClose} aria-label="关闭对话框"><IClose /></button>
        </div>
        <div className="modal-body">{children}</div>
      </div>
    </div>,
    document.body,
  )
}

function Composer({
  value, onChange, onSend, onStop, busy, disabled, hero, cwd, model, err, onPickModel,
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
  onPickModel?: () => void
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
          <button type="button" className="model-chip" data-testid="open-model" onClick={onPickModel}>
            {model || '选择模型'}
          </button>
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
  const [dark, setDark] = useState(() => localStorage.getItem('ki-theme') === 'dark')
  const [collapsed, setCollapsed] = useState(false)
  const [tab, setTab] = useState<Tab>('conversation')
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [filter, setFilter] = useState('')
  const [currentId, setCurrentId] = useState<string | null>(null)
  const [view, setView] = useState<ViewState>(emptyView)
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [skillDis, setSkillDis] = useState('')
  const [mcpOnly, setMcpOnly] = useState('')
  const [mcpDis, setMcpDis] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [modelOpen, setModelOpen] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    document.body.dataset.theme = dark ? 'dark' : 'light'
    document.body.toggleAttribute('data-ds-dark-theme', dark)
    localStorage.setItem('ki-theme', dark ? 'dark' : 'light')
  }, [dark])

  useEffect(() => {
    void api.models().then(setModels).catch(() => setModels([]))
  }, [api])

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
    setCurrentId(id)
    setErr(null)
    abortRef.current?.abort()
    try {
      const detail = await api.get(id)
      const next = loadHistory(detail)
      setView(next)
      setSkillDis(csv(next.skills?.disabled))
      setMcpOnly(csv(next.mcp?.only))
      setMcpDis(csv(next.mcp?.disabled))
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
        const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
        await api.prompt(id, text, spec || undefined)
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) throw e
      }
      void listen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, cfg.cwd, currentId, draft, listen, refreshList, view.model, view.provider])

  const stop = useCallback(async () => {
    if (!currentId) return
    try { await api.abort(currentId) } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId])

  const switchModel = useCallback(async (spec: string) => {
    const [p, m] = spec.includes('/') ? spec.split('/') : [view.provider, spec]
    setView(v => ({ ...v, provider: p || v.provider, model: m || v.model }))
    setModelOpen(false)
    if (!currentId) return
    try {
      const out = await api.patch(currentId, { model: spec })
      setView(v => ({ ...v, model: out.model, provider: out.provider }))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId, view.provider])

  const saveToggles = useCallback(async () => {
    if (!currentId) return
    try {
      const out = await api.patch(currentId, {
        skills: { disabled: parseCsv(skillDis) },
        mcp: { only: parseCsv(mcpOnly), disabled: parseCsv(mcpDis) },
      })
      setView(v => ({ ...v, skills: out.skills, mcp: out.mcp }))
      setSettingsOpen(false)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId, skillDis, mcpOnly, mcpDis])

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
  const composer = (
    <Composer
      value={draft}
      onChange={setDraft}
      onSend={() => void send()}
      onStop={() => void stop()}
      busy={view.busy}
      cwd={view.cwd || cfg.cwd}
      model={view.model}
      err={err}
      onPickModel={() => setModelOpen(true)}
    />
  )

  return (
    <div className={`app${collapsed ? ' sidebar-collapsed' : ''}${(settingsOpen || modelOpen) ? ' modal-open' : ''}`}>
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
          <button type="button" className="icon-btn" data-testid="open-settings" onClick={() => setSettingsOpen(true)} aria-label="设置"><IGear /></button>
        </div>
      </aside>

      <main className="main">
        <header className="conv-header">
          <div className="title-row">
            <div className="conv-title">{view.title || (currentId ? '新会话' : 'ki')}</div>
          </div>
          <div className="tabs">
            <button type="button" className={`tab${tab === 'conversation' ? ' active' : ''}`} data-testid="tab-conversation" onClick={() => setTab('conversation')}>对话</button>
            <button type="button" className={`tab${tab === 'trajectory' ? ' active' : ''}`} data-testid="tab-trajectory" onClick={() => setTab('trajectory')}>轨迹</button>
          </div>
        </header>

        {tab === 'trajectory' ? (
          <div className="conv-body">
            <TrajectoryView records={view.records} />
            {composer}
          </div>
        ) : (
          <div className="conv-body">
            {empty ? (
              <div className="hero" data-testid="hero">
                <h1>开始对话</h1>
                <div className="hero-composer">{composer}</div>
              </div>
            ) : (
              <>
                <div className="scroll">
                  <ChatView nodes={view.nodes} busy={view.busy} />
                </div>
                {composer}
              </>
            )}
          </div>
        )}
      </main>

      {modelOpen ? (
        <Modal title="选择模型" onClose={() => setModelOpen(false)} testid="model-dialog">
          <ul className="model-list">
            {models.map(m => {
              const on = view.provider === m.provider && view.model === m.id
              return (
                <li key={m.spec}>
                  <button
                    type="button"
                    className={`model-opt${on ? ' on' : ''}`}
                    data-testid="model-option"
                    data-spec={m.spec}
                    onClick={() => void switchModel(m.spec)}
                  >
                    <span className="model-opt-id">{m.id}</span>
                    <span className="model-opt-p">{m.provider}</span>
                  </button>
                </li>
              )
            })}
          </ul>
        </Modal>
      ) : null}

      {settingsOpen ? (
        <Modal title="设置" onClose={() => setSettingsOpen(false)} testid="settings">
          <div className="set-block">
            <div className="set-label">外观</div>
            <div className="theme-picks" data-testid="settings-theme">
              <button type="button" className={`theme-pick${!dark ? ' on' : ''}`} onClick={() => setDark(false)}>
                <span className="theme-swatch light" aria-hidden />
                <span>浅色</span>
              </button>
              <button type="button" className={`theme-pick${dark ? ' on' : ''}`} onClick={() => setDark(true)}>
                <span className="theme-swatch dark" aria-hidden />
                <span>深色</span>
              </button>
            </div>
            <p className="set-hint">立即生效，只保存在本浏览器。</p>
          </div>
          <div className="set-block">
            <div className="set-label">工作目录</div>
            <p className="set-path">{view.cwd || cfg.cwd}</p>
          </div>
          <div className={`set-group${!currentId ? ' disabled' : ''}`}>
            <div className="set-group-h">本会话</div>
            <div className="set-block">
              <div className="set-label">关闭的 Skills</div>
              <input data-testid="settings-skills" placeholder="逗号分隔，如 skill-a, skill-b" value={skillDis} disabled={!currentId} onChange={e => setSkillDis(e.target.value)} />
            </div>
            <div className="set-block">
              <div className="set-label">仅启用的 MCP</div>
              <input data-testid="settings-mcp-only" placeholder="留空表示全部" value={mcpOnly} disabled={!currentId} onChange={e => setMcpOnly(e.target.value)} />
            </div>
            <div className="set-block">
              <div className="set-label">关闭的 MCP</div>
              <input data-testid="settings-mcp-dis" placeholder="逗号分隔" value={mcpDis} disabled={!currentId} onChange={e => setMcpDis(e.target.value)} />
            </div>
            {!currentId ? <p className="set-hint">先选择或新建会话后再改这些开关。</p> : null}
          </div>
          <div className="modal-actions">
            <button type="button" className="primary-btn" data-testid="settings-save" disabled={!currentId} onClick={() => void saveToggles()}>保存</button>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}
