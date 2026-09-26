import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type DragEvent as ReactDragEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client } from './api/client'
import { AuthLoading, LoginScreen } from './features/settings/AuthScreen'
import { ChatView } from './features/chat/Chat'
import { RequestNav } from './features/chat/RequestNav'
import { Composer, type Draft } from './features/chat/Composer'
import { AttachmentBrowser } from './features/attachments/AttachmentBrowser'
import { DirectoryBrowser } from './features/sessions/DirectoryBrowser'
import { ExtensionConfigEditor, MessageSettings, NotificationSettings, SessionConfig, SettingsToggles } from './features/settings/SessionConfig'
import { PromptSettings } from './features/settings/PromptSettings'
import { ModelPickerDialog } from './features/settings/ModelPickerDialog'
import { ProviderSettings } from './features/settings/ProviderSettings'
import { IChev, IChevDown, IClose, IDots, IEdit, IFile, IFolder, IFork, IGear, IImage, IPanel, IPin, IPlus, ISearch, ITrash } from './components/icons'
import { appendOptimisticUser, applyEvent, applyIndex, applyRuntimeCatalog, applyTail, clampThinkingEffort, emptyView, hydrateEntries, initialView, keepComposer, latestStats, loadHistory, loadLastComposerModel, pickComposerModel, saveLastComposerModel, sessionCreateBody, userRequests } from './lib/model'
import type { CatalogExtension, ChatNode, Content, ExtensionUI, ModelInfo, PushEvent, SearchHit, SessionInfo, ViewState, WorkspaceInfo } from './api/types'
import { TrajectoryView } from './features/chat/Trajectory'
import { useI18n } from './i18n/index'
import { toast } from './components/toast'
import { ExtensionInspector, localizedExtensionText, seedExtFields, statusChips, visibleStatusChips } from './features/settings/ExtensionPanel'
import { useDialogFocus } from './hooks/useDialogFocus'
import { useTabFocus } from './hooks/useTabFocus'
import { useServerEvents } from './hooks/useServerEvents'
import { currentPermission, loadNotifyPref, notifyCompletion, requestPermission, saveNotifyPref, showNotification, type NotifyPermission } from './lib/notifications'
import { focusedSession } from './lib/tab-focus'
import { ancestorsOf, buildSessionForest, orderedChildren, pinnedFirst, topLevelRoot } from './lib/session-tree'

type Tab = 'conversation' | 'trajectory' | 'config'
type SettingsPage = 'providers' | 'skills' | 'tools' | 'extensions' | 'prompt' | 'message' | 'notifications' | 'appearance'
const SETTINGS_PAGES: readonly SettingsPage[] = ['providers', 'skills', 'tools', 'extensions', 'prompt', 'message', 'notifications', 'appearance']
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

// SessionRows renders a workspace's session list as a parent/child tree.
// Subagent sessions (children) nest under their parent and are collapsed by
// default; the arrow toggles the branch while the row itself opens the session.
type SessionRowsProps = {
  rows: SessionInfo[]
  depth: number
  currentId: string | null
  untitled: string
  childrenOf: (id: string) => SessionInfo[]
  isExpanded: (id: string) => boolean
  onToggleExpand: (id: string) => void
  onOpen: (id: string) => void
  onMenu: (event: ReactMouseEvent<HTMLButtonElement>, id: string) => void
  menu: { kind: 'ws' | 'sess'; id: string } | null
  menuID: string
  draggable?: boolean
  onRowDrop?: (event: ReactDragEvent<HTMLDivElement>, index: number, session: SessionInfo) => void
  onRowDragStart?: (event: ReactDragEvent<HTMLDivElement>, session: SessionInfo) => void
}

