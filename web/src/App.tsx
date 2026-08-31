import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client } from './api'
import { AuthLoading, LoginScreen } from './AuthScreen'
import { ChatView } from './Chat'
import { Composer, type Draft } from './Composer'
import { AttachmentBrowser } from './AttachmentBrowser'
import { DirectoryBrowser } from './DirectoryBrowser'
import { SessionTreeBrowser } from './SessionTreeBrowser'
import { ExtensionConfigEditor, MessageSettings, SessionConfig, SettingsToggles } from './SessionConfig'
import { ModelPickerDialog } from './ModelPickerDialog'
import { ProviderSettings } from './ProviderSettings'
import { IChev, IChevDown, IClose, IDots, IEdit, IFile, IFolder, IFork, IGear, IImage, IPanel, IPin, IPlus, ISearch, ITrash } from './icons'
import { appendOptimisticUser, applyEvent, applyRuntimeCatalog, clampThinkingEffort, emptyView, initialView, keepComposer, loadHistory, loadLastComposerModel, pickComposerModel, saveLastComposerModel, sessionCreateBody, sessionStats } from './model'
import type { CatalogExtension, ChatNode, Content, ExtensionUI, ModelInfo, SearchHit, SessionInfo, ViewState, WorkspaceInfo } from './types'
import { TrajectoryView } from './Trajectory'
import { useI18n } from './i18n'
import { toast } from './toast'
import { ExtensionInspector, localizedExtensionText, seedExtFields, statusChips, visibleStatusChips } from './ExtensionPanel'
import { useDialogFocus } from './useDialogFocus'

type Tab = 'conversation' | 'trajectory' | 'config'
type SettingsPage = 'providers' | 'skills' | 'extensions' | 'message' | 'appearance'
const SETTINGS_PAGES: readonly SettingsPage[] = ['providers', 'skills', 'extensions', 'message', 'appearance']
const SHOW = 5
const EXPAND_KEY = 'ki-ws-expanded'
const COMPACT_LAYOUT_QUERY = '(max-width: 900px)'

