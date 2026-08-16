import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client, boot } from './api'
import { ChatView } from './Chat'
import { DirectoryBrowser } from './DirectoryBrowser'
import { SessionConfig } from './SessionConfig'
import { IChev, IChevDown, IClose, IDots, IEdit, IFolder, IGear, IPanel, IPin, IPlus, ISearch, ISend, IStop, ITrash } from './icons'
import { appendOptimisticUser, applyEvent, emptyView, loadHistory } from './model'
import type { ChatNode, ModelInfo, SearchHit, SessionInfo, ViewState, WorkspaceInfo } from './types'
import { TrajectoryView } from './Trajectory'
import { useI18n } from './i18n'

type Tab = 'conversation' | 'trajectory' | 'config'
const SHOW = 5
const EXPAND_KEY = 'ki-ws-expanded'

function basename(p: string): string {
  const s = p.replace(/[\\/]+$/, '')
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return i >= 0 ? s.slice(i + 1) : s
}

function loadExpanded(): Record<string, boolean> {
  try { return JSON.parse(localStorage.getItem(EXPAND_KEY) || '{}') as Record<string, boolean> }
  catch { return {} }
}

function Modal({ title, onClose, children, testid }: { title: string; onClose: () => void; children: ReactNode; testid?: string }) {
  const { t } = useI18n()
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
          <button type="button" className="icon-btn" onClick={onClose} aria-label={t('dialog.close')}><IClose /></button>
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
  const { t } = useI18n()
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
          placeholder={disabled ? t('composer.placeholderDisabled') : t('composer.placeholder')}
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
            {model || t('composer.pickModel')}
          </button>
          <span className="grow" />
          {busy ? (
            <button type="button" className="send stop" data-testid="composer-stop" onClick={onStop} aria-label={t('composer.stop')}><IStop /></button>
          ) : (
            <button type="button" className="send" data-testid="composer-send" disabled={disabled || !value.trim()} onClick={onSend} aria-label={t('composer.send')}><ISend /></button>
          )}
        </div>
      </div>
    </div>
  )
}