function SessionRows({
  rows,
  depth,
  currentId,
  untitled,
  childrenOf,
  isExpanded,
  onToggleExpand,
  onOpen,
  onMenu,
  menu,
  menuID,
  draggable,
  onRowDrop,
  onRowDragStart,
}: SessionRowsProps) {
  const { t } = useI18n()
  return (
    <>
      {rows.map((session, index) => {
        const children = childrenOf(session.id)
        const open = isExpanded(session.id)
        const canDrag = !!draggable && depth === 0
        return (
          <Fragment key={session.id}>
            <div
              className={`session-row${session.id === currentId ? ' active' : ''}${depth > 0 ? ' session-child' : ''}`}
              data-testid="session-row"
              data-depth={depth}
              style={{ '--session-depth': depth } as CSSProperties}
              draggable={canDrag}
              onDragStart={e => { if (canDrag) onRowDragStart?.(e, session) }}
              onDragOver={e => { if (canDrag) e.preventDefault() }}
              onDrop={e => { if (canDrag) onRowDrop?.(e, index, session) }}
            >
              {children.length ? (
                <button
                  type="button"
                  className="session-toggle"
                  data-testid="session-toggle"
                  aria-label={t('session.expand')}
                  aria-expanded={open}
                  onClick={() => onToggleExpand(session.id)}
                >
                  <IChev open={open} />
                </button>
              ) : (
                <span className="session-toggle-spacer" aria-hidden />
              )}
              <button
                type="button"
                className="session-main"
                aria-current={session.id === currentId ? 'page' : undefined}
                onClick={() => onOpen(session.id)}
              >
                <span className={`dot${session.running ? ' on' : ''}`} />
                {session.pinned && session.forkMode !== 'tree' ? <span className="pin-mark" aria-label={t('session.pinned')}><IPin /></span> : null}
                {depth > 0 ? <span className="subagent-mark" aria-hidden title={t('session.subagent')}><IFork /></span> : null}
                <span className="meta">
                  <div className="title" data-testid="session-title">{session.title || untitled}</div>
                  <div className="sub">{session.model}</div>
                </span>
              </button>
              <button
                type="button"
                className="icon-btn tiny"
                aria-label={t('session.menu')}
                aria-haspopup="menu"
                aria-expanded={menu?.kind === 'sess' && menu.id === session.id}
                aria-controls={menu?.kind === 'sess' && menu.id === session.id ? menuID : undefined}
                onClick={e => onMenu(e, session.id)}
              ><IDots /></button>
            </div>
            {children.length && open ? (
              <SessionRows
                rows={children}
                depth={depth + 1}
                currentId={currentId}
                untitled={untitled}
                childrenOf={childrenOf}
                isExpanded={isExpanded}
                onToggleExpand={onToggleExpand}
                onOpen={onOpen}
                onMenu={onMenu}
                menu={menu}
                menuID={menuID}
              />
            ) : null}
          </Fragment>
        )
      })}
    </>
  )
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
  // Why: sidebar branch expansion is navigation state, not a durable
  // preference. Subagent branches start collapsed; opening a session expands
  // its ancestor chain for this App lifetime, and a reload starts fresh.
  const [expandedSessions, setExpandedSessions] = useState<Record<string, boolean>>({})
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
  const [notifyEnabled, setNotifyEnabled] = useState<boolean>(loadNotifyPref)
  const [notifyPerm, setNotifyPerm] = useState<NotifyPermission>(currentPermission)
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
  const [jumpToId, setJumpToId] = useState<string | null>(null)
  const [activeRequestId, setActiveRequestId] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  // The push handler is event-driven and long-lived, so it reads the open
  // session and the run stream it holds through refs instead of captured state.
  const currentIdRef = useRef<string | null>(currentId)
  const viewRef = useRef(view)
  const listeningIdRef = useRef<string | null>(null)
  const abortedRuns = useRef(new Set<string>())
  // Sessions this tab has seen running. Completion notifications are limited to
  // these so a run this browser never observed (CLI, agent child) stays silent.
  const runningKnown = useRef(new Set<string>())
  const searchAc = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const searchRootRef = useRef<HTMLDivElement>(null)
  const sidebarRef = useRef<HTMLElement>(null)
  const mainRef = useRef<HTMLElement>(null)
  const mobileNavToggleRef = useRef<HTMLButtonElement>(null)
  const mobileSidebarWasOpen = useRef(false)
  const drawerDialogSource = useRef<'settings' | 'dir' | null>(null)

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

  // Run-completion notifications are decoupled from the selected session's view
  // stream (which is torn down on switch): the single push stream delivers
  // every run's agent_end, and the tab marker lets another tab's focus suppress
  // the ping for the session it is showing. The handler is wired up below,
  // after refreshList() is defined.

  const toggleNotify = useCallback(async (on: boolean) => {
    if (!on) {
      setNotifyEnabled(false)
      saveNotifyPref(false)
      return
    }
    const perm = currentPermission()
    setNotifyPerm(perm)
    if (perm === 'insecure' || perm === 'unsupported') {
      toast.error(t(perm === 'insecure' ? 'settings.notifyInsecure' : 'settings.notifyUnsupported'))
      return
    }
    // Permission must be requested from a user gesture; the toggle click is it.
    const granted = perm === 'granted' ? perm : await requestPermission()
    setNotifyPerm(granted)
    if (granted === 'granted') {
      setNotifyEnabled(true)
      saveNotifyPref(true)
    } else {
      setNotifyEnabled(false)
      saveNotifyPref(false)
      toast.error(t('settings.notifyDenied'))
    }
  }, [t])

  const sendTestNotification = useCallback(() => {
    const perm = currentPermission()
    setNotifyPerm(perm)
    if (perm === 'insecure' || perm === 'unsupported') {
      toast.error(t(perm === 'insecure' ? 'settings.notifyInsecure' : 'settings.notifyUnsupported'))
      return
    }
    if (perm !== 'granted') { toast.error(t('settings.notifyDenied')); return }
    showNotification(t('settings.notifyTestTitle'), t('settings.notifyTestBody'))
  }, [t])

  // Permission can change in browser settings while the page is open; refresh
  // the displayed state whenever the settings modal opens.
  useEffect(() => {
    if (settingsOpen) setNotifyPerm(currentPermission())
  }, [settingsOpen])

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
	const refreshExtensions = useCallback(async (silent = false) => {
		try {
			setGlobalExtensions(await api.extensions())
		} catch (e) {
			// A push-driven refresh is a background sync; toasting every transient
			// failure would spam while an extension restarts.
			if (!silent) toast.from(e)
		}
	}, [api])

  // Sidebar refreshes are coalesced: runs ending together, or rapid pin/move/
  // delete actions, collapse into one in-flight read plus at most one trailing
  // read. Every caller's promise resolves after a refresh that started at or
  // after its call, so `await refreshList()` still guarantees fresh state.
  // The server's ETag lets an unchanged list skip setSessions entirely.
  const listEtag = useRef<string | null>(null)
  const wsKey = useRef<string | null>(null)
  const refreshGate = useRef({ active: false, dirty: false, waiters: [] as Array<() => void> })
  const sessionsRef = useRef(sessions)
  useEffect(() => { sessionsRef.current = sessions }, [sessions])

  const refreshList = useCallback((): Promise<void> => {
    const gate = refreshGate.current
    gate.dirty = true
    return new Promise<void>(resolve => {
      gate.waiters.push(resolve)
      if (gate.active) return
      gate.active = true
      void (async () => {
        try {
          while (gate.dirty) {
            gate.dirty = false
            const [ss, ws] = await Promise.all([api.list(listEtag.current ?? undefined), api.workspaces()])
            if (!ss.notModified) {
              listEtag.current = ss.etag
              setSessions(ss.sessions)
              for (const session of ss.sessions) {
                if (session.running) runningKnown.current.add(session.id)
              }
            }
            const nextWs = JSON.stringify(ws)
            if (nextWs !== wsKey.current) {
              wsKey.current = nextWs
              setWorkspaces(ws)
            }
          }
        } catch (e) {
          toast.from(e)
        } finally {
          gate.active = false
          const waiters = gate.waiters
          gate.waiters = []
          for (const w of waiters) w()
        }
      })()
    })
  }, [api])

	useEffect(() => { void refreshList() }, [refreshList])

  useEffect(() => { currentIdRef.current = currentId }, [currentId])
  useEffect(() => { viewRef.current = view }, [view])

  useTabFocus(currentId)
  const handleRunComplete = useCallback((id: string) => {
    const session = sessionsRef.current.find(s => s.id === id)
    notifyCompletion({
      enabled: notifyEnabled,
      sessionId: id,
      focusedSession: focusedSession(),
      // Subagent sessions nest under a parent whose run owns the notification;
      // announcing each child would be noise the user cannot act on.
      subagent: session?.forkMode === 'tree',
      title: session?.title || t('session.untitled'),
      // The cwd disambiguates sessions that share a title (e.g. short prompts).
      body: session?.cwd ? t('notify.doneBodyDir', { cwd: session.cwd }) : t('notify.doneBody'),
    })
    void refreshList()
  }, [notifyEnabled, refreshList, t])

	useEffect(() => { void refreshExtensions() }, [refreshExtensions])
	useEffect(() => {
		if (currentId) return
		void api.commands(selectedWs).then(commands => {
			setView(v => v.commands === commands ? v : { ...v, commands })
		}).catch(() => {})
	}, [api, currentId, selectedWs])
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
    listeningIdRef.current = id
    runningKnown.current.add(id)
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
        // A finished run is reconciled from the tail: the events already
        // arrived on this stream, so only the window and leaf need refreshing,
        // not the whole history (and the index stays warm across the run).
        const detail = await api.get(id).catch(() => null)
        if (abortRef.current === ac && detail) {
          setView(v => applyTail(v, detail))
          if (detail.running) void listen(id)
          else listeningIdRef.current = null
        } else if (abortRef.current === ac) {
          setView(v => ({ ...v, busy: false }))
          listeningIdRef.current = null
        }
        void refreshList()
      }
    }
  }, [api, refreshList])

  // The push channel's handler for the session this tab has open.
  const refreshOpenRuntime = useCallback(async () => {
    const id = currentIdRef.current
    if (!id) return
    try {
      const detail = await api.get(id, { fields: 'runtime' })
      if (currentIdRef.current !== id) return
      setView(v => ({ ...applyRuntimeCatalog(v, detail), busy: v.busy || !!detail.running }))
      if (detail.running && listeningIdRef.current !== id) void listen(id)
    } catch {
      // The session may have just been deleted; the list refresh closes it.
    }
  }, [api, listen])

  // Everything the server pushes to this tab arrives here: invalidate hints for
  // the sidebar/workspace/catalog state, and session sideband events for both
  // the open session and background completions. Why one handler: see
  // useServerEvents and docs/events.md — one stream replaces the previous
  // one-notification-stream-per-running-session.
  const onServerEvent = useCallback((ev: PushEvent) => {
    if (ev.type === 'ready') {
      // A fresh subscription replays nothing, so refetching everything is what
      // makes a reconnect catch up on whatever it missed.
      void refreshList()
      void refreshExtensions(true)
      void refreshOpenRuntime()
      return
    }
    if (ev.type === 'invalidate') {
      switch (ev.scope) {
        case 'sessions':
        case 'workspaces':
          // A run that started elsewhere, a workspace deleted elsewhere, or a
          // session created/removed: refetch the list, then reconcile the open
          // session against it.
          void refreshList().then(() => {
            const id = currentIdRef.current
            if (!id) return
            const info = sessionsRef.current.find(s => s.id === id)
            if (!info) {
              // The open session was deleted by another client (e.g. its
              // workspace was deleted): leave the transcript instead of showing
              // a session that no longer exists.
              abortRef.current?.abort()
              abortRef.current = null
              listeningIdRef.current = null
              setCurrentId(null)
              setView(v => keepComposer(v))
              return
            }
            if (info.running && listeningIdRef.current !== id) void listen(id)
          })
          return
        case 'extensions':
          void refreshExtensions(true)
          void refreshOpenRuntime()
          return
        case 'providers':
          refreshModels()
          return
      }
      return
    }
    const sessionId = ev.sessionId
    if (!sessionId) return
    if (ev.type === 'run_aborted') {
      // Abort still ends with agent_end; remember it so the completion is not
      // announced as a finished run.
      abortedRuns.current.add(sessionId)
      if (sessionId === currentIdRef.current) setView(v => applyEvent(v, ev))
      return
    }
    if (ev.type === 'agent_end') {
      const wasAborted = abortedRuns.current.delete(sessionId)
      // Only announce a run this tab had seen as running. The push stream is
      // global, so an unfiltered handler would raise a desktop notification for
      // any run in the process, including `ki run` sessions and agent children
      // this browser never observed (docs/webui.md).
      const known = runningKnown.current.delete(sessionId)
      if (!wasAborted && known) handleRunComplete(sessionId)
      return
    }
    if (sessionId !== currentIdRef.current) return
    switch (ev.type) {
      case 'compaction_start':
      case 'compaction_end':
        // A manual /compact is a synchronous request, so its progress arrives
        // as a session notification rather than on a run stream.
        setView(v => applyEvent(v, ev))
        return
      case 'context_usage':
        // A compaction outside a run (manual /compact, threshold) refreshes the
        // meter through the push stream; a run's context_usage still arrives on
        // its run SSE.
        setView(v => applyEvent(v, ev))
        return
      case 'runtime_ready':
        void refreshOpenRuntime()
        return
      case 'extension_ui_updated':
      case 'queue_changed':
        void refreshOpenRuntime()
        return
      case 'extension_notice': {
        const text = ev.messageText || ev.reason || ev.server || 'extension'
        if (ev.reason === 'warn' || ev.reason === 'error') toast.error(text)
        else toast.info(text)
        return
      }
      case 'extension_error':
        toast.action('error', `${ev.server || 'extension'}: ${ev.messageText || ev.reason || t('extension.failed')}`, t('extension.reload'), async () => {
          try {
            const result = await api.reload(currentIdRef.current ?? undefined)
            toast.info(result.queued ? t('extension.reloadQueued') : t('extension.reloaded'))
          } catch (e) { toast.from(e) }
        })
        return
    }
  }, [api, handleRunComplete, listen, refreshExtensions, refreshList, refreshModels, refreshOpenRuntime, t])
  useServerEvents(api, onServerEvent)

  // Safety net for a push stream that outlived a laptop sleep or a proxy idle
  // timeout without raising a read error yet: resync when the tab is shown
  // again. The refetch goes through the ETag, so an idle tab costs a 304.
  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState !== 'visible') return
      void refreshList()
      void refreshExtensions(true)
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [refreshExtensions, refreshList])

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

  /**
   * Fetch the tree index off the critical path.
   *
   * Why: the conversation renders the newest window of the transcript, which is
   * what first paint needs; the index (branch rows, trajectory table, absolute
   * turn numbers) is the part that made opening a long session read the whole
   * file. It arrives moments later and only renumbers, never unmounts the chat.
   * Short sessions answer with the index inline, so this is a no-op for them.
   */
  const indexLoading = useRef<string | null>(null)
  const requestIndex = useCallback(async (id: string) => {
    if (indexLoading.current === id) return
    indexLoading.current = id
    try {
      const detail = await api.get(id, { fields: 'index' })
      setView(v => (currentIdRef.current === id ? applyIndex(v, detail) : v))
    } catch {
      // The tail view already renders; the next open or tab switch retries.
    } finally {
      if (indexLoading.current === id) indexLoading.current = null
    }
  }, [api])

  useEffect(() => {
    if (!currentId || view.indexLoaded || !view.hasMore) return
    const id = currentId
    const timer = window.setTimeout(() => { void requestIndex(id) }, 250)
    return () => window.clearTimeout(timer)
  }, [currentId, requestIndex, view.indexLoaded, view.hasMore])

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
          const detail = await api.get(currentId, { fields: 'runtime' })
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
  const enabledExtensions = useMemo(
		() => globalExtensions.filter(item => item.enabled),
		[globalExtensions],
	)
  const globalExtensionItems = useMemo(
		() => enabledExtensions.filter(item => item.configurable || item.ui?.status?.text || item.ui?.panel),
		[enabledExtensions],
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
		const globalItem = enabledExtensions.find(item => item.name === extOpen)
		return globalItem ? globalExtensionUI(globalItem) : null
	}, [enabledExtensions, extOpen, sessionExtChips])

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
    if (extChips.some(ui => ui.extension === extOpen) || enabledExtensions.some(item => item.name === extOpen)) return
    if (!extChips.length && !enabledExtensions.length) {
      setExtOpen(null)
      return
    }
    selectExtension(extChips[0]?.extension ?? enabledExtensions[0].name)
  }, [enabledExtensions, extChips, extOpen, selectExtension])

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

  const sendContent = useCallback(async (content: Content[], parentId?: string, editedMessageId?: string, delivery?: 'steer' | 'queue', restore?: () => void) => {
	if (!content.some(c => (c.type === 'text' && !!c.text?.trim()) || c.type !== 'text')) return
    // Why: callers clear the composer before awaiting this. Slash commands such
    // as /compact stay inside the HTTP request until the work finishes, so
    // clearing on response would leave the typed command visible for seconds.
    // Anything accepted stays cleared; a pre-acceptance failure restores it.
    let consumed = false
    const fail = () => { if (!consumed) restore?.() }
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
        consumed = true
        if (result?.handled) {
          if (result.notice) (result.error ? toast.error : toast.info)(result.notice)
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
          fail()
          return
        }
        throw e
      }
	  if (result?.accepted === 'queued') {
		try {
		  const detail = await api.get(id, { fields: 'runtime' })
		  setView(v => ({ ...applyRuntimeCatalog(v, detail), busy: true }))
		} catch (e) { toast.from(e) }
		return
	  }
	  if (result?.accepted === 'steered') {
		setView(v => appendOptimisticUser(v, content))
		return
	  }
	  if (editedMessageId) {
		setView(v => {
		  const cut = v.nodes.findIndex(n => n.id === editedMessageId)
		  // Hide the abandoned descendant path immediately; the authoritative
		  // tree is reloaded after SSE completes.
		  return appendOptimisticUser({ ...v, nodes: cut >= 0 ? v.nodes.slice(0, cut) : v.nodes }, content)
		})
	  } else {
		setView(v => appendOptimisticUser(v, content))
	  }
      void listen(id)
    } catch (e) {
      fail()
      toast.from(e)
    }
	}, [api, currentId, listen, makeSession, openSession, selectedWs, view.model, view.provider])

	const send = useCallback((delivery?: 'steer' | 'queue') => {
	  const content: Content[] = [...(draft.text.trim() ? [{ type: 'text', text: draft.text } as Content] : []), ...draft.attachments]
	  const snapshot = draft
	  // Why: clear the input the moment the user submits so the command never
	  // looks unsent while a slow server-side slash command runs. A failed send
	  // restores the snapshot, but only when the user has not typed something
	  // new in the meantime.
	  setDraft({ text: '', attachments: [] })
	  return sendContent(content, undefined, undefined, delivery, () => setDraft(d => (d.text || d.attachments.length) ? d : snapshot))
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
	    const detail = await api.get(currentId, { fields: 'runtime' })
	    setView(v => ({ ...v, queued: detail.queued ?? [], busy: v.busy || !!detail.running }))
	  } catch (e) { toast.from(e) }
	}, [api, currentId, view.model, view.provider, view.queued])

	const sendEdit = useCallback(() => {
	  if (!edit) return Promise.resolve()
	  const content: Content[] = [...(edit.draft.text.trim() ? [{ type: 'text', text: edit.draft.text } as Content] : []), ...edit.draft.attachments]
	  const snapshot = edit
	  // Same optimistic close as the composer: submit closes the editor, and a
	  // rejected send only reopens it if the user did not start another edit.
	  setEdit(null)
	  return sendContent(content, snapshot.parentId, snapshot.messageId, undefined, () => setEdit(e => e ?? snapshot))
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

  const forest = useMemo(() => buildSessionForest(sessions, workspaces), [sessions, workspaces])

  const trees = useMemo(() => {
    // Top-level rows are sessions without a trusted parent edge. Subagent
    // children are rendered recursively by SessionRows, not listed here.
    const rootsOf = (rows: SessionInfo[]) => rows.filter(session => !forest.parentOf.has(session.id))
    const used = new Set<string>()
    const groups = workspaces.map(ws => {
      const order = ws.sessionIds?.length
        ? [...ws.sessionIds, ...sessions.filter(s => s.workspaceId === ws.id && !ws.sessionIds!.includes(s.id)).map(s => s.id)]
        : sessions.filter(s => s.workspaceId === ws.id).map(s => s.id)
      // Pin first: the account order is stored order, which starts with the
      // newest session, so a freshly created one would otherwise sit above a pin.
      const allRows = pinnedFirst(order.map(id => byId.get(id)).filter((s): s is SessionInfo => !!s))
      allRows.forEach(s => used.add(s.id))
      return { ws, rows: rootsOf(allRows) }
    })
    const ungrouped = rootsOf(pinnedFirst(sessions.filter(s => !used.has(s.id))))
    return { groups, ungrouped }
  }, [byId, forest, sessions, workspaces])

  const childrenOf = useCallback((id: string) => orderedChildren(forest, id), [forest])
  const isSessionExpanded = useCallback((id: string) => expandedSessions[id] === true, [expandedSessions])
  const toggleSession = useCallback((id: string) => {
    setExpandedSessions(current => ({ ...current, [id]: !current[id] }))
  }, [])

  const openSessionRow = useCallback((id: string) => {
    // A subagent result is a transcript, so surface it on the conversation tab
    // rather than whatever tab the parent was left on.
    if (byId.get(id)?.forkMode === 'tree') setTab('conversation')
    void openSession(id)
  }, [byId, openSession])

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

  const pendingHydrate = useRef(new Set<string>())
  const hydrateTimer = useRef(0)
  const olderLock = useRef(false)
  const requestHydrate = useCallback((id: string) => {
    if (!currentId || !id) return
    pendingHydrate.current.add(id)
    if (hydrateTimer.current) return
    hydrateTimer.current = window.setTimeout(() => {
      hydrateTimer.current = 0
      const ids = [...pendingHydrate.current]
      pendingHydrate.current.clear()
      void api.getEntries(currentId, ids).then(out => {
        setView(v => hydrateEntries(v, out.entries ?? []))
      }).catch(e => toast.from(e))
    }, 32)
  }, [api, currentId])

  const loadOlder = useCallback(async () => {
    if (!currentId || !view.hasMore || !view.oldestId || olderLock.current) return
    olderLock.current = true
    const el = scrollRef.current
    const prev = el?.scrollHeight ?? 0
    const prevTop = el?.scrollTop ?? 0
    try {
      const extra = await api.get(currentId, { before: view.oldestId })
      setView(v => hydrateEntries(v, extra.entries ?? [], { hasMore: extra.hasMore, oldestId: extra.oldestId }))
      requestAnimationFrame(() => {
        if (!el) return
        el.scrollTop = prevTop + (el.scrollHeight - prev)
      })
    } catch (e) {
      toast.from(e)
    } finally {
      olderLock.current = false
    }
  }, [api, currentId, view.hasMore, view.oldestId])

  useEffect(() => {
    const el = scrollRef.current
    if (!el || !atBottom) return
    el.scrollTop = el.scrollHeight
  }, [view.nodes, atBottom])

  // Jumping to an older prompt pages history in; 500 is the server's per-page
  // cap, so a jump to the start of a long session takes a handful of requests.
  const jumpPageSize = 500

  const inspect = (n: ChatNode) => {
    setInspId(n.id)
    setTab('trajectory')
    // The trajectory table is built from the tree index; fetch it now instead
    // of waiting for the background pass.
    if (currentId && !view.indexLoaded) void requestIndex(currentId)
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
	  // Fork copies settled history up to this message and runs in a new session,
	  // so it never touches the current (possibly running) turn.
	  if (!currentId) return
	  try {
		const child = await api.fork(currentId, node.id)
		await refreshList()
		await openSession(child.id)
	  } catch (e) { toast.from(e) }
	}, [api, currentId, openSession, refreshList])

	const regenerate = useCallback((node: Extract<ChatNode, { kind: 'assistant' }>) => {
	  // Regenerate edits the user turn in place (a new branch), which would race
	  // the running loop; refuse and tell the user instead of silently no-oping.
	  if (view.busy) { toast.info(t('chat.regenBusy')); return }
	  const idx = view.nodes.findIndex(n => n.id === node.id)
	  for (let i = idx - 1; i >= 0; i--) {
		const candidate = view.nodes[i]
		if (candidate.kind !== 'user') continue
		void sendContent(candidate.content, candidate.parentId ?? '', candidate.id)
		return
	  }
	}, [sendContent, t, view.busy, view.nodes])

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
  const stats = useMemo(() => latestStats(view), [view])
  // The navigator walks the branch from the loaded entries and index rows, not
  // from chat nodes, so every prompt is listed without paging the chat back.
  const requestItems = useMemo(() => userRequests(view.allEntries, view.leafId, view.nodes), [view.allEntries, view.leafId, view.nodes])

  useEffect(() => {
    setJumpToId(null)
    setActiveRequestId(null)
  }, [currentId])

  /**
   * Jump to a prompt from the navigator.
   *
   * The list covers the whole branch, but chat nodes only cover the loaded
   * window, so a jump to older history pages it in first — the same pages
   * scrolling up would fetch, in larger steps. history runs out at hasMore.
   */
  const jumpToRequest = useCallback(async (id: string) => {
    setAtBottom(false)
    setActiveRequestId(id)
    const sessionId = currentIdRef.current
    if (!sessionId) return
    // A live node (a prompt sent in this tab, not persisted yet) is already on
    // screen: paging history for it would never find it.
    if (!viewRef.current.allEntries.some(e => e.id === id)) {
      setJumpToId(id)
      return
    }
    let have = new Set(viewRef.current.entries.map(e => e.id))
    while (!have.has(id)) {
      const current = viewRef.current
      if (!current.hasMore || !current.oldestId || currentIdRef.current !== sessionId) return
      let page
      try {
        page = await api.get(sessionId, { before: current.oldestId, limit: jumpPageSize })
      } catch (e) {
        toast.from(e)
        return
      }
      if (currentIdRef.current !== sessionId) return
      const entries = page.entries ?? []
      if (!entries.length) return
      setView(v => hydrateEntries(v, entries, { hasMore: page.hasMore, oldestId: page.oldestId }))
      have = new Set([...have, ...entries.map(e => e.id)])
    }
    setJumpToId(id)
  }, [api])
  const clearJump = useCallback(() => setJumpToId(null), [])
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
                void api.patch(currentId, { queued: ids }).then(() => api.get(currentId, { fields: 'runtime' })).then(detail => {
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
    // Opening a subagent child must keep its top-level branch in the slice,
    // otherwise the expanded row would be cut off by the show-more limit.
    const anchorId = currentId ? topLevelRoot(forest, currentId) : null
    const idx = anchorId ? rows.findIndex(s => s.id === anchorId) : -1
    const n = idx >= SHOW ? idx + 1 : SHOW
    return rows.slice(0, n)
  }

  // Opening a session (including a deep subagent child restored from the URL)
  // expands its ancestor chain so the active row is reachable in the tree.
  useEffect(() => {
    if (!currentId) return
    const chain = ancestorsOf(forest, currentId)
    if (!chain.length) return
    setExpandedSessions(current => {
      if (chain.every(id => current[id] === true)) return current
      const next = { ...current }
      for (const id of chain) next[id] = true
      return next
    })
  }, [currentId, forest])

  return (
    <div className={`app${collapsed && !compactLayout ? ' sidebar-collapsed' : ''}${mobileSidebarOpen ? ' mobile-sidebar-open' : ''}${(settingsOpen || modelOpen || dirOpen || attachmentTarget || extOpen) ? ' modal-open' : ''}`}>
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
                <SessionRows
                  rows={shown}
                  depth={0}
                  currentId={currentId}
                  untitled={untitled}
                  childrenOf={childrenOf}
                  isExpanded={isSessionExpanded}
                  onToggleExpand={toggleSession}
                  onOpen={openSessionRow}
                  onMenu={(e, id) => openMenu('sess', id, e.currentTarget)}
                  menu={menu}
                  menuID={menuID}
                  draggable
                  onRowDragStart={(e, s) => e.dataTransfer.setData('text/sess', `${ws.id}:${s.id}`)}
                  onRowDrop={(e, i, s) => {
                    const raw = e.dataTransfer.getData('text/sess')
                    const [fromWs, sid] = raw.split(':')
                    if (fromWs !== ws.id || !sid || sid === s.id) return
                    const rect = e.currentTarget.getBoundingClientRect()
                    const before = e.clientY < rect.top + rect.height / 2 ? s.id : shown[i + 1]?.id
                    void api.moveSession(ws.id, sid, before ?? null).then(() => refreshList()).catch(er => toast.from(er))
                  }}
                />
                {open && rows.length > shown.length ? (
                  <button type="button" className="show-more" data-testid="show-more" onClick={() => setShowAll(a => ({ ...a, [ws.id]: true }))}>{t('session.showMore')}</button>
                ) : null}
              </div>
            )
          })}
          {sidebarWide && !filter.trim() && trees.ungrouped.length ? (
            <div>
              <div className="cwd-label">{t('session.ungrouped')}</div>
              <SessionRows
                rows={trees.ungrouped}
                depth={0}
                currentId={currentId}
                untitled={untitled}
                childrenOf={childrenOf}
                isExpanded={isSessionExpanded}
                onToggleExpand={toggleSession}
                onOpen={openSessionRow}
                onMenu={(e, id) => openMenu('sess', id, e.currentTarget)}
                menu={menu}
                menuID={menuID}
              />
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
            <button type="button" className={`tab${tab === 'trajectory' ? ' active' : ''}`} data-testid="tab-trajectory" onClick={() => { setTab('trajectory'); if (currentId && !view.indexLoaded) void requestIndex(currentId) }}>{t('tab.trajectory')}</button>
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
              onOpenSession={id => { setTab('conversation'); void openSession(id) }}
              runtimeReady={view.runtimeReady}
            />
          </div>
        ) : tab === 'trajectory' ? (
          <div className="conv-body">
            <TrajectoryView records={view.records} requests={view.requests} selectId={inspId} onHydrate={requestHydrate} />
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
                <div className="chat-stage">
                <div
                  className="scroll"
                  data-testid="chat-scroll"
                  ref={scrollRef}
                  onScroll={e => {
                    const el = e.currentTarget
                    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80)
                    if (el.scrollTop < 96) void loadOlder()
                  }}
                >
				  <ChatView
					api={api}
					nodes={view.nodes}
					busy={view.busy}
					scrollRef={scrollRef}
					onHydrate={requestHydrate}
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
					jumpToId={jumpToId}
					onJumped={clearJump}
					onActiveRequest={setActiveRequestId}
					turnBase={view.turnBase}
				  />
                </div>
                <RequestNav
                  key={currentId ?? 'none'}
                  items={requestItems}
                  activeId={activeRequestId}
                  onJump={jumpToRequest}
                  onOpen={() => { if (currentId && !view.indexLoaded) void requestIndex(currentId) }}
                />
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
                </div>
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
            globalItems={enabledExtensions}
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
              ['tools', t('settings.tools')],
              ['extensions', t('settings.extensions')],
              ['prompt', t('settings.prompt')],
              ['message', t('settings.message')],
              ['notifications', t('settings.notifications')],
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
                ) : page === 'tools' ? (
                  <SettingsToggles kind="tools" api={api} sessionId={currentId} workspaceId={selectedWs} />
                ) : page === 'extensions' ? (
                  <SettingsToggles kind="extensions" api={api} onConfigure={openExtensionConfig} onChanged={refreshExtensions} />
                ) : page === 'prompt' ? (
                  <PromptSettings api={api} workspaceId={selectedWs} />
                ) : page === 'message' ? (
                  <MessageSettings api={api} />
                ) : page === 'notifications' ? (
                  <NotificationSettings
                    enabled={notifyEnabled}
                    permission={notifyPerm}
                    onToggle={on => void toggleNotify(on)}
                    onTest={sendTestNotification}
                  />
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