function useCompactLayout(): boolean {
  const [compact, setCompact] = useState(() => window.matchMedia(COMPACT_LAYOUT_QUERY).matches)
  useEffect(() => {
    const query = window.matchMedia(COMPACT_LAYOUT_QUERY)
    const update = () => setCompact(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])
  return compact
}

function loadExpanded(): Record<string, boolean> {
  try { return JSON.parse(localStorage.getItem(EXPAND_KEY) || '{}') as Record<string, boolean> }
  catch { return {} }
}

function extensionRuntimeTone(item: CatalogExtension): string {
  switch (item.runtime?.state) {
    case 'ready': return 'success'
    case 'starting':
    case 'restarting': return 'warning'
    case 'failed': return 'error'
    default: return 'info'
  }
}

function extensionRuntimeTitle(item: CatalogExtension): string {
  const state = item.runtime?.state || (item.enabled ? 'enabled' : 'disabled')
  return item.runtime?.error ? `${item.name} · ${state}: ${item.runtime.error}` : `${item.name} · ${state}`
}

function globalExtensionUI(item: CatalogExtension): ExtensionUI | null {
  if (item.ui) {
    return {
      ...item.ui,
      extension: item.name,
      status: item.ui.status?.text
        ? item.ui.status
        : { key: item.name, text: item.name, tone: extensionRuntimeTone(item) },
    }
  }
  if (!item.configurable) return null
  return {
    extension: item.name,
    status: { key: item.name, text: item.name, tone: extensionRuntimeTone(item) },
  }
}

function mergeExtensionUI(globalItems: ExtensionUI[], sessionItems: ExtensionUI[]): ExtensionUI[] {
  const byName = new Map<string, ExtensionUI>()
  for (const item of globalItems) byName.set(item.extension, item)
  for (const item of sessionItems) byName.set(item.extension, item)
  return statusChips(Array.from(byName.values()))
}

function sessionPathLabel(id: string, byId: Map<string, SessionInfo>, fallback: string): string {
  const labels: string[] = []
  const seen = new Set<string>()
  let current: SessionInfo | undefined = byId.get(id)
  while (current && !seen.has(current.id)) {
    seen.add(current.id)
    labels.unshift(current.title || fallback)
    current = current.parentSessionId ? byId.get(current.parentSessionId) : undefined
  }
  return labels.join(' / ')
}

// Why: .pop-menu used to be hard-coded at left:200/top:120, so a click on a
// lower-row ⋯ opened the menu at the top of the sidebar. Anchor to the trigger
// and clamp to the viewport so the menu stays under the pointer.
function popMenuStyle(anchor: DOMRect, menu: HTMLElement): CSSProperties {
  const gap = 4
  const pad = 8
  const mw = menu.offsetWidth
  const mh = menu.offsetHeight
  const visual = window.visualViewport
  const leftEdge = visual?.offsetLeft ?? 0
  const topEdge = visual?.offsetTop ?? 0
  const rightEdge = leftEdge + (visual?.width ?? window.innerWidth)
  const bottomEdge = topEdge + (visual?.height ?? window.innerHeight)
  let top = anchor.bottom + gap
  if (top + mh > bottomEdge - pad && anchor.top - gap - mh >= topEdge + pad) {
    top = anchor.top - gap - mh
  }
  const left = Math.max(leftEdge + pad, Math.min(anchor.left, rightEdge - mw - pad))
  top = Math.max(topEdge + pad, Math.min(top, bottomEdge - mh - pad))
  return { left, top }
}

function Modal({ title, onClose, children, testid, wide, className, initialFocusRef, restoreFocusRef }: { title: string; onClose: () => void; children: ReactNode; testid?: string; wide?: boolean; className?: string; initialFocusRef?: RefObject<HTMLElement>; restoreFocusRef?: RefObject<HTMLElement> }) {
  const { t } = useI18n()
  const dialogRef = useDialogFocus<HTMLDivElement>({ open: true, onEscape: onClose, initialFocusRef, restoreFocusRef })
  return createPortal(
    <div className="modal-mask" onClick={onClose} data-testid={testid ? `${testid}-mask` : undefined}>
      <div ref={dialogRef} className={`modal${testid === 'settings' ? ' settings-modal' : ''}${wide ? ' modal-wide' : ''}${className ? ` ${className}` : ''}`} data-testid={testid} onClick={e => e.stopPropagation()} role="dialog" aria-modal="true" aria-label={title} tabIndex={-1}>
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
  const api = useMemo(() => new Client(), [])
  const [auth, setAuth] = useState<'checking' | 'required' | 'authenticated'>('checking')

  useEffect(() => {
    void api.authStatus()
      .then(status => setAuth(status.authenticated ? 'authenticated' : 'required'))
      .catch(() => setAuth('required'))
  }, [api])

  if (auth === 'checking') return <AuthLoading />
  if (auth === 'required') return <LoginScreen api={api} onLogin={() => setAuth('authenticated')} />
  return <WorkspaceApp api={api} />
}

function WorkspaceApp({ api }: { api: Client }) {
  const { t, lang, setLang } = useI18n()
  const untitled = t('session.untitled')
  const compactLayout = useCompactLayout()
  const [dark, setDark] = useState(() => localStorage.getItem('ki-theme') === 'dark')
  const [collapsed, setCollapsed] = useState(false)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [settled, setSettled] = useState(false)
  const everWide = useRef(true)
  const [tab, setTab] = useState<Tab>('conversation')
  const [extOpen, setExtOpen] = useState<string | null>(null)
  const [extFields, setExtFields] = useState<Record<string, string>>({})
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [workspaces, setWorkspaces] = useState<WorkspaceInfo[]>([])
  const [filter, setFilter] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [hits, setHits] = useState<SearchHit[]>([])
  const [searchMore, setSearchMore] = useState(false)
  const [searchErr, setSearchErr] = useState<string | null>(null)
  const [currentId, setCurrentId] = useState<string | null>(null)
  // Why: Tree reveals are navigation context, not a durable preference. Keep
  // them for the lifetime of App so ordinary navigation can return to them,
  // while a full page reload naturally starts with no temporary children.
  const [temporaryTreeRevealIds, setTemporaryTreeRevealIds] = useState<string[]>([])
  const [treeOpen, setTreeOpen] = useState(false)
  const [selectedWs, setSelectedWs] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>(loadExpanded)
  const [showAll, setShowAll] = useState<Record<string, boolean>>({})
  const [view, setView] = useState<ViewState>(initialView)
  const [draft, setDraft] = useState<Draft>({ text: '', attachments: [] })
	const [edit, setEdit] = useState<{ messageId: string; parentId: string; draft: Draft } | null>(null)
	const [attachmentTarget, setAttachmentTarget] = useState<'new' | 'edit' | null>(null)
	const [uploading, setUploading] = useState(false)
  const [fileDragActive, setFileDragActive] = useState(false)
  const [models, setModels] = useState<ModelInfo[]>([])
	const [defaultModel, setDefaultModel] = useState('')
	const [globalExtensions, setGlobalExtensions] = useState<CatalogExtension[]>([])
  const [settingsOpen, setSettingsOpen] = useState(false)
	const [settingsPage, setSettingsPage] = useState<SettingsPage>('providers')
  const [modelOpen, setModelOpen] = useState(false)
  const [dirOpen, setDirOpen] = useState(false)
  const [dirBusy, setDirBusy] = useState(false)
  const [menu, setMenu] = useState<{ kind: 'ws' | 'sess'; id: string } | null>(null)
  const [menuStyle, setMenuStyle] = useState<CSSProperties>({ visibility: 'hidden' })
  const menuID = useId()
  const menuAnchor = useRef<DOMRect | null>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuTriggerRef = useRef<HTMLButtonElement | null>(null)
  const [rename, setRename] = useState<{ kind: 'ws' | 'sess'; id: string; title: string } | null>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const [confirmDel, setConfirmDel] = useState<{ kind: 'ws' | 'sess'; id: string; label: string; extra?: string } | null>(null)
  const [inspId, setInspId] = useState<string | null>(null)
  const [atBottom, setAtBottom] = useState(true)
  const abortRef = useRef<AbortController | null>(null)
  const searchAc = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const searchRootRef = useRef<HTMLDivElement>(null)
  const sidebarRef = useRef<HTMLElement>(null)
  const mainRef = useRef<HTMLElement>(null)
  const mobileNavToggleRef = useRef<HTMLButtonElement>(null)
  const mobileSidebarWasOpen = useRef(false)
  const drawerDialogSource = useRef<'settings' | 'dir' | null>(null)
  const sessionRowRefs = useRef(new Map<string, HTMLButtonElement>())

  const openDirectoryFromSidebar = () => {
    drawerDialogSource.current = compactLayout && mobileSidebarOpen ? 'dir' : null
    setMobileSidebarOpen(false)
    setDirOpen(true)
  }

  const openSettingsFromSidebar = () => {
    drawerDialogSource.current = compactLayout && mobileSidebarOpen ? 'settings' : null
    setMobileSidebarOpen(false)
    setSettingsOpen(true)
  }

  const openMenu = (kind: 'ws' | 'sess', id: string, el: HTMLButtonElement) => {
    menuTriggerRef.current = el
    menuAnchor.current = el.getBoundingClientRect()
    setMenuStyle({ visibility: 'hidden' })
    setMenu({ kind, id })
  }

  const closeMenu = useCallback((restoreFocus = true) => {
    const trigger = menuTriggerRef.current
    setMenu(null)
    if (restoreFocus) window.requestAnimationFrame(() => trigger?.isConnected && trigger.focus({ preventScroll: true }))
  }, [])

  const handoffMenuToDialog = useCallback(() => {
    // The next dialog captures document.activeElement as its restore target.
    // Move focus back to the opener before the menu unmounts so dialog close
    // never tries to restore a detached menu item.
    menuTriggerRef.current?.focus({ preventScroll: true })
    setMenu(null)
  }, [])

  useLayoutEffect(() => {
    if (!menu) return
    const anchor = menuAnchor.current
    const el = menuRef.current
    if (!anchor || !el) return
    setMenuStyle(popMenuStyle(anchor, el))
    const frame = window.requestAnimationFrame(() => el.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus({ preventScroll: true }))
    return () => window.cancelAnimationFrame(frame)
  }, [menu])

  useEffect(() => {
    if (!menu) return
    const close = () => closeMenu()
    const closeAfterAnchorMoves = () => {
      const anchor = menuAnchor.current
      const trigger = menuTriggerRef.current
      if (anchor && trigger?.isConnected) {
        const current = trigger.getBoundingClientRect()
        // Why: browsers may deliver the trigger's scroll-into-view event after
        // the click has mounted the menu. That stale event must not immediately
        // dismiss a menu whose anchor is already at the recorded position.
        if (
          Math.abs(current.top - anchor.top) < 0.5
          && Math.abs(current.right - anchor.right) < 0.5
          && Math.abs(current.bottom - anchor.bottom) < 0.5
          && Math.abs(current.left - anchor.left) < 0.5
        ) return
      }
      close()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close() }
    window.addEventListener('resize', close)
    window.addEventListener('scroll', closeAfterAnchorMoves, true)
    window.visualViewport?.addEventListener('resize', close)
    window.visualViewport?.addEventListener('scroll', close)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', closeAfterAnchorMoves, true)
      window.visualViewport?.removeEventListener('resize', close)
      window.visualViewport?.removeEventListener('scroll', close)
      window.removeEventListener('keydown', onKey)
    }
  }, [closeMenu, menu])

  const onMenuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'))
    if (!items.length) return
    const current = items.indexOf(document.activeElement as HTMLButtonElement)
    let next = current
    if (event.key === 'ArrowDown') next = (current + 1 + items.length) % items.length
    else if (event.key === 'ArrowUp') next = (current - 1 + items.length) % items.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = items.length - 1
    else if (event.key === 'Tab') next = (current + (event.shiftKey ? -1 : 1) + items.length) % items.length
    else if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      closeMenu()
      return
    } else return
    event.preventDefault()
    event.stopPropagation()
    items[next]?.focus({ preventScroll: true })
  }

  const onSettingsTabKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
    const current = (event.target as HTMLElement).closest<HTMLButtonElement>('[role="tab"]')
    const index = current ? tabs.indexOf(current) : -1
    if (index < 0 || !tabs.length) return
    const next = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? tabs.length - 1
        : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
    const page = tabs[next]?.dataset.settingsPage as SettingsPage | undefined
    if (!page || !SETTINGS_PAGES.includes(page)) return
    event.preventDefault()
    // Horizontal tab strips can scroll on compact layouts; focus the newly
    // active tab so keyboard users receive the same visible context as taps.
    setSettingsPage(page)
    window.requestAnimationFrame(() => tabs[next]?.focus({ preventScroll: false }))
  }

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
  const sidebarWide = compactLayout || wide

  useEffect(() => {
    if (!compactLayout) setMobileSidebarOpen(false)
  }, [compactLayout])

  useEffect(() => {
    if (!mobileSidebarOpen) return
    const close = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMobileSidebarOpen(false)
    }
    window.addEventListener('keydown', close)
    return () => window.removeEventListener('keydown', close)
  }, [mobileSidebarOpen])

  useEffect(() => {
    // Why: a selected session must become visible immediately on a phone;
    // leaving the navigation drawer above it made taps appear to do nothing.
    if (compactLayout) setMobileSidebarOpen(false)
  }, [compactLayout, currentId])

  useLayoutEffect(() => {
    const sidebar = sidebarRef.current
    const main = mainRef.current
    if (!sidebar || !main) return
    // Why: visibility and aria-hidden do not remove descendants from every
    // browser's tab order. inert makes the closed off-canvas drawer truly
    // unavailable until its scrim and content are visible.
    sidebar.toggleAttribute('inert', compactLayout && !mobileSidebarOpen)
    main.toggleAttribute('inert', compactLayout && mobileSidebarOpen)
    if (compactLayout && mobileSidebarOpen) {
      requestAnimationFrame(() => sidebar.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled)')?.focus({ preventScroll: true }))
    } else if (compactLayout && mobileSidebarWasOpen.current) {
      // Why: a dialog opened from the drawer owns initial focus and has an
      // explicit return target. The ordinary drawer-close frame must not steal
      // focus back to the hamburger after that dialog has mounted.
      const handingOffToDialog = (drawerDialogSource.current === 'settings' && settingsOpen)
        || (drawerDialogSource.current === 'dir' && dirOpen)
      if (!handingOffToDialog) requestAnimationFrame(() => mobileNavToggleRef.current?.focus({ preventScroll: true }))
    }
    mobileSidebarWasOpen.current = mobileSidebarOpen
  }, [compactLayout, dirOpen, mobileSidebarOpen, settingsOpen])

  const onSidebarKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (!compactLayout || !mobileSidebarOpen || event.key !== 'Tab') return
    const sidebar = sidebarRef.current
    if (!sidebar) return
    const focusable = Array.from(sidebar.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [href], [tabindex]:not([tabindex="-1"])'))
      .filter(element => element.offsetParent !== null)
    if (!focusable.length) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

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
    void Promise.all([api.models(), api.meta()]).then(([list, meta]) => {
      setModels(list)
		setDefaultModel(meta.provider && meta.model ? `${meta.provider}/${meta.model}` : '')
      setView(v => {
        if (v.provider && v.model) {
          const found = list.find(m => m.provider === v.provider && m.id === v.model)
          if (found) {
            const thinkingEffort = clampThinkingEffort(v.thinkingEffort, found)
            saveLastComposerModel({ provider: v.provider, model: v.model, thinkingEffort })
            return thinkingEffort === v.thinkingEffort ? v : { ...v, thinkingEffort }
          }
          if (v.nodes.length > 0) return v
        }
        const picked = pickComposerModel(list, loadLastComposerModel(), meta)
        saveLastComposerModel(picked)
        if (v.provider === picked.provider && v.model === picked.model && v.thinkingEffort === picked.thinkingEffort) return v
        return { ...v, provider: picked.provider, model: picked.model, thinkingEffort: picked.thinkingEffort }
      })
    }).catch(() => setModels([]))
  }, [api])
	const refreshModels = useCallback(() => {
		void Promise.all([api.models(), api.meta()]).then(([list, meta]) => {
			setModels(list)
			setDefaultModel(meta.provider && meta.model ? `${meta.provider}/${meta.model}` : '')
		}).catch(() => setModels([]))
	}, [api])
	const refreshExtensions = useCallback(async () => {
		try {
			setGlobalExtensions(await api.extensions())
		} catch (e) {
			toast.from(e)
		}
	}, [api])

  const refreshList = useCallback(async () => {
    try {
      const [ss, ws] = await Promise.all([api.list(), api.workspaces()])
      setSessions(ss)
      setWorkspaces(ws)
    } catch (e) {
      toast.from(e)
    }
  }, [api])

	useEffect(() => { void refreshList() }, [refreshList])
	useEffect(() => { void refreshExtensions() }, [refreshExtensions])
	useEffect(() => {
		if (currentId) return
		void api.commands(selectedWs).then(commands => {
			setView(v => v.commands === commands ? v : { ...v, commands })
		}).catch(() => {})
	}, [api, currentId, selectedWs])
	useEffect(() => {
		const timer = window.setInterval(() => {
			void api.extensions().then(setGlobalExtensions).catch(() => {})
		}, 1000)
		return () => window.clearInterval(timer)
	}, [api])
  useEffect(() => {
    if (!temporaryTreeRevealIds.length) return
    const available = new Set(sessions.map(session => session.id))
    const next = temporaryTreeRevealIds.filter(id => available.has(id))
    if (next.length !== temporaryTreeRevealIds.length) setTemporaryTreeRevealIds(next)
  }, [sessions, temporaryTreeRevealIds])
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
    // Why: the sidebar dot reads sessions[].running, which only refreshList()
    // updates (after SSE ends or a manual refetch). Light it as soon as this
    // client starts listening so a live run is green without switching tabs.
    setSessions(ss => ss.map(s => s.id === id ? { ...s, running: true } : s))
    try {
      for await (const ev of api.events(id, ac.signal)) {
        setView(v => (abortRef.current === ac ? applyEvent(v, ev) : v))
      }
    } catch (e) {
      if ((e as { name?: string }).name === 'AbortError') return
      toast.from(e)
    } finally {
      if (abortRef.current === ac) {
        const detail = await api.get(id).catch(() => null)
        if (abortRef.current === ac && detail) {
          setView(loadHistory(detail))
          if (detail.running) void listen(id)
        } else if (abortRef.current === ac) {
          setView(v => ({ ...v, busy: false }))
        }
        void refreshList()
      }
    }
  }, [api, refreshList])

	useEffect(() => {
		if (!currentId) return
		const ac = new AbortController()
		void (async () => {
			while (!ac.signal.aborted) {
				try {
					for await (const ev of api.events(currentId, ac.signal, true)) {
					if (ev.type === 'run_aborted') {
					  setView(v => applyEvent(v, ev))
					  continue
					}
					if (ev.type === 'runtime_ready') {
					  try {
					    const detail = await api.get(currentId)
					    if (ac.signal.aborted) return
					    setView(v => applyRuntimeCatalog(v, detail))
					  } catch (e) { toast.from(e) }
					  continue
					}
					if (ev.type === 'extension_ui_updated' || ev.type === 'queue_changed') {
					  try {
					    const detail = await api.get(currentId)
					    if (ac.signal.aborted) return
					    setView(v => ({ ...loadHistory(detail), busy: v.busy || !!detail.running }))
					    if (detail.running) void listen(currentId)
					  } catch (e) { toast.from(e) }
					  continue
					}
					if (ev.type === 'extension_notice') {
						const text = ev.messageText || ev.reason || ev.server || 'extension'
						if (ev.reason === 'warn' || ev.reason === 'error') toast.error(text)
						else toast.info(text)
						continue
					}
					if (ev.type === 'extension_error') {
						toast.action('error', `${ev.server || 'extension'}: ${ev.messageText || ev.reason || t('extension.failed')}`, t('extension.reload'), async () => {
							try {
								const result = await api.reload(currentId)
								toast.info(result.queued ? t('extension.reloadQueued') : t('extension.reloaded'))
							} catch (e) { toast.from(e) }
						})
						continue
					}
					}
				} catch (e) {
					if ((e as { name?: string }).name === 'AbortError' || ac.signal.aborted) return
				}
				// The selected session keeps a notification stream while idle. Retry
				// transient proxy/network closes without creating an error-toast loop.
				await new Promise<void>(resolve => {
					const onAbort = () => { window.clearTimeout(timer); resolve() }
					const timer = window.setTimeout(() => { ac.signal.removeEventListener('abort', onAbort); resolve() }, 1000)
					ac.signal.addEventListener('abort', onAbort, { once: true })
				})
				}
		})()
		return () => ac.abort()
	}, [api, currentId, listen, t])

  const openSession = useCallback(async (id: string): Promise<boolean> => {
    setCurrentId(id)
	setEdit(null)
    abortRef.current?.abort()
	abortRef.current = null
    try {
      const detail = await api.get(id)
      const next = loadHistory(detail)
      setView(next)
      saveLastComposerModel({ provider: next.provider, model: next.model, thinkingEffort: next.thinkingEffort })
      setSelectedWs(detail.workspaceId ?? null)
      if (detail.workspaceId) setExpanded(e => ({ ...e, [detail.workspaceId!]: true }))
      if (detail.running) void listen(id)
      return true
    } catch (e) {
      toast.from(e)
      return false
    }
  }, [api, listen])

  useEffect(() => {
    if (!currentId || view.runtimeReady !== false) return
    const ac = new AbortController()
    const started = Date.now()
    void (async () => {
      while (!ac.signal.aborted && Date.now() - started < 25_000) {
        await new Promise<void>(resolve => {
          const onAbort = () => { window.clearTimeout(timer); resolve() }
          const timer = window.setTimeout(() => { ac.signal.removeEventListener('abort', onAbort); resolve() }, 400)
          ac.signal.addEventListener('abort', onAbort, { once: true })
        })
        if (ac.signal.aborted) return
        try {
          const detail = await api.get(currentId)
          if (ac.signal.aborted) return
          if (detail.runtime?.ready) {
            setView(v => applyRuntimeCatalog(v, detail))
            return
          }
        } catch { /* retry until timeout */ }
      }
      if (!ac.signal.aborted) setView(v => ({ ...v, runtimeReady: true }))
    })()
    return () => ac.abort()
  }, [api, currentId, view.runtimeReady])

  const sessionExtChips = useMemo(() => statusChips(view.extensionUi), [view.extensionUi])
  const globalExtensionItems = useMemo(
		() => globalExtensions.filter(item => item.enabled && (item.configurable || item.ui?.status?.text || item.ui?.panel)),
		[globalExtensions],
	)
	const globalExtChips = useMemo(
		() => globalExtensionItems.flatMap(item => {
			const ui = globalExtensionUI(item)
			return ui ? [ui] : []
		}),
		[globalExtensionItems],
	)
  const extChips = useMemo(() => mergeExtensionUI(globalExtChips, sessionExtChips), [globalExtChips, sessionExtChips])
  const extVisible = useMemo(() => visibleStatusChips(extChips), [extChips])
  const extHidden = extChips.length - extVisible.length
  const extSelected = useMemo(() => {
		if (!extOpen) return null
		const sessionUI = sessionExtChips.find(ui => ui.extension === extOpen)
		if (sessionUI) return sessionUI
		const globalItem = globalExtensionItems.find(item => item.name === extOpen)
		return globalItem ? globalExtensionUI(globalItem) : null
	}, [extOpen, globalExtensionItems, sessionExtChips])
	const globalConfigExtensions = useMemo(
		() => globalExtensions.filter(item => item.enabled && item.configurable),
		[globalExtensions],
	)

	const selectExtension = useCallback((name: string) => {
		setExtOpen(name)
		setExtFields(seedExtFields(extChips.find(ui => ui.extension === name)))
	}, [extChips])

	const openExtensionConfig = useCallback((name: string) => {
		setSettingsOpen(false)
		selectExtension(name)
	}, [selectExtension])

  useEffect(() => {
    if (!extOpen) return
    if (extChips.some(ui => ui.extension === extOpen) || globalExtensionItems.some(item => item.name === extOpen)) return
    if (!extChips.length && !globalExtensionItems.length) {
      setExtOpen(null)
      return
    }
    selectExtension(extChips[0]?.extension ?? globalExtensionItems[0].name)
  }, [extChips, extOpen, globalExtensionItems, selectExtension])

  const openExt = useCallback((name?: string) => {
    const chips = extChips
    if (!chips.length) return
    const pick = name && chips.some(ui => ui.extension === name)
      ? name
      : (visibleStatusChips(chips).length < chips.length
        ? chips[visibleStatusChips(chips).length].extension
        : chips[0].extension)
    if (pick === extOpen) return
	selectExtension(pick)
	}, [extChips, extOpen, selectExtension])

  const makeSession = useCallback(async (workspaceId?: string | null) => {
    const s = await api.create(sessionCreateBody(workspaceId, { provider: view.provider, model: view.model, thinkingEffort: view.thinkingEffort }, models))
    await refreshList()
    setCurrentId(s.id)
    setSelectedWs(s.workspaceId ?? workspaceId ?? null)
    try {
      setView(loadHistory(await api.get(s.id)))
    } catch {
      setView({ ...emptyView(), cwd: s.cwd, model: s.model, provider: s.provider, thinkingEffort: s.thinkingEffort ?? '' })
    }
    saveLastComposerModel({ provider: s.provider, model: s.model, thinkingEffort: s.thinkingEffort ?? '' })
    setTab('conversation')
	setEdit(null)
    if (s.workspaceId) {
      setExpanded(e => ({ ...e, [s.workspaceId!]: true }))
      setShowAll(a => ({ ...a, [s.workspaceId!]: true }))
    }
    return s
  }, [api, models, refreshList, view.model, view.provider, view.thinkingEffort])

	const uploadClientFiles = useCallback(async (target: 'new' | 'edit', files: File[]) => {
	  if (!files.length || uploading) return
	  setUploading(true)
	  try {
		let id = currentId
		if (!id) id = (await makeSession(selectedWs)).id
		const added = await Promise.all(files.map(file => api.uploadAttachment(id!, file)))
		if (target === 'edit') {
		  setEdit(e => e ? { ...e, draft: { ...e.draft, attachments: [...e.draft.attachments, ...added.filter(a => !e.draft.attachments.some(old => old.id === a.id))] } } : e)
		} else {
		  setDraft(d => ({ ...d, attachments: [...d.attachments, ...added.filter(a => !d.attachments.some(old => old.id === a.id))] }))
		}
	  } catch (e) { toast.from(e) }
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
		if (!view.busy && !uploading && view.runtimeReady !== false) setFileDragActive(true)
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
		if (!files.length || view.busy || uploading || view.runtimeReady === false) return
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
    try { await makeSession(wsId ?? selectedWs) }
    catch (e) { toast.from(e) }
  }, [makeSession, selectedWs])

  const sendContent = useCallback(async (content: Content[], parentId?: string, editedMessageId?: string, delivery?: 'steer' | 'queue') => {
	if (!content.some(c => (c.type === 'text' && !!c.text?.trim()) || c.type !== 'text')) return
    try {
      let id = currentId
      if (!id) {
        const s = await makeSession(selectedWs)
        id = s.id
      }
      let result: { handled?: boolean; notice?: string; error?: boolean; accepted?: boolean | string; sessionId?: string; cwd?: string; workspaceId?: string } | undefined
      try {
        const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
		result = await api.prompt(id, content, spec || undefined, parentId, delivery)
        if (result?.handled) {
          if (result.notice) (result.error ? toast.error : toast.info)(result.notice)
          setDraft(d => ({ ...d, text: '' }))
          if (!result.error) {
            if (result.sessionId && result.sessionId !== id) {
              await refreshList()
              await openSession(result.sessionId)
            } else {
              await openSession(id)
            }
          }
          return
        }
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) {
          toast.error(e.message)
          return
        }
        throw e
      }
	  if (result?.accepted === 'queued') {
		setDraft({ text: '', attachments: [] })
		try {
		  const detail = await api.get(id)
		  setView(v => ({ ...loadHistory(detail), busy: true }))
		} catch (e) { toast.from(e) }
		return
	  }
	  if (result?.accepted === 'steered') {
		setDraft({ text: '', attachments: [] })
		setView(v => appendOptimisticUser(v, content))
		return
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
      toast.from(e)
    }
	}, [api, currentId, listen, makeSession, openSession, selectedWs, view.model, view.provider])

	const send = useCallback((delivery?: 'steer' | 'queue') => {
	  const content: Content[] = [...(draft.text.trim() ? [{ type: 'text', text: draft.text } as Content] : []), ...draft.attachments]
	  return sendContent(content, undefined, undefined, delivery)
	}, [draft, sendContent])

	const steerQueued = useCallback(async (queueId?: string) => {
	  const items = view.queued ?? []
	  const item = queueId ? items.find(q => q.id === queueId) : items.at(-1)
	  if (!currentId || !item) return
	  try {
	    const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
	    const result = await api.prompt(currentId, [], spec || undefined, undefined, 'steer', item.id)
	    if (result?.accepted === 'steered') {
	      setView(v => ({
	        ...appendOptimisticUser(v, item.content ?? []),
	        queued: (v.queued ?? []).filter(q => q.id !== item.id),
	      }))
	      return
	    }
	    const detail = await api.get(currentId)
	    setView(v => ({ ...v, queued: detail.queued ?? [], busy: v.busy || !!detail.running }))
	  } catch (e) { toast.from(e) }
	}, [api, currentId, view.model, view.provider, view.queued])

	const sendEdit = useCallback(() => {
	  if (!edit) return Promise.resolve()
	  const content: Content[] = [...(edit.draft.text.trim() ? [{ type: 'text', text: edit.draft.text } as Content] : []), ...edit.draft.attachments]
	  return sendContent(content, edit.parentId, edit.messageId)
	}, [edit, sendContent])

  const stop = useCallback(async () => {
    if (!currentId) return
    try { await api.abort(currentId) } catch (e) {
      toast.from(e)
    }
  }, [api, currentId])

  const switchModel = useCallback(async (spec: string) => {
    const next = models.find(m => m.spec === spec)
    const [p, m] = spec.includes('/') ? spec.split('/') : [view.provider, spec]
    const provider = next?.provider || p || view.provider
    const model = next?.id || m || view.model
    const thinkingEffort = clampThinkingEffort(view.thinkingEffort, next)
    setView(v => ({ ...v, provider, model, thinkingEffort }))
    saveLastComposerModel({ provider, model, thinkingEffort })
    setModelOpen(false)
    if (!currentId) return
    try {
      const out = await api.patch(currentId, { model: spec, thinkingEffort })
	  setView(v => ({ ...v, model: out.model, provider: out.provider, thinkingEffort: out.thinkingEffort ?? thinkingEffort }))
      saveLastComposerModel({ provider: out.provider, model: out.model, thinkingEffort: out.thinkingEffort ?? thinkingEffort })
    } catch (e) {
      toast.from(e)
    }
  }, [api, currentId, models, view.model, view.provider, view.thinkingEffort])

  const selectedModel = useMemo(() => models.find(m => m.provider === view.provider && m.id === view.model), [models, view.model, view.provider])
	const switchThinking = useCallback(async (thinkingEffort: string) => {
		setView(v => ({ ...v, thinkingEffort }))
		if (view.provider && view.model) saveLastComposerModel({ provider: view.provider, model: view.model, thinkingEffort })
		if (!currentId) return
		try {
			const out = await api.patch(currentId, { thinkingEffort })
			setView(v => ({ ...v, thinkingEffort: out.thinkingEffort ?? thinkingEffort }))
			if (view.provider && view.model) saveLastComposerModel({ provider: view.provider, model: view.model, thinkingEffort: out.thinkingEffort ?? thinkingEffort })
		} catch (e) { toast.from(e) }
	}, [api, currentId, view.model, view.provider])

  const byId = useMemo(() => new Map(sessions.map(s => [s.id, s])), [sessions])

  const trees = useMemo(() => {
    const temporary = temporaryTreeRevealIds.map(id => byId.get(id)).filter((s): s is SessionInfo => !!s)
    const displayRows = (workspaceId: string | undefined, rows: SessionInfo[]) => {
      const regular = rows.filter(session => session.forkMode !== 'tree')
      const selected = temporary.filter(session => session.forkMode === 'tree' && session.workspaceId === workspaceId)
      if (!selected.length) return regular

      const regularIds = new Set(regular.map(session => session.id))
      const selectedIds = new Set(selected.map(session => session.id))
      const anchorOf = (session: SessionInfo): string | null => {
        const seen = new Set<string>()
        let anchor = session.parentSessionId
        while (anchor && !seen.has(anchor)) {
          if (regularIds.has(anchor) || selectedIds.has(anchor)) return anchor
          seen.add(anchor)
          anchor = byId.get(anchor)?.parentSessionId
        }
        return null
      }
      const children = new Map<string | null, SessionInfo[]>()
      for (const session of selected) {
        const anchor = anchorOf(session)
        children.set(anchor, [...(children.get(anchor) ?? []), session])
      }
      const out: SessionInfo[] = []
      const appendSelected = (anchor: string | null, ancestors = new Set<string>()) => {
        for (const session of children.get(anchor) ?? []) {
          if (ancestors.has(session.id)) continue
          out.push(session)
          const nextAncestors = new Set(ancestors)
          nextAncestors.add(session.id)
          appendSelected(session.id, nextAncestors)
        }
      }
      for (const session of regular) {
        out.push(session)
        appendSelected(session.id)
      }
      appendSelected(null)
      return out
    }
    const used = new Set<string>()
    const groups = workspaces.map(ws => {
      const order = ws.sessionIds?.length
        ? [...ws.sessionIds, ...sessions.filter(s => s.workspaceId === ws.id && !ws.sessionIds!.includes(s.id)).map(s => s.id)]
        : sessions.filter(s => s.workspaceId === ws.id).map(s => s.id)
      const allRows = order.map(id => byId.get(id)).filter((s): s is SessionInfo => !!s)
      allRows.forEach(s => used.add(s.id))
      const rows = displayRows(ws.id, allRows)
      return { ws, rows }
    })
    const ungrouped = displayRows(undefined, sessions.filter(s => !used.has(s.id)))
    return { groups, ungrouped }
  }, [byId, sessions, temporaryTreeRevealIds, workspaces])

  const treeAvailable = useMemo(() => {
    if (!currentId) return false
    const current = byId.get(currentId)
    return current?.forkMode === 'tree' || sessions.some(session => (
      session.forkMode === 'tree' && session.parentSessionId === currentId
    ))
  }, [byId, currentId, sessions])

  const selectTreeSession = useCallback(async (id: string): Promise<boolean> => {
    const target = byId.get(id)
    if (!target) {
      toast.error(t('tree.missing'))
      return false
    }
    setTemporaryTreeRevealIds(current => current.includes(id) ? current : [...current, id])
    if (target.workspaceId) {
      setExpanded(value => ({ ...value, [target.workspaceId!]: true }))
      setShowAll(value => ({ ...value, [target.workspaceId!]: true }))
    }
    const opened = await openSession(id)
    if (!opened) setTemporaryTreeRevealIds(current => current.filter(item => item !== id))
    else setTab('conversation')
    return opened
  }, [byId, openSession, t])

  const localHits = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return []
    return sessions.filter(s => {
      if (s.forkMode === 'tree') return false
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
      if (byId.get(h.id)?.forkMode === 'tree') continue
      const prev = map.get(h.id)
      map.set(h.id, { id: h.id, title: h.title || prev?.title || untitled, workspace: h.workspaceTitle || prev?.workspace, snippet: h.snippet })
    }
    return [...map.values()].slice(0, 20)
  }, [byId, hits, localHits, untitled, workspaces])

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
	  try {
		const child = await api.fork(currentId, node.id)
		await refreshList()
		await openSession(child.id)
	  } catch (e) { toast.from(e) }
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
	  } catch (e) { toast.from(e) }
	}, [api, currentId, openSession, view.allEntries, view.busy])

  const empty = view.nodes.length === 0
  const stats = useMemo(() => sessionStats(view), [view])
  const queued = view.queued ?? []
  const extQueued = view.extQueued ?? []
  const steerShortcut = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘+Enter' : t('queue.steerHint')
  const composer = (
    <>
    {extQueued.length ? (
      <ul className="queued-list ext-queued" data-testid="ext-queued-list">
        {extQueued.map(item => {
          const text = (item.content ?? []).filter(c => c.type === 'text').map(c => c.text).join(' ')
          return (
            <li key={item.id} className="queued-item" data-testid="ext-queued-item">
              <span className="queued-origin">ext</span>
              <span className="queued-text">{text || t('queue.attachment')}</span>
            </li>
          )
        })}
      </ul>
    ) : null}
    {queued.length ? (
      <ul className="queued-list" data-testid="queued-list">
        {queued.map((item, i) => {
          const text = (item.content ?? []).filter(c => c.type === 'text').map(c => c.text).join(' ')
          const tail = i === queued.length - 1
          return (
            <li key={item.id} className="queued-item" data-testid="queued-item">
              <span className="queued-text">{text || t('queue.attachment')}</span>
              {tail ? <kbd className="queued-kbd" title={t('queue.steerAria')}>{steerShortcut}</kbd> : null}
              <button type="button" className="queued-steer" data-testid="queued-steer" onClick={() => void steerQueued(item.id)}>{t('queue.steer')}</button>
              <button type="button" className="queued-remove" data-testid="queued-remove" aria-label={t('queue.remove')} onClick={() => {
                if (!currentId) return
                const ids = queued.filter(q => q.id !== item.id).map(q => q.id)
                void api.patch(currentId, { queued: ids }).then(() => api.get(currentId)).then(detail => {
                  setView(v => ({ ...v, queued: detail.queued ?? [] }))
                }).catch(e => toast.from(e))
              }}><IClose /></button>
            </li>
          )
        })}
      </ul>
    ) : null}
    <Composer
	  api={api}
	  draft={draft}
	  onChange={setDraft}
      onSend={d => void send(d)}
      onStop={() => void stop()}
      onSteerQueued={() => void steerQueued()}
	  onAttach={() => setAttachmentTarget('new')}
	  onFiles={files => void uploadClientFiles('new', files)}
	  uploading={uploading}
      busy={view.busy}
      disabled={!!currentId && view.runtimeReady === false}
      loading={!!currentId && view.runtimeReady === false}
      hasQueued={queued.length > 0}
      cwd={view.cwd}
      model={view.model}
      commands={view.commands ?? []}
      onEnsureSession={async () => { if (!currentId) await makeSession(selectedWs) }}
      onPickModel={() => setModelOpen(true)}
	  thinkingLevels={selectedModel?.thinkingLevels}
	  thinkingEffort={view.thinkingEffort}
	  defaultThinking={selectedModel?.defaultThinking}
	  onThinking={effort => void switchThinking(effort)}
	  contextUsage={view.contextUsage}
	  stats={stats}
    />
    </>
  )

  const visibleRows = (wsId: string, rows: SessionInfo[]) => {
    if (showAll[wsId]) return rows
    const idx = currentId ? rows.findIndex(s => s.id === currentId) : -1
    const n = idx >= SHOW ? idx + 1 : SHOW
    return rows.slice(0, n)
  }

  const rememberSessionRow = useCallback((id: string, element: HTMLButtonElement | null) => {
    if (element) sessionRowRefs.current.set(id, element)
    else sessionRowRefs.current.delete(id)
  }, [])

  useEffect(() => {
    if (!currentId || !temporaryTreeRevealIds.includes(currentId)) return
    const frame = requestAnimationFrame(() => {
      const row = sessionRowRefs.current.get(currentId)
      row?.scrollIntoView({ block: 'nearest' })
      row?.focus({ preventScroll: true })
    })
    return () => cancelAnimationFrame(frame)
  }, [currentId, temporaryTreeRevealIds, trees])

  return (
    <div className={`app${collapsed && !compactLayout ? ' sidebar-collapsed' : ''}${mobileSidebarOpen ? ' mobile-sidebar-open' : ''}${(settingsOpen || modelOpen || dirOpen || attachmentTarget || extOpen || treeOpen) ? ' modal-open' : ''}`}>
	  {fileDragActive ? createPortal(<div className="global-drop-overlay" data-testid="global-drop-overlay">
		<div className="global-drop-visual"><span><IImage /></span><span><IFile /></span><span><IPlus /></span></div>
		<strong>{edit ? t('drop.editTitle') : t('drop.newTitle')}</strong>
		<p>{t('drop.hint')}</p>
	  </div>, document.body) : null}
      <aside
        ref={sidebarRef}
        id="app-sidebar"
        className={`sidebar${collapsed && wide && !compactLayout ? ' fading' : ''}${!sidebarWide ? ' rail' : ''}${!sidebarWide && everWide.current ? ' rail-in' : ''}`}
        style={sidebarWide ? { width: 280 } : undefined}
        aria-hidden={compactLayout && !mobileSidebarOpen}
        onKeyDown={onSidebarKeyDown}
      >
        <div className="sidebar-top">
          {sidebarWide ? <div className="wordmark wide-only">ki</div> : null}
          <button type="button" className="icon-btn" onClick={() => compactLayout ? setMobileSidebarOpen(false) : setCollapsed(v => !v)} aria-label={compactLayout || !collapsed ? t('sidebar.collapse') : t('sidebar.open')} title={compactLayout || !collapsed ? t('sidebar.collapse') : t('sidebar.open')}><IPanel /></button>
        </div>
        <button type="button" className="new-session" data-testid="new-session" onClick={() => { setMobileSidebarOpen(false); void newSession() }}>
          <IPlus />{sidebarWide ? <span className="wide-only">{t('session.new')}</span> : null}
        </button>
        {sidebarWide ? (
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
              <button type="button" className="icon-btn" data-testid="add-workspace" onClick={openDirectoryFromSidebar} aria-label={t('workspace.add')} title={t('workspace.add')}><IFolder /></button>
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
            <button type="button" className="icon-btn" data-testid="add-workspace" onClick={openDirectoryFromSidebar} aria-label={t('workspace.add')} title={t('workspace.add')}><IFolder /></button>
          </div>
        )}
        <div className="session-list">
          {sidebarWide && filter.trim() ? (
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
          {sidebarWide && !filter.trim() && trees.groups.map(({ ws, rows }) => {
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
                    void api.moveWorkspace(id, before).then(setWorkspaces).catch(er => toast.from(er))
                  }}
                >
                  <button type="button" className="ws-toggle" onClick={() => { setSelectedWs(ws.id); setExpanded(e => ({ ...e, [ws.id]: !open })) }}>
                    <IChev open={open} />
                    <IFolder />
                    <span className="title">{ws.title}{ws.temp ? ` · ${t('workspace.temp')}` : ''}</span>
                  </button>
                  <button
                    type="button"
                    className="icon-btn tiny"
                    aria-label={t('workspace.menu')}
                    aria-haspopup="menu"
                    aria-expanded={menu?.kind === 'ws' && menu.id === ws.id}
                    aria-controls={menu?.kind === 'ws' && menu.id === ws.id ? menuID : undefined}
                    onClick={e => openMenu('ws', ws.id, e.currentTarget)}
                  ><IDots /></button>
                  <button type="button" className="icon-btn tiny" data-testid="ws-new-session" aria-label={t('session.new')} onClick={() => void newSession(ws.id)}><IPlus /></button>
                </div>
                {shown.map((s, i) => (
                  <div
                    key={s.id}
                    className={`session-row${s.id === currentId ? ' active' : ''}${temporaryTreeRevealIds.includes(s.id) ? ' tree-focus' : ''}`}
                    data-testid="session-row"
                    data-tree-focus={temporaryTreeRevealIds.includes(s.id) || undefined}
                    draggable={!temporaryTreeRevealIds.includes(s.id)}
                    onDragStart={e => { if (!temporaryTreeRevealIds.includes(s.id)) e.dataTransfer.setData('text/sess', `${ws.id}:${s.id}`) }}
                    onDragOver={e => e.preventDefault()}
                    onDrop={e => {
                      if (temporaryTreeRevealIds.includes(s.id)) return
                      const raw = e.dataTransfer.getData('text/sess')
                      const [fromWs, sid] = raw.split(':')
                      if (fromWs !== ws.id || !sid || sid === s.id) return
                      const rect = e.currentTarget.getBoundingClientRect()
                      const before = e.clientY < rect.top + rect.height / 2 ? s.id : shown[i + 1]?.id
                      void api.moveSession(ws.id, sid, before ?? null).then(() => refreshList()).catch(er => toast.from(er))
                    }}
                  >
                    <button
                      ref={element => rememberSessionRow(s.id, element)}
                      type="button"
                      className="session-main"
                      aria-current={s.id === currentId ? 'page' : undefined}
                      onClick={() => { if (s.forkMode === 'tree') setTab('conversation'); void openSession(s.id) }}
                    >
                      <span className={`dot${s.running ? ' on' : ''}`} />
                      {s.pinned && s.forkMode !== 'tree' ? <span className="pin-mark" aria-label={t('session.pinned')}><IPin /></span> : null}
                      {temporaryTreeRevealIds.includes(s.id) ? <span className="tree-session-mark" aria-hidden><IFork /></span> : null}
                      <span className="meta">
                        <div className="title" data-testid="session-title">{s.title || untitled}</div>
                        <div className="sub">{temporaryTreeRevealIds.includes(s.id) ? `${t('tree.label')} · ${sessionPathLabel(s.id, byId, untitled)}` : s.model}</div>
                      </span>
                    </button>
                    <button
                      type="button"
                      className="icon-btn tiny"
                      aria-label={t('session.menu')}
                      aria-haspopup="menu"
                      aria-expanded={menu?.kind === 'sess' && menu.id === s.id}
                      aria-controls={menu?.kind === 'sess' && menu.id === s.id ? menuID : undefined}
                      onClick={e => openMenu('sess', s.id, e.currentTarget)}
                    ><IDots /></button>
                  </div>
                ))}
                {open && rows.length > shown.length ? (
                  <button type="button" className="show-more" data-testid="show-more" onClick={() => setShowAll(a => ({ ...a, [ws.id]: true }))}>{t('session.showMore')}</button>
                ) : null}
              </div>
            )
          })}
          {sidebarWide && !filter.trim() && trees.ungrouped.length ? (
            <div>
              <div className="cwd-label">{t('session.ungrouped')}</div>
              {trees.ungrouped.map(s => (
                <button
                  key={s.id}
                  ref={element => rememberSessionRow(s.id, element)}
                  type="button"
                  className={`session-row${s.id === currentId ? ' active' : ''}${temporaryTreeRevealIds.includes(s.id) ? ' tree-focus' : ''}`}
                  data-testid="session-row"
                  data-tree-focus={temporaryTreeRevealIds.includes(s.id) || undefined}
                  aria-current={s.id === currentId ? 'page' : undefined}
                    onClick={() => { if (s.forkMode === 'tree') setTab('conversation'); void openSession(s.id) }}
                >
                  <span className={`dot${s.running ? ' on' : ''}`} />
                  {temporaryTreeRevealIds.includes(s.id) ? <span className="tree-session-mark" aria-hidden><IFork /></span> : null}
                  <span className="meta">
                    <div className="title" data-testid="session-title">{s.title || untitled}</div>
                    <div className="sub">{temporaryTreeRevealIds.includes(s.id) ? `${t('tree.label')} · ${sessionPathLabel(s.id, byId, untitled)}` : s.model}</div>
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
            onClick={openSettingsFromSidebar}
            aria-label={t('settings.open')}
            title={t('settings.open')}
          >
            <IGear />{sidebarWide ? <span className="wide-only">{t('settings.open')}</span> : null}
          </button>
        </div>
      </aside>

      {compactLayout ? (
        // Why: the scrim is a pointer shortcut, not a second keyboard control;
        // the labeled collapse button inside the trapped drawer owns that job.
        <div
          className="mobile-sidebar-backdrop"
          aria-hidden="true"
          onClick={() => setMobileSidebarOpen(false)}
        />
      ) : null}

      <main ref={mainRef} className="main" aria-hidden={compactLayout && mobileSidebarOpen}>
        <header className="conv-header">
          <div className="title-row">
            {compactLayout ? (
              <button
                ref={mobileNavToggleRef}
                type="button"
                className="icon-btn mobile-nav-toggle"
                data-testid="mobile-nav-toggle"
                aria-label={t('sidebar.open')}
                aria-expanded={mobileSidebarOpen}
                aria-controls="app-sidebar"
                onClick={() => setMobileSidebarOpen(true)}
              >
                <IPanel />
              </button>
            ) : null}
            <div className="conv-title">{view.title || (currentId ? untitled : 'ki')}</div>
            <div className="ext-chips" data-testid="ext-chips">
              {extVisible.map(ui => (
                <button
                  key={ui.extension}
                  type="button"
                  className={`ext-chip${globalExtensionItems.some(item => item.name === ui.extension) ? ' ext-global-chip' : ''} tone-${ui.status?.tone || 'info'}${extOpen === ui.extension ? ' on' : ''}`}
                  data-testid={`ext-chip-${ui.extension}`}
                  title={globalExtensionItems.find(item => item.name === ui.extension) ? extensionRuntimeTitle(globalExtensionItems.find(item => item.name === ui.extension)!) : localizedExtensionText(ui.status?.text, globalExtensions.find(item => item.name === ui.extension)?.i18n, lang)}
                  onClick={() => openExt(ui.extension)}
                >
                  {localizedExtensionText(ui.status?.text, globalExtensions.find(item => item.name === ui.extension)?.i18n, lang)}
                </button>
              ))}
              {extChips.length ? (
                <button
                  type="button"
                  className="ext-chip ext-chip-more"
                  data-testid="ext-chips-more"
                  title={extHidden ? t('ext.moreCount', { n: extHidden }) : t('ext.more')}
                  aria-label={extHidden ? t('ext.moreCount', { n: extHidden }) : t('ext.more')}
                  onClick={() => openExt()}
                >
                  <span className="ext-more-wide">{extHidden ? `+${extHidden}` : <IDots />}</span>
                  <span className="ext-more-narrow">{t('ext.inspector')} · {extChips.length}</span>
                </button>
              ) : null}
            </div>
          </div>
          <div className="tabs">
            <button type="button" className={`tab${tab === 'conversation' ? ' active' : ''}`} data-testid="tab-conversation" onClick={() => setTab('conversation')}>{t('tab.conversation')}</button>
            <button type="button" className={`tab${tab === 'trajectory' ? ' active' : ''}`} data-testid="tab-trajectory" onClick={() => setTab('trajectory')}>{t('tab.trajectory')}</button>
            <button type="button" className={`tab${tab === 'config' ? ' active' : ''}`} data-testid="tab-config" onClick={() => setTab('config')}>{t('tab.info')}</button>
          </div>
        </header>
        {tab === 'config' ? (
          <div className="conv-body">
            <SessionConfig
              api={api}
              sessionId={currentId}
              workspaceTitle={workspaces.find(w => w.id === (selectedWs ?? sessions.find(s => s.id === currentId)?.workspaceId))?.title}
              busy={view.busy}
              onEdit={page => { drawerDialogSource.current = null; setSettingsPage(page); setSettingsOpen(true) }}
              treeAvailable={treeAvailable}
              onTreeOpen={() => setTreeOpen(true)}
            />
          </div>
        ) : tab === 'trajectory' ? (
          <div className="conv-body">
            <TrajectoryView records={view.records} requests={view.requests} selectId={inspId} />
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
        <div className="menu-mask" onClick={() => closeMenu()}>
          <div
            ref={menuRef}
            id={menuID}
            className="pop-menu"
            data-testid="pop-menu"
            style={menuStyle}
            role="menu"
            aria-label={menu.kind === 'ws' ? t('workspace.menu') : t('session.menu')}
            onKeyDown={onMenuKeyDown}
            onClick={e => e.stopPropagation()}
          >
            {menu.kind === 'ws' ? (
              <>
                <button type="button" role="menuitem" onClick={() => {
                  const ws = workspaces.find(w => w.id === menu.id)
                  handoffMenuToDialog()
                  setRename({ kind: 'ws', id: menu.id, title: ws?.title ?? '' })
                }}
                >
                  <IEdit /> {t('workspace.rename')}
                </button>
                <button type="button" role="menuitem" className="danger" onClick={() => {
                  const ws = workspaces.find(w => w.id === menu.id)
                  const n = sessions.filter(s => s.workspaceId === menu.id).length
                  handoffMenuToDialog()
                  setConfirmDel({ kind: 'ws', id: menu.id, label: ws?.title ?? '', extra: t('delete.workspaceExtra', { n, path: ws?.path ?? '' }) })
                }}
                >
                  <ITrash /> {t('workspace.delete')}
                </button>
              </>
            ) : (
              <>
                {byId.get(menu.id)?.forkMode !== 'tree' ? (
                  <button type="button" role="menuitem" onClick={() => {
                    const s = byId.get(menu.id)
                    void api.patch(menu.id, { pinned: !s?.pinned }).then(() => refreshList())
                    closeMenu()
                  }}
                  >
                    <IPin /> {byId.get(menu.id)?.pinned ? t('session.unpin') : t('session.pin')}
                  </button>
                ) : null}
                <button type="button" role="menuitem" onClick={() => {
                  const s = byId.get(menu.id)
                  handoffMenuToDialog()
                  setRename({ kind: 'sess', id: menu.id, title: s?.title ?? '' })
                }}
                >
                  <IEdit /> {t('session.rename')}
                </button>
                <button type="button" role="menuitem" className="danger" onClick={() => {
                  const s = byId.get(menu.id)
                  handoffMenuToDialog()
                  setConfirmDel({ kind: 'sess', id: menu.id, label: s?.title || untitled })
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
        <Modal title={t('rename.title')} onClose={() => setRename(null)} testid="rename-dialog" initialFocusRef={renameInputRef}>
          <input ref={renameInputRef} className="session-search" data-testid="rename-input" aria-label={t('rename.title')} value={rename.title} onChange={e => setRename({ ...rename, title: e.target.value })} />
          <div className="modal-actions">
            <button
              type="button"
              className="primary-btn"
              data-testid="rename-ok"
              onClick={() => {
                void (rename.kind === 'ws' ? api.patchWorkspace(rename.id, { title: rename.title }) : api.patch(rename.id, { title: rename.title }))
                  .then(() => refreshList())
                  .then(() => setRename(null))
                  .catch(e => toast.from(e))
              }}
            >
              {t('rename.ok')}
            </button>
          </div>
        </Modal>
      ) : null}

      {extOpen ? (
        <Modal title={t('ext.inspector')} onClose={() => setExtOpen(null)} testid="ext-panel" className="modal-ext modal-flush">
          <ExtensionInspector
            items={sessionExtChips}
            globalItems={globalExtensionItems}
            selected={extSelected}
            selectedName={extOpen}
            fields={extFields}
            onSelect={selectExtension}
            onField={(id, value) => setExtFields(v => ({ ...v, [id]: value }))}
            onAction={id => {
              if (!currentId || !extSelected) return
              void api.extensionUI(currentId, { kind: 'action', extension: extSelected.extension, value: id }).catch(e => toast.from(e))
            }}
            onSubmit={fields => {
              if (!currentId || !extSelected) return
              void api.extensionUI(currentId, { kind: 'submit', extension: extSelected.extension, fields }).catch(e => toast.from(e))
            }}
			 renderConfig={name => <ExtensionConfigEditor api={api} name={name} onClose={() => setExtOpen(null)} embedded models={models} defaultModel={defaultModel} />}
          />
        </Modal>
      ) : null}

      {(() => {
        const prompt = (view.extensionUi ?? []).find(u => u.prompt)?.prompt
        const extName = (view.extensionUi ?? []).find(u => u.prompt)?.extension
        if (!prompt || !extName || !currentId) return null
        const extensionI18n = globalExtensions.find(item => item.name === extName)?.i18n
        return (
          <Modal title={localizedExtensionText(prompt.title || extName, extensionI18n, lang)} onClose={() => void api.extensionUI(currentId, { kind: prompt.kind, extension: extName, ok: false }).catch(e => toast.from(e))} testid="ext-ui-prompt">
            {prompt.message ? <p>{localizedExtensionText(prompt.message, extensionI18n, lang)}</p> : null}
            {prompt.kind === 'select' ? (
              <div className="ext-select">
                {(prompt.options ?? []).map(opt => (
                  <button
                    key={opt}
                    type="button"
                    data-testid={`ext-select-${opt}`}
                    onClick={() => void api.extensionUI(currentId, { kind: 'select', extension: extName, ok: true, value: opt }).catch(e => toast.from(e))}
                  >
                    {opt}
                  </button>
                ))}
              </div>
            ) : (
              <div className="modal-actions">
                <button type="button" data-testid="ext-confirm-cancel" onClick={() => void api.extensionUI(currentId, { kind: 'confirm', extension: extName, ok: false }).catch(e => toast.from(e))}>{t('ext.cancel')}</button>
                <button type="button" className="primary-btn" data-testid="ext-confirm-ok" onClick={() => void api.extensionUI(currentId, { kind: 'confirm', extension: extName, ok: true }).catch(e => toast.from(e))}>{t('ext.ok')}</button>
              </div>
            )}
          </Modal>
        )
      })()}

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
                      setView(v => keepComposer(v))
                    }
                    if (confirmDel.kind === 'ws' && selectedWs === confirmDel.id) {
                      setSelectedWs(null)
                      if (sessions.find(s => s.id === currentId)?.workspaceId === confirmDel.id) {
                        setCurrentId(null)
                        setView(v => keepComposer(v))
                      }
                    }
                    setConfirmDel(null)
                    return refreshList()
                  })
                  .catch(e => toast.from(e))
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
        restoreFocusRef={drawerDialogSource.current === 'dir' ? mobileNavToggleRef : undefined}
        onClose={() => { setDirOpen(false); drawerDialogSource.current = null }}
        onOpen={path => {
          setDirBusy(true)
          void api.createWorkspace(path).then(ws => {
            setDirOpen(false)
            drawerDialogSource.current = null
            if (ws && ws.id) {
              setSelectedWs(ws.id)
              setExpanded(e => ({ ...e, [ws.id]: true }))
            }
            return refreshList()
          }).catch(e => toast.from(e))
            .finally(() => setDirBusy(false))
        }}
      />
	  <SessionTreeBrowser
		open={treeOpen}
		sessions={sessions}
		workspaces={workspaces}
		currentId={currentId}
		onClose={() => setTreeOpen(false)}
		onSelect={selectTreeSession}
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

		<ModelPickerDialog
			open={modelOpen}
			models={models}
			value={view.provider && view.model ? `${view.provider}/${view.model}` : view.model}
			onSelect={spec => void switchModel(spec)}
			onClose={() => setModelOpen(false)}
		/>

      {settingsOpen ? (
        <Modal
          title={t('settings.title')}
          onClose={() => { setSettingsOpen(false); drawerDialogSource.current = null }}
          testid="settings"
          restoreFocusRef={drawerDialogSource.current === 'settings' ? mobileNavToggleRef : undefined}
        >
		  <nav className="tabs settings-tabs" role="tablist" aria-label={t('settings.title')} onKeyDown={onSettingsTabKeyDown}>
            {([
              ['providers', t('settings.providers')],
              ['skills', t('settings.skills')],
              ['extensions', t('settings.extensions')],
              ['message', t('settings.message')],
              ['appearance', t('settings.appearanceLanguage')],
            ] as const).map(([page, label]) => (
              <button
                key={page}
                id={`settings-tab-${page}-control`}
                type="button"
                role="tab"
                aria-selected={settingsPage === page}
                aria-controls={`settings-panel-${page}`}
                tabIndex={settingsPage === page ? 0 : -1}
                className={`tab${settingsPage === page ? ' active' : ''}`}
                data-testid={`settings-tab-${page}`}
                data-settings-page={page}
                onClick={() => setSettingsPage(page)}
              >
                {label}
              </button>
            ))}
		  </nav>
            {SETTINGS_PAGES.map(page => (
              <div
                key={page}
                className="settings-page"
                id={`settings-panel-${page}`}
                role="tabpanel"
                aria-labelledby={`settings-tab-${page}-control`}
                tabIndex={page === settingsPage ? 0 : -1}
                hidden={page !== settingsPage}
              >
                {page !== settingsPage ? null : page === 'providers' ? <ProviderSettings api={api} onChanged={refreshModels} /> : page === 'skills' ? (
                  <SettingsToggles kind="skills" api={api} workspaceId={selectedWs} />
                ) : page === 'extensions' ? (
                  <SettingsToggles kind="extensions" api={api} onConfigure={openExtensionConfig} onChanged={refreshExtensions} />
                ) : page === 'message' ? (
                  <MessageSettings api={api} />
                ) : (
                  <div className="preference-page" data-testid="appearance-settings">
                    <header className="settings-page-title"><div><h3>{t('settings.appearanceLanguage')}</h3><p>{t('settings.preferenceHint')}</p></div></header>
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
            ))}
        </Modal>
      ) : null}
    </div>
  )
}
