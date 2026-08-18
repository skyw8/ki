import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client, boot } from './api'
import { ChatView } from './Chat'
import { Composer, type Draft } from './Composer'
import { AttachmentBrowser } from './AttachmentBrowser'
import { DirectoryBrowser } from './DirectoryBrowser'
import { SessionConfig } from './SessionConfig'
import { ProviderSettings } from './ProviderSettings'
import { ICheck, IChev, IChevDown, IClose, IDots, IEdit, IFile, IFolder, IGear, IImage, IPanel, IPin, IPlus, ISearch, ITrash } from './icons'
import { appendOptimisticUser, applyEvent, emptyView, loadHistory, sessionStats } from './model'
import type { ChatNode, Content, ModelInfo, SearchHit, SessionInfo, ViewState, WorkspaceInfo } from './types'
import { TrajectoryView } from './Trajectory'
import { useI18n } from './i18n'

type Tab = 'conversation' | 'trajectory' | 'config'
type SettingsPage = 'providers' | 'appearance'
const SHOW = 5
const EXPAND_KEY = 'ki-ws-expanded'

function loadExpanded(): Record<string, boolean> {
  try { return JSON.parse(localStorage.getItem(EXPAND_KEY) || '{}') as Record<string, boolean> }
  catch { return {} }
}

function fuzzyTextMatch(value: string, query: string): boolean {
  const haystack = value.normalize('NFKC').toLocaleLowerCase()
  const needle = query.normalize('NFKC').toLocaleLowerCase()
  if (haystack.includes(needle)) return true
  let cursor = 0
  for (const char of haystack) {
    if (char === needle[cursor]) cursor++
    if (cursor === needle.length) return true
  }
  return needle.length === 0
}