export function App() {
  const { t, lang, setLang } = useI18n()
  const untitled = t('session.untitled')
  const cfg = useMemo(() => boot(), [])
  const api = useMemo(() => new Client(cfg.token), [cfg.token])
  const [dark, setDark] = useState(() => localStorage.getItem('ki-theme') === 'dark')
  const [collapsed, setCollapsed] = useState(false)
  const [settled, setSettled] = useState(false)
  const everWide = useRef(true)
  const [tab, setTab] = useState<Tab>('conversation')
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [workspaces, setWorkspaces] = useState<WorkspaceInfo[]>([])
  const [filter, setFilter] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [hits, setHits] = useState<SearchHit[]>([])
  const [searchMore, setSearchMore] = useState(false)
  const [searchErr, setSearchErr] = useState<string | null>(null)
  const [currentId, setCurrentId] = useState<string | null>(null)
  const [selectedWs, setSelectedWs] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>(loadExpanded)
  const [showAll, setShowAll] = useState<Record<string, boolean>>({})
  const [view, setView] = useState<ViewState>(emptyView)
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [modelOpen, setModelOpen] = useState(false)
  const [dirOpen, setDirOpen] = useState(false)
  const [dirBusy, setDirBusy] = useState(false)
  const [menu, setMenu] = useState<{ kind: 'ws' | 'sess'; id: string } | null>(null)
  const [rename, setRename] = useState<{ kind: 'ws' | 'sess'; id: string; title: string } | null>(null)
  const [confirmDel, setConfirmDel] = useState<{ kind: 'ws' | 'sess'; id: string; label: string; extra?: string } | null>(null)
  const [inspId, setInspId] = useState<string | null>(null)
  const [atBottom, setAtBottom] = useState(true)
  const abortRef = useRef<AbortController | null>(null)
  const searchAc = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const searchRootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    document.body.dataset.theme = dark ? 'dark' : 'light'
    document.body.toggleAttribute('data-ds-dark-theme', dark)
    localStorage.setItem('ki-theme', dark ? 'dark' : 'light')
  }, [dark])

  useEffect(() => { localStorage.setItem(EXPAND_KEY, JSON.stringify(expanded)) }, [expanded])

  useEffect(() => {
    if (!collapsed) {
      everWide.current = true
      setSettled(false)
      return
    }
    const t = window.setTimeout(() => setSettled(true), 150)
    return () => window.clearTimeout(t)
  }, [collapsed])
  const wide = !collapsed || !settled

  useEffect(() => {
    if (!wide || !(searchOpen || filter)) return
    const t = window.setTimeout(() => searchInputRef.current?.focus(), collapsed ? 300 : 0)
    return () => window.clearTimeout(t)
  }, [wide, searchOpen, filter, collapsed])

  useEffect(() => {
    if (!(searchOpen || filter)) return
    const onDown = (e: MouseEvent) => {
      if (searchRootRef.current?.contains(e.target as Node)) return
      if (!filter.trim()) setSearchOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [searchOpen, filter])

  useEffect(() => {
    void api.models().then(setModels).catch(() => setModels([]))
  }, [api])

  const refreshList = useCallback(async () => {
    try {
      const [ss, ws] = await Promise.all([api.list(), api.workspaces()])
      setSessions(ss)
      setWorkspaces(ws)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api])

  useEffect(() => { void refreshList() }, [refreshList])

  useEffect(() => {
    const q = filter.trim()
    if (!q) {
      setHits([])
      setSearchMore(false)
      setSearchErr(null)
      return
    }
    const t = window.setTimeout(() => {
      searchAc.current?.abort()
      const ac = new AbortController()
      searchAc.current = ac
      void api.search(q, ac.signal).then(out => {
        setHits(out.items)
        setSearchMore(out.hasMore)
        setSearchErr(null)
      }).catch(e => {
        if ((e as { name?: string }).name === 'AbortError') return
        setSearchErr(e instanceof Error ? e.message : String(e))
      })
    }, 250)
    return () => window.clearTimeout(t)
  }, [api, filter])

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
      setSelectedWs(detail.workspaceId ?? null)
      if (detail.workspaceId) setExpanded(e => ({ ...e, [detail.workspaceId!]: true }))
      if (detail.running) void listen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, listen])

  const makeSession = useCallback(async (workspaceId?: string | null) => {
    const s = await api.create(workspaceId ? { workspaceId } : {})
    await refreshList()
    setCurrentId(s.id)
    setSelectedWs(s.workspaceId ?? workspaceId ?? null)
    setView({ ...emptyView(), cwd: s.cwd, model: s.model, provider: s.provider })
    setTab('conversation')
    if (s.workspaceId) {
      setExpanded(e => ({ ...e, [s.workspaceId!]: true }))
      setShowAll(a => ({ ...a, [s.workspaceId!]: true }))
    }
    return s
  }, [api, refreshList])

  const newSession = useCallback(async (wsId?: string) => {
    setErr(null)
    try { await makeSession(wsId ?? selectedWs) }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
  }, [makeSession, selectedWs])

  const send = useCallback(async () => {
    const text = draft.trim()
    if (!text) return
    setErr(null)
    try {
      let id = currentId
      if (!id) {
        const s = await makeSession(selectedWs)
        id = s.id
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
  }, [api, currentId, draft, listen, makeSession, selectedWs, view.model, view.provider])

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

  const byId = useMemo(() => new Map(sessions.map(s => [s.id, s])), [sessions])

  const trees = useMemo(() => {
    const used = new Set<string>()
    const groups = workspaces.map(ws => {
      const order = ws.sessionIds?.length
        ? [...ws.sessionIds, ...sessions.filter(s => s.workspaceId === ws.id && !ws.sessionIds!.includes(s.id)).map(s => s.id)]
        : sessions.filter(s => s.workspaceId === ws.id).map(s => s.id)
      const rows = order.map(id => byId.get(id)).filter((s): s is SessionInfo => !!s)
      rows.forEach(s => used.add(s.id))
      return { ws, rows }
    })
    const ungrouped = sessions.filter(s => !used.has(s.id))
    return { groups, ungrouped }
  }, [byId, sessions, workspaces])

  const localHits = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return []
    return sessions.filter(s => {
      if (!s.title) return false
      const ws = workspaces.find(w => w.id === s.workspaceId)
      return s.title.toLowerCase().includes(q) || s.id.includes(q) || (ws?.title.toLowerCase().includes(q) ?? false) || s.cwd.toLowerCase().includes(q)
    }).slice(0, 20)
  }, [filter, sessions, workspaces])

  const mergedHits = useMemo(() => {
    const map = new Map<string, { id: string; title: string; workspace?: string; snippet?: string }>()
    for (const s of localHits) {
      const ws = workspaces.find(w => w.id === s.workspaceId)
      map.set(s.id, { id: s.id, title: s.title || untitled, workspace: ws?.title })
    }
    for (const h of hits) {
      const prev = map.get(h.id)
      map.set(h.id, { id: h.id, title: h.title || prev?.title || untitled, workspace: h.workspaceTitle || prev?.workspace, snippet: h.snippet })
    }
    return [...map.values()].slice(0, 20)
  }, [hits, localHits, untitled, workspaces])

  useEffect(() => {
    const el = scrollRef.current
    if (!el || !atBottom) return
    el.scrollTop = el.scrollHeight
  }, [view.nodes, atBottom])

  const inspect = (n: ChatNode) => {
    setInspId(n.id)
    setTab('trajectory')
  }

  const empty = view.nodes.length === 0
  const composer = (
    <Composer
      value={draft}
      onChange={setDraft}
      onSend={() => void send()}
      onStop={() => void stop()}
      busy={view.busy}
      cwd={view.cwd}
      model={view.model}
      err={err}
      onPickModel={() => setModelOpen(true)}
    />
  )

  const visibleRows = (wsId: string, rows: SessionInfo[]) => {
    if (showAll[wsId]) return rows
    const idx = currentId ? rows.findIndex(s => s.id === currentId) : -1
    const n = idx >= SHOW ? idx + 1 : SHOW
    return rows.slice(0, n)
  }

  return (
    <div className={`app${collapsed ? ' sidebar-collapsed' : ''}${(settingsOpen || modelOpen || dirOpen) ? ' modal-open' : ''}`}>
      <aside
        className={`sidebar${collapsed && wide ? ' fading' : ''}${!wide ? ' rail' : ''}${!wide && everWide.current ? ' rail-in' : ''}`}
        style={wide ? { width: 280 } : undefined}
      >
        <div className="sidebar-top">
          {wide ? <div className="wordmark wide-only">ki</div> : null}
          <button type="button" className="icon-btn" onClick={() => setCollapsed(v => !v)} aria-label={collapsed ? t('sidebar.open') : t('sidebar.collapse')} title={collapsed ? t('sidebar.open') : t('sidebar.collapse')}><IPanel /></button>
        </div>
        <button type="button" className="new-session" data-testid="new-session" onClick={() => void newSession()}>
          <IPlus />{wide ? <span className="wide-only">{t('session.new')}</span> : null}
        </button>
        {wide ? (
          <div className="ws-toolbar">
            <span className={`ws-label${searchOpen || filter ? ' hidden' : ''}`}>{t('workspace.label')}</span>
            <div className={`search-slot${searchOpen || filter ? ' expanded' : ''}`}>
              <div
                ref={searchRootRef}
                className={`search-cap${searchOpen || filter ? ' expanded' : ''}`}
                onClick={() => setSearchOpen(true)}
              >
                <button type="button" className="search-btn" aria-label={t('search.sessions')} aria-expanded={searchOpen || !!filter} title={t('search.sessions')} onClick={() => setSearchOpen(true)}>
                  <ISearch />
                </button>
                <input
                  ref={searchInputRef}
                  className="session-search"
                  data-testid="session-search"
                  placeholder={t('search.sessions')}
                  value={filter}
                  tabIndex={searchOpen || filter ? 0 : -1}
                  onChange={e => setFilter(e.target.value)}
                  onKeyDown={e => {
                    if (e.key !== 'Escape') return
                    setFilter('')
                    setSearchOpen(false)
                  }}
                />
                {searchOpen || filter ? (
                  <button type="button" className="search-clear" aria-label={t('search.clear')} onClick={e => { e.stopPropagation(); setFilter(''); setSearchOpen(false) }}>
                    <IClose />
                  </button>
                ) : null}
              </div>
            </div>
            <div className={`header-actions${searchOpen || filter ? ' hidden' : ''}`}>
              <button type="button" className="icon-btn" data-testid="add-workspace" onClick={() => setDirOpen(true)} aria-label={t('workspace.add')} title={t('workspace.add')}><IFolder /></button>
            </div>
          </div>
        ) : (
          <div className="rail-actions">
            <button
              type="button"
              className="icon-btn"
              aria-label={t('search.sessions')}
              title={t('search.sessions')}
              onClick={() => { setSearchOpen(true); setCollapsed(false) }}
            >
              <ISearch />
            </button>
            <button type="button" className="icon-btn" data-testid="add-workspace" onClick={() => setDirOpen(true)} aria-label={t('workspace.add')} title={t('workspace.add')}><IFolder /></button>
          </div>
        )}
        <div className="session-list">
          {wide && filter.trim() ? (
            <>
              {mergedHits.map(h => (
                <button key={h.id} type="button" className="session-row" data-testid="search-hit" onClick={() => void openSession(h.id)}>
                  <span className="meta">
                    <div className="title">{h.title}</div>
                    <div className="sub">{h.workspace}{h.snippet ? ` · ${h.snippet}` : ''}</div>
                  </span>
                </button>
              ))}
              {searchErr ? <div className="cwd-label">{t('search.failed')}</div> : null}
              {searchMore ? <div className="cwd-label">{t('search.tooMany')}</div> : null}
            </>
          ) : null}
          {wide && !filter.trim() && trees.groups.map(({ ws, rows }) => {
            const open = expanded[ws.id] !== false
            const shown = open ? visibleRows(ws.id, rows) : []
            return (
              <div key={ws.id} data-testid="workspace-group" data-ws={ws.id}>
                <div
                  className="ws-row"
                  data-testid="workspace-row"
                  title={ws.path}
                  draggable
                  onDragStart={e => { e.dataTransfer.setData('text/ws', ws.id) }}
                  onDragOver={e => e.preventDefault()}
                  onDrop={e => {
                    const id = e.dataTransfer.getData('text/ws')
                    if (!id || id === ws.id) return
                    const rect = e.currentTarget.getBoundingClientRect()
                    const i = workspaces.findIndex(w => w.id === ws.id)
                    const before = e.clientY < rect.top + rect.height / 2 ? ws.id : workspaces[i + 1]?.id ?? null
                    void api.moveWorkspace(id, before).then(setWorkspaces).catch(er => setErr(er instanceof Error ? er.message : String(er)))
                  }}
                >
                  <button type="button" className="ws-toggle" onClick={() => { setSelectedWs(ws.id); setExpanded(e => ({ ...e, [ws.id]: !open })) }}>
                    <IChev open={open} />
                    <IFolder />
                    <span className="title">{ws.title}{ws.temp ? ` · ${t('workspace.temp')}` : ''}</span>
                  </button>
                  <button type="button" className="icon-btn tiny" aria-label={t('workspace.menu')} onClick={() => setMenu({ kind: 'ws', id: ws.id })}><IDots /></button>
                  <button type="button" className="icon-btn tiny" data-testid="ws-new-session" aria-label={t('session.new')} onClick={() => void newSession(ws.id)}><IPlus /></button>
                </div>
                {shown.map((s, i) => (
                  <div
                    key={s.id}
                    className={`session-row${s.id === currentId ? ' active' : ''}`}
                    data-testid="session-row"
                    draggable
                    onDragStart={e => { e.dataTransfer.setData('text/sess', `${ws.id}:${s.id}`) }}
                    onDragOver={e => e.preventDefault()}
                    onDrop={e => {
                      const raw = e.dataTransfer.getData('text/sess')
                      const [fromWs, sid] = raw.split(':')
                      if (fromWs !== ws.id || !sid || sid === s.id) return
                      const rect = e.currentTarget.getBoundingClientRect()
                      const before = e.clientY < rect.top + rect.height / 2 ? s.id : rows[i + 1]?.id
                      void api.moveSession(ws.id, sid, before ?? null).then(() => refreshList()).catch(er => setErr(er instanceof Error ? er.message : String(er)))
                    }}
                  >
                    <button type="button" className="session-main" onClick={() => void openSession(s.id)}>
                      <span className={`dot${s.running ? ' on' : ''}`} />
                      {s.pinned ? <span className="pin-mark" aria-label={t('session.pinned')}><IPin /></span> : null}
                      <span className="meta">
                        <div className="title" data-testid="session-title">{s.title || untitled}</div>
                        <div className="sub">{s.model}</div>
                      </span>
                    </button>
                    <button type="button" className="icon-btn tiny" aria-label={t('session.menu')} onClick={() => setMenu({ kind: 'sess', id: s.id })}><IDots /></button>
                  </div>
                ))}
                {open && rows.length > shown.length ? (
                  <button type="button" className="show-more" data-testid="show-more" onClick={() => setShowAll(a => ({ ...a, [ws.id]: true }))}>{t('session.showMore')}</button>
                ) : null}
              </div>
            )
          })}
          {wide && !filter.trim() && trees.ungrouped.length ? (
            <div>
              <div className="cwd-label">{t('session.ungrouped')}</div>
              {trees.ungrouped.map(s => (
                <button key={s.id} type="button" className={`session-row${s.id === currentId ? ' active' : ''}`} data-testid="session-row" onClick={() => void openSession(s.id)}>
                  <span className={`dot${s.running ? ' on' : ''}`} />
                  <span className="meta">
                    <div className="title" data-testid="session-title">{s.title || untitled}</div>
                    <div className="sub">{s.model}</div>
                  </span>
                </button>
              ))}
            </div>
          ) : null}
        </div>
        <div className="sidebar-foot">
          <button
            type="button"
            className="settings-btn"
            data-testid="open-settings"
            onClick={() => setSettingsOpen(true)}
            aria-label={t('settings.open')}
            title={t('settings.open')}
          >
            <IGear />{wide ? <span className="wide-only">{t('settings.open')}</span> : null}
          </button>
        </div>
      </aside>

      <main className="main">
        <header className="conv-header">
          <div className="title-row">
            <div className="conv-title">{view.title || (currentId ? untitled : 'ki')}</div>
          </div>
          <div className="tabs">
            <button type="button" className={`tab${tab === 'conversation' ? ' active' : ''}`} data-testid="tab-conversation" onClick={() => setTab('conversation')}>{t('tab.conversation')}</button>
            <button type="button" className={`tab${tab === 'trajectory' ? ' active' : ''}`} data-testid="tab-trajectory" onClick={() => setTab('trajectory')}>{t('tab.trajectory')}</button>
            <button type="button" className={`tab${tab === 'config' ? ' active' : ''}`} data-testid="tab-config" onClick={() => setTab('config')}>{t('tab.config')}</button>
          </div>
        </header>

        {tab === 'config' ? (
          <div className="conv-body">
            <SessionConfig
              api={api}
              sessionId={currentId}
              workspaceTitle={workspaces.find(w => w.id === (selectedWs ?? sessions.find(s => s.id === currentId)?.workspaceId))?.title}
              busy={view.busy}
              onError={setErr}
            />
          </div>
        ) : tab === 'trajectory' ? (
          <div className="conv-body">
            <TrajectoryView records={view.records} selectId={inspId} />
            {composer}
          </div>
        ) : (
          <div className="conv-body">
            {empty ? (
              <div className="hero" data-testid="hero">
                <h1>{t('hero.title')}</h1>
                <div className="hero-composer">{composer}</div>
              </div>
            ) : (
              <>
                <div
                  className="scroll"
                  data-testid="chat-scroll"
                  ref={scrollRef}
                  onScroll={e => {
                    const el = e.currentTarget
                    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80)
                  }}
                >
                  <ChatView nodes={view.nodes} busy={view.busy} onSelect={inspect} />
                </div>
                {!atBottom ? (
                  <div className="to-bottom-slot">
                    <button type="button" className="to-bottom" data-testid="to-bottom" aria-label={t('chat.toBottom')} onClick={() => {
                      const el = scrollRef.current
                      if (!el) return
                      el.scrollTop = el.scrollHeight
                      setAtBottom(true)
                    }}
                    >
                      <IChevDown />
                    </button>
                  </div>
                ) : null}
                {composer}
              </>
            )}
          </div>
        )}
      </main>

      {menu ? (
        <div className="menu-mask" onClick={() => setMenu(null)}>
          <div className="pop-menu" onClick={e => e.stopPropagation()}>
            {menu.kind === 'ws' ? (
              <>
                <button type="button" onClick={() => {
                  const ws = workspaces.find(w => w.id === menu.id)
                  setRename({ kind: 'ws', id: menu.id, title: ws?.title ?? '' })
                  setMenu(null)
                }}
                >
                  <IEdit /> {t('workspace.rename')}
                </button>
                <button type="button" className="danger" onClick={() => {
                  const ws = workspaces.find(w => w.id === menu.id)
                  const n = sessions.filter(s => s.workspaceId === menu.id).length
                  setConfirmDel({ kind: 'ws', id: menu.id, label: ws?.title ?? '', extra: t('delete.workspaceExtra', { n, path: ws?.path ?? '' }) })
                  setMenu(null)
                }}
                >
                  <ITrash /> {t('workspace.delete')}
                </button>
              </>
            ) : (
              <>
                <button type="button" onClick={() => {
                  const s = byId.get(menu.id)
                  void api.patch(menu.id, { pinned: !s?.pinned }).then(() => refreshList())
                  setMenu(null)
                }}
                >
                  <IPin /> {byId.get(menu.id)?.pinned ? t('session.unpin') : t('session.pin')}
                </button>
                <button type="button" onClick={() => {
                  const s = byId.get(menu.id)
                  setRename({ kind: 'sess', id: menu.id, title: s?.title ?? '' })
                  setMenu(null)
                }}
                >
                  <IEdit /> {t('session.rename')}
                </button>
                <button type="button" className="danger" onClick={() => {
                  const s = byId.get(menu.id)
                  setConfirmDel({ kind: 'sess', id: menu.id, label: s?.title || untitled })
                  setMenu(null)
                }}
                >
                  <ITrash /> {t('session.delete')}
                </button>
              </>
            )}
          </div>
        </div>
      ) : null}

      {rename ? (
        <Modal title={t('rename.title')} onClose={() => setRename(null)} testid="rename-dialog">
          <input className="session-search" data-testid="rename-input" value={rename.title} onChange={e => setRename({ ...rename, title: e.target.value })} />
          <div className="modal-actions">
            <button
              type="button"
              className="primary-btn"
              data-testid="rename-ok"
              onClick={() => {
                void (rename.kind === 'ws' ? api.patchWorkspace(rename.id, { title: rename.title }) : api.patch(rename.id, { title: rename.title }))
                  .then(() => refreshList())
                  .then(() => setRename(null))
                  .catch(e => setErr(e instanceof Error ? e.message : String(e)))
              }}
            >
              {t('rename.ok')}
            </button>
          </div>
        </Modal>
      ) : null}

      {confirmDel ? (
        <Modal title={t('delete.title')} onClose={() => setConfirmDel(null)} testid="confirm-del">
          <p>
            {confirmDel.kind === 'ws'
              ? t('delete.workspace', { label: confirmDel.label })
              : t('delete.session', { label: confirmDel.label })}
            {confirmDel.kind === 'ws' && confirmDel.extra ? ` ${confirmDel.extra}` : ''}
          </p>
          <div className="modal-actions">
            <button
              type="button"
              className="primary-btn"
              data-testid="confirm-del-ok"
              onClick={() => {
                void (confirmDel.kind === 'ws' ? api.deleteWorkspace(confirmDel.id) : api.deleteSession(confirmDel.id))
                  .then(() => {
                    if (confirmDel.kind === 'sess' && currentId === confirmDel.id) {
                      setCurrentId(null)
                      setView(emptyView())
                    }
                    if (confirmDel.kind === 'ws' && selectedWs === confirmDel.id) {
                      setSelectedWs(null)
                      if (sessions.find(s => s.id === currentId)?.workspaceId === confirmDel.id) {
                        setCurrentId(null)
                        setView(emptyView())
                      }
                    }
                    setConfirmDel(null)
                    return refreshList()
                  })
                  .catch(e => setErr(e instanceof Error ? e.message : String(e)))
              }}
            >
              {t('delete.ok')}
            </button>
          </div>
        </Modal>
      ) : null}

      <DirectoryBrowser
        api={api}
        open={dirOpen}
        busy={dirBusy}
        onClose={() => setDirOpen(false)}
        onOpen={path => {
          setDirBusy(true)
          void api.createWorkspace(path).then(ws => {
            setDirOpen(false)
            if (ws && ws.id) {
              setSelectedWs(ws.id)
              setExpanded(e => ({ ...e, [ws.id]: true }))
            }
            return refreshList()
          }).catch(e => setErr(e instanceof Error ? e.message : String(e)))
            .finally(() => setDirBusy(false))
        }}
      />

      {modelOpen ? (
        <Modal title={t('model.title')} onClose={() => setModelOpen(false)} testid="model-dialog">
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
        <Modal title={t('settings.title')} onClose={() => setSettingsOpen(false)} testid="settings">
          <div className="set-block">
            <div className="set-label">{t('settings.appearance')}</div>
            <div className="theme-picks" data-testid="settings-theme">
              <button type="button" className={`theme-pick${!dark ? ' on' : ''}`} onClick={() => setDark(false)}>
                <span className="theme-swatch light" aria-hidden />
                <span>{t('settings.themeLight')}</span>
              </button>
              <button type="button" className={`theme-pick${dark ? ' on' : ''}`} onClick={() => setDark(true)}>
                <span className="theme-swatch dark" aria-hidden />
                <span>{t('settings.themeDark')}</span>
              </button>
            </div>
          </div>
          <div className="set-block">
            <div className="set-label">{t('settings.language')}</div>
            <div className="lang-picks" data-testid="settings-lang">
              <button type="button" className={`lang-pick${lang === 'zh' ? ' on' : ''}`} data-testid="lang-zh" onClick={() => setLang('zh')}>
                {t('settings.langZh')}
              </button>
              <button type="button" className={`lang-pick${lang === 'en' ? ' on' : ''}`} data-testid="lang-en" onClick={() => setLang('en')}>
                {t('settings.langEn')}
              </button>
            </div>
          </div>
          <p className="set-hint">{t('settings.hint')}</p>
        </Modal>
      ) : null}
    </div>
  )
}