function modelMatches(model: ModelInfo, query: string): boolean {
  const fields = [model.provider, model.id, model.name, model.spec, `${model.provider}/${model.id}`]
  return query.trim().split(/\s+/).every(token => fields.some(field => fuzzyTextMatch(field || '', token)))
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
      <div className={`modal${testid === 'settings' ? ' settings-modal' : ''}`} data-testid={testid} onClick={e => e.stopPropagation()} role="dialog" aria-label={title}>
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
  const [draft, setDraft] = useState<Draft>({ text: '', attachments: [] })
	const [edit, setEdit] = useState<{ messageId: string; parentId: string; draft: Draft } | null>(null)
	const [attachmentTarget, setAttachmentTarget] = useState<'new' | 'edit' | null>(null)
	const [uploading, setUploading] = useState(false)
	const [fileDragActive, setFileDragActive] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [settingsOpen, setSettingsOpen] = useState(false)
	const [settingsPage, setSettingsPage] = useState<SettingsPage>('providers')
  const [modelOpen, setModelOpen] = useState(false)
  const [modelQuery, setModelQuery] = useState('')
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
  const modelSearchRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    document.body.dataset.theme = dark ? 'dark' : 'light'
    document.body.toggleAttribute('data-ds-dark-theme', dark)
    localStorage.setItem('ki-theme', dark ? 'dark' : 'light')
  }, [dark])

  useEffect(() => { localStorage.setItem(EXPAND_KEY, JSON.stringify(expanded)) }, [expanded])

  useEffect(() => {
    if (!modelOpen) return
    requestAnimationFrame(() => modelSearchRef.current?.focus())
  }, [modelOpen])

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
	const refreshModels = useCallback(() => { void api.models().then(setModels).catch(() => setModels([])) }, [api])

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
		void api.get(id).then(detail => {
		  if (abortRef.current === ac) setView(loadHistory(detail))
		}).catch(() => {})
        void refreshList()
      }
    }
  }, [api, refreshList])

  const openSession = useCallback(async (id: string) => {
    setCurrentId(id)
    setErr(null)
	setEdit(null)
    abortRef.current?.abort()
	abortRef.current = null
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
    setView({ ...emptyView(), cwd: s.cwd, model: s.model, provider: s.provider, thinkingEffort: s.thinkingEffort ?? '' })
    setTab('conversation')
	setEdit(null)
    if (s.workspaceId) {
      setExpanded(e => ({ ...e, [s.workspaceId!]: true }))
      setShowAll(a => ({ ...a, [s.workspaceId!]: true }))
    }
    return s
  }, [api, refreshList])

	const uploadClientFiles = useCallback(async (target: 'new' | 'edit', files: File[]) => {
	  if (!files.length || uploading) return
	  setUploading(true)
	  setErr(null)
	  try {
		let id = currentId
		if (!id) id = (await makeSession(selectedWs)).id
		const added = await Promise.all(files.map(file => api.uploadAttachment(id!, file)))
		if (target === 'edit') {
		  setEdit(e => e ? { ...e, draft: { ...e.draft, attachments: [...e.draft.attachments, ...added.filter(a => !e.draft.attachments.some(old => old.id === a.id))] } } : e)
		} else {
		  setDraft(d => ({ ...d, attachments: [...d.attachments, ...added.filter(a => !d.attachments.some(old => old.id === a.id))] }))
		}
	  } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
	  finally { setUploading(false) }
	}, [api, currentId, makeSession, selectedWs, uploading])

	useEffect(() => {
	  let depth = 0
	  const hasFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes('Files')
	  const reset = () => { depth = 0; setFileDragActive(false) }
	  const onDragEnter = (event: DragEvent) => {
		if (!hasFiles(event)) return
		event.preventDefault()
		depth++
		if (!view.busy && !uploading) setFileDragActive(true)
	  }
	  const onDragOver = (event: DragEvent) => {
		if (!hasFiles(event)) return
		event.preventDefault()
		if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
	  }
	  const onDragLeave = (event: DragEvent) => {
		if (!hasFiles(event)) return
		event.preventDefault()
		depth = Math.max(0, depth - 1)
		if (depth === 0) setFileDragActive(false)
	  }
	  const onDrop = (event: DragEvent) => {
		if (!hasFiles(event)) return
		event.preventDefault()
		const files = Array.from(event.dataTransfer?.files ?? [])
		reset()
		if (!files.length || view.busy || uploading) return
		setAttachmentTarget(null)
		void uploadClientFiles(edit ? 'edit' : 'new', files)
	  }
	  window.addEventListener('dragenter', onDragEnter)
	  window.addEventListener('dragover', onDragOver)
	  window.addEventListener('dragleave', onDragLeave)
	  window.addEventListener('drop', onDrop)
	  window.addEventListener('dragend', reset)
	  return () => {
		window.removeEventListener('dragenter', onDragEnter)
		window.removeEventListener('dragover', onDragOver)
		window.removeEventListener('dragleave', onDragLeave)
		window.removeEventListener('drop', onDrop)
		window.removeEventListener('dragend', reset)
	  }
	}, [edit, uploadClientFiles, uploading, view.busy])

  const newSession = useCallback(async (wsId?: string) => {
    setErr(null)
    try { await makeSession(wsId ?? selectedWs) }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
  }, [makeSession, selectedWs])

  const sendContent = useCallback(async (content: Content[], parentId?: string, editedMessageId?: string) => {
	if (!content.some(c => (c.type === 'text' && !!c.text?.trim()) || c.type !== 'text')) return
    setErr(null)
    try {
      let id = currentId
      if (!id) {
        const s = await makeSession(selectedWs)
        id = s.id
      }
      try {
        const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
		await api.prompt(id, content, spec || undefined, parentId)
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) throw e
      }
	  if (editedMessageId) {
		setEdit(null)
		setView(v => {
		  const cut = v.nodes.findIndex(n => n.id === editedMessageId)
		  // Hide the abandoned descendant path immediately; the authoritative
		  // tree is reloaded after SSE completes, so failed requests never lose
		  // the editor draft or mutate the visible branch.
		  return appendOptimisticUser({ ...v, nodes: cut >= 0 ? v.nodes.slice(0, cut) : v.nodes }, content)
		})
	  } else {
		setDraft({ text: '', attachments: [] })
		setView(v => appendOptimisticUser(v, content))
	  }
      void listen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
	}, [api, currentId, listen, makeSession, selectedWs, view.model, view.provider])

	const send = useCallback(() => {
	  const content: Content[] = [...(draft.text.trim() ? [{ type: 'text', text: draft.text } as Content] : []), ...draft.attachments]
	  return sendContent(content)
	}, [draft, sendContent])

	const sendEdit = useCallback(() => {
	  if (!edit) return Promise.resolve()
	  const content: Content[] = [...(edit.draft.text.trim() ? [{ type: 'text', text: edit.draft.text } as Content] : []), ...edit.draft.attachments]
	  return sendContent(content, edit.parentId, edit.messageId)
	}, [edit, sendContent])

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
    setModelQuery('')
    if (!currentId) return
    try {
      const out = await api.patch(currentId, { model: spec })
	  setView(v => ({ ...v, model: out.model, provider: out.provider, thinkingEffort: out.thinkingEffort ?? '' }))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [api, currentId, view.provider])

  const selectedModel = useMemo(() => models.find(m => m.provider === view.provider && m.id === view.model), [models, view.model, view.provider])
	const filteredModels = useMemo(() => modelQuery.trim() ? models.filter(model => modelMatches(model, modelQuery)) : models, [modelQuery, models])
	const switchThinking = useCallback(async (thinkingEffort: string) => {
		setView(v => ({ ...v, thinkingEffort }))
		if (!currentId) return
		try {
			const out = await api.patch(currentId, { thinkingEffort })
			setView(v => ({ ...v, thinkingEffort: out.thinkingEffort ?? thinkingEffort }))
		} catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
	}, [api, currentId])

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

	const startEdit = useCallback((node: Extract<ChatNode, { kind: 'user' }>) => {
	  if (view.busy) return
	  setEdit({
		messageId: node.id,
		parentId: node.parentId ?? '',
		draft: { text: node.text, attachments: node.content.filter(c => c.type === 'image' || c.type === 'file' || c.type === 'workspace_file') },
	  })
	}, [view.busy])

	const forkMessage = useCallback(async (node: Extract<ChatNode, { kind: 'assistant' }>) => {
	  if (!currentId || view.busy) return
	  setErr(null)
	  try {
		const child = await api.fork(currentId, node.id)
		await refreshList()
		await openSession(child.id)
	  } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
	}, [api, currentId, openSession, refreshList, view.busy])

	const regenerate = useCallback((node: Extract<ChatNode, { kind: 'assistant' }>) => {
	  const idx = view.nodes.findIndex(n => n.id === node.id)
	  for (let i = idx - 1; i >= 0; i--) {
		const candidate = view.nodes[i]
		if (candidate.kind !== 'user') continue
		void sendContent(candidate.content, candidate.parentId ?? '', candidate.id)
		return
	  }
	}, [sendContent, view.nodes])

	const branchInfo = useMemo(() => {
	  const groups = new Map<string, string[]>()
	  for (const entry of view.allEntries) {
		if (entry.type !== 'message' || entry.message?.role !== 'user') continue
		const key = entry.parentId ?? ''
		groups.set(key, [...(groups.get(key) ?? []), entry.id])
	  }
	  const out: Record<string, { index: number; total: number }> = {}
	  for (const ids of groups.values()) ids.forEach((id, index) => { out[id] = { index, total: ids.length } })
	  return out
	}, [view.allEntries])

	const switchBranch = useCallback(async (node: Extract<ChatNode, { kind: 'user' }>, delta: number) => {
	  if (!currentId || view.busy) return
	  const siblings = view.allEntries.filter(e => e.type === 'message' && e.message?.role === 'user' && (e.parentId ?? '') === (node.parentId ?? ''))
	  const at = siblings.findIndex(e => e.id === node.id)
	  const target = siblings[at + delta]
	  if (!target) return
	  const byId = new Map(view.allEntries.map(e => [e.id, e]))
	  let leaf = target.id
	  // A branch selection targets its newest descendant so tool/result pairs
	  // remain complete instead of exposing an arbitrary physical jsonl tail.
	  for (let i = view.allEntries.length - 1; i >= 0; i--) {
		let id: string | undefined = view.allEntries[i].id
		while (id) {
		  if (id === target.id) { leaf = view.allEntries[i].id; id = undefined; break }
		  id = byId.get(id)?.parentId
		}
		if (leaf !== target.id) break
	  }
	  try {
		await api.patch(currentId, { leafId: leaf })
		await openSession(currentId)
	  } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
	}, [api, currentId, openSession, view.allEntries, view.busy])

  const empty = view.nodes.length === 0
  const stats = useMemo(() => sessionStats(view), [view])
  const composer = (
    <Composer
	  api={api}
	  draft={draft}
	  onChange={setDraft}
      onSend={() => void send()}
      onStop={() => void stop()}
	  onAttach={() => setAttachmentTarget('new')}
	  onFiles={files => void uploadClientFiles('new', files)}
	  uploading={uploading}
      busy={view.busy}
      cwd={view.cwd}
      model={view.model}
      err={err}
      onPickModel={() => { setModelQuery(''); setModelOpen(true) }}
	  thinkingLevels={selectedModel?.thinkingLevels}
	  thinkingEffort={view.thinkingEffort}
	  onThinking={effort => void switchThinking(effort)}
	  contextUsage={view.contextUsage}
	  stats={stats}
    />
  )

  const visibleRows = (wsId: string, rows: SessionInfo[]) => {
    if (showAll[wsId]) return rows
    const idx = currentId ? rows.findIndex(s => s.id === currentId) : -1
    const n = idx >= SHOW ? idx + 1 : SHOW
    return rows.slice(0, n)
  }

  return (
    <div className={`app${collapsed ? ' sidebar-collapsed' : ''}${(settingsOpen || modelOpen || dirOpen || attachmentTarget) ? ' modal-open' : ''}`}>
	  {fileDragActive ? createPortal(<div className="global-drop-overlay" data-testid="global-drop-overlay">
		<div className="global-drop-visual"><span><IImage /></span><span><IFile /></span><span><IPlus /></span></div>
		<strong>{edit ? t('drop.editTitle') : t('drop.newTitle')}</strong>
		<p>{t('drop.hint')}</p>
	  </div>, document.body) : null}
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
				  <ChatView
					api={api}
					nodes={view.nodes}
					busy={view.busy}
					onSelect={inspect}
					edit={edit}
					onStartEdit={startEdit}
					onEditChange={draft => setEdit(e => e ? { ...e, draft } : e)}
					onCancelEdit={() => setEdit(null)}
					onSendEdit={() => void sendEdit()}
					onAttachEdit={() => setAttachmentTarget('edit')}
					onFilesEdit={files => void uploadClientFiles('edit', files)}
					uploading={uploading}
					onFork={node => void forkMessage(node)}
					onRegen={regenerate}
					branches={branchInfo}
					onBranch={(node, delta) => void switchBranch(node, delta)}
				  />
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
				{!edit ? composer : null}
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
	  <AttachmentBrowser
		api={api}
		open={attachmentTarget !== null}
		startPath={view.cwd || undefined}
		onClose={() => setAttachmentTarget(null)}
		onPick={content => {
		  if (attachmentTarget === 'edit') {
			setEdit(e => e && !e.draft.attachments.some(a => a.path === content.path) ? { ...e, draft: { ...e.draft, attachments: [...e.draft.attachments, content] } } : e)
		  } else {
			setDraft(d => d.attachments.some(a => a.path === content.path) ? d : { ...d, attachments: [...d.attachments, content] })
		  }
		}}
	  />

      {modelOpen ? (
        <Modal title={t('model.title')} onClose={() => { setModelOpen(false); setModelQuery('') }} testid="model-dialog">
          <div className="model-search-wrap">
            <ISearch />
            <input
              ref={modelSearchRef}
              type="search"
              data-testid="model-search"
              aria-label={t('model.search')}
              placeholder={t('model.searchPlaceholder')}
              value={modelQuery}
              onChange={event => setModelQuery(event.target.value)}
            />
            {modelQuery ? <button type="button" aria-label={t('model.clearSearch')} onClick={() => { setModelQuery(''); modelSearchRef.current?.focus() }}><IClose /></button> : null}
          </div>
          <ul className="model-list">
            {filteredModels.map(m => {
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
                    <span className="model-opt-copy"><strong>{m.name || m.id}</strong><small>{m.name && m.name !== m.id ? `${m.provider} / ${m.id}` : m.provider}</small></span>
                    {on ? <ICheck /> : null}
                  </button>
                </li>
              )
            })}
          </ul>
          {!filteredModels.length ? <div className="model-search-empty" data-testid="model-search-empty"><ISearch /><span>{t('model.noResults')}</span></div> : null}
        </Modal>
      ) : null}

      {settingsOpen ? (
        <Modal title={t('settings.title')} onClose={() => setSettingsOpen(false)} testid="settings">
		  <nav className="settings-tabs" role="tablist" aria-label={t('settings.title')}>
			<button type="button" role="tab" aria-selected={settingsPage === 'providers'} className={settingsPage === 'providers' ? 'on' : ''} data-testid="settings-tab-providers" onClick={() => setSettingsPage('providers')}>{t('settings.providers')}</button>
			<button type="button" role="tab" aria-selected={settingsPage === 'appearance'} className={settingsPage === 'appearance' ? 'on' : ''} data-testid="settings-tab-appearance" onClick={() => setSettingsPage('appearance')}>{t('settings.appearanceLanguage')}</button>
		  </nav>
		  <div className="settings-page">
			{settingsPage === 'providers' ? <ProviderSettings api={api} onChanged={refreshModels} /> : (
			  <div className="preference-page" data-testid="appearance-settings">
				<header className="settings-page-title"><h3>{t('settings.appearanceLanguage')}</h3><p>{t('settings.preferenceHint')}</p></header>
				<section className="preference-section">
				  <div className="preference-copy"><h4>{t('settings.appearance')}</h4><p>{t('settings.themeHint')}</p></div>
				  <div className="theme-picks" data-testid="settings-theme" role="radiogroup" aria-label={t('settings.appearance')}>
					<button type="button" role="radio" aria-checked={!dark} className={`theme-pick${!dark ? ' on' : ''}`} onClick={() => setDark(false)}><span className="theme-swatch light" aria-hidden /><span>{t('settings.themeLight')}</span></button>
					<button type="button" role="radio" aria-checked={dark} className={`theme-pick${dark ? ' on' : ''}`} onClick={() => setDark(true)}><span className="theme-swatch dark" aria-hidden /><span>{t('settings.themeDark')}</span></button>
				  </div>
				</section>
				<section className="preference-section inline">
				  <div className="preference-copy"><h4>{t('settings.language')}</h4><p>{t('settings.languageHint')}</p></div>
				  <div className="lang-picks" data-testid="settings-lang" role="radiogroup" aria-label={t('settings.language')}>
					<button type="button" role="radio" aria-checked={lang === 'zh'} className={`lang-pick${lang === 'zh' ? ' on' : ''}`} data-testid="lang-zh" onClick={() => setLang('zh')}>{t('settings.langZh')}</button>
					<button type="button" role="radio" aria-checked={lang === 'en'} className={`lang-pick${lang === 'en' ? ' on' : ''}`} data-testid="lang-en" onClick={() => setLang('en')}>{t('settings.langEn')}</button>
				  </div>
				</section>
				<p className="preference-footnote">{t('settings.hint')}</p>
			  </div>
			)}
		  </div>
        </Modal>
      ) : null}
    </div>
  )
}
