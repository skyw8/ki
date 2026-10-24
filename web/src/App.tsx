import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type DragEvent as ReactDragEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { ApiError, Client } from './api/client'
import { AuthLoading, LoginScreen } from './features/settings/AuthScreen'
import { ChatView } from './features/chat/Chat'
import { conversationTarget, conversationTargetTurn } from './features/chat/conversationTarget'
import { ImagePreviewProvider } from './features/attachments/AttachmentImage'
import { useTranscriptRequests } from './features/chat/useTranscriptRequests'
import type { TranscriptScroll } from './features/chat/useTranscriptScroll'
import { PerfHud } from './features/chat/PerfHud'
import { RequestNav } from './features/chat/RequestNav'
import { Composer, type Draft } from './features/chat/Composer'
import { AttachmentBrowser } from './features/attachments/AttachmentBrowser'
import { OutputPreview } from './features/chat/tools/OutputPreview'
import { DirectoryBrowser } from './features/sessions/DirectoryBrowser'
import { ExtensionConfigEditor, MessageSettings, NotificationSettings, SessionConfig, SettingsToggles } from './features/settings/SessionConfig'
import { PromptSettings } from './features/settings/PromptSettings'
import { AboutSettings } from './features/settings/AboutSettings'
import { ModelPickerDialog } from './features/settings/ModelPickerDialog'
import { ProviderSettings } from './features/settings/ProviderSettings'
import { IChev, IChevDown, IClose, IDots, IEdit, IFile, IFolder, IFork, IGear, IImage, IPanel, IPin, IPlus, ISearch, ITrash } from './components/icons'
import { appendOptimisticUser, applyEvent, applyRuntimeCatalog, clampThinkingEffort, emptyView, keepComposer, loadHistory, reconcileComposerModel, sessionCreateBody, sessionStats, userRequests } from './lib/model'
import { clampCompactKeep, loadMessageView, saveMessageView, type MessageView } from './lib/messageView'
import type { CatalogExtension, ChatNode, Content, ExtensionUI, Meta, ModelInfo, PushEvent, SearchHit, SessionInfo, WorkspaceInfo } from './api/types'
import { ContextView } from './features/context/ContextView'
import { MarkdownLinkProvider } from './features/markdown/links'
import { useI18n } from './i18n/index'
import { toast } from './components/toast'
import { ExtensionInspector, localizedExtensionText, seedExtFields, statusChips, visibleStatusChips } from './features/settings/ExtensionPanel'
import { useDialogFocus } from './hooks/useDialogFocus'
import { useKeyboardInset } from './hooks/useKeyboardInset'
import { useTabFocus } from './hooks/useTabFocus'
import { useServerEvents } from './hooks/useServerEvents'
import { currentPermission, loadNotifyPref, notifyCompletion, requestPermission, saveNotifyPref, showNotification, type NotifyPermission } from './lib/notifications'
import { dropPushSubscription, ensurePushSubscription } from './lib/push'
import { focusedSession } from './lib/tab-focus'
import { reconcileFinishedRuns } from './lib/completion-catchup'
import { isNetworkError } from './lib/errors'
import { useTranscriptStore } from './hooks/useTranscriptStore'
import { SessionSyncController } from './lib/session-sync'
import { clientRequestId } from './lib/client-request'
import { ancestorsOf, buildSessionForest, orderedChildren, pinnedFirst, topLevelRoot } from './lib/session-tree'
import type { LauncherAuth } from './lib/launcher-auth'

type Tab = 'conversation' | 'context' | 'config'
type SettingsPage = 'providers' | 'skills' | 'tools' | 'mcp' | 'extensions' | 'prompt' | 'message' | 'notifications' | 'appearance' | 'about'
const SETTINGS_PAGES: readonly SettingsPage[] = ['providers', 'skills', 'tools', 'mcp', 'extensions', 'prompt', 'message', 'notifications', 'appearance', 'about']
const SHOW = 5
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
                <span className={`dot${session.running ? ' on' : (session.activeDescendantCount ?? 0) > 0 ? ' delegated' : ''}`} aria-hidden />
                {session.pinned && session.forkMode !== 'tree' ? <span className="pin-mark" aria-label={t('session.pinned')}><IPin /></span> : null}
                {depth > 0 ? <span className="subagent-mark" aria-hidden title={t('session.subagent')}><IFork /></span> : null}
                <span className="meta">
                  <div className="title" data-testid="session-title">{session.title || untitled}</div>
                  <div className="sub" data-testid={(session.activeDescendantCount ?? 0) > 0 ? 'session-child-activity' : undefined}
                    title={(session.activeDescendantCount ?? 0) > 0 ? t(session.running ? 'session.runningChildren' : 'session.waitingChildren', { n: session.activeDescendantCount! }) : undefined}>
                    {(session.activeDescendantCount ?? 0) > 0
                      ? t(session.running ? 'session.runningChildren' : 'session.waitingChildren', { n: session.activeDescendantCount! })
                      : session.model}
                  </div>
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

export function App({ launcherAuth }: { launcherAuth: LauncherAuth }) {
  const [generation, setGeneration] = useState(0)
  const api = useMemo(() => new Client(), [generation])
  const [auth, setAuth] = useState<'checking' | 'required' | 'authenticated'>('checking')
  useKeyboardInset()

  const onServerChange = useCallback(() => {
    setAuth('checking')
    setGeneration(n => n + 1)
  }, [])
  useEffect(() => {
    let active = true
    void launcherAuth.check(api).then(result => {
      if (!active) return
      if (result === 'serverChanged') onServerChange()
      else setAuth(result)
    })
    return () => { active = false }
  }, [api, launcherAuth, onServerChange])

  if (auth === 'checking') return <AuthLoading />
  if (auth === 'required') return <LoginScreen api={api} onLogin={() => setAuth('authenticated')} onServerChange={onServerChange} />
  return <ImagePreviewProvider key={generation}><WorkspaceApp api={api} onServerChange={onServerChange} /></ImagePreviewProvider>
}

function WorkspaceApp({ api, onServerChange }: { api: Client; onServerChange: () => void }) {
  const { t, lang, setLang } = useI18n()
  const untitled = t('session.untitled')
  const compactLayout = useCompactLayout()
  const [dark, setDark] = useState(() => {
    try { return localStorage.getItem('ki-theme') === 'dark' }
    catch { return false }
  })
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
  const [expandedSessions, setExpandedSessions] = useState<Record<string, boolean>>({})
  const [selectedWs, setSelectedWs] = useState<string | null>(null)
  const selectedWsRef = useRef(selectedWs)
  selectedWsRef.current = selectedWs
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [showAll, setShowAll] = useState<Record<string, boolean>>({})
  const { store, view, setView } = useTranscriptStore()
  const [draft, setDraft] = useState<Draft>({ text: '', attachments: [] })
	const [edit, setEdit] = useState<{ messageId: string; parentId: string; draft: Draft } | null>(null)
	const [attachmentTarget, setAttachmentTarget] = useState<'new' | 'edit' | null>(null)
	const [previewPath, setPreviewPath] = useState<string | null>(null)
	const openMarkdownFile = useCallback((target: { path: string }) => setPreviewPath(target.path), [])
	const loadMarkdownFile = useCallback((path: string) => api.previewFS(path), [api])
	const [uploading, setUploading] = useState(false)
  const [fileDragActive, setFileDragActive] = useState(false)
  const [models, setModels] = useState<ModelInfo[]>([])
	const [defaultModel, setDefaultModel] = useState('')
	const [serverMeta, setServerMeta] = useState<Meta | null>(null)
	const [globalExtensions, setGlobalExtensions] = useState<CatalogExtension[]>([])
	const [extensionCatalogRev, setExtensionCatalogRev] = useState(0)
  const [settingsOpen, setSettingsOpen] = useState(false)
	const [settingsPage, setSettingsPage] = useState<SettingsPage>('providers')
  const [messageView, setMessageView] = useState<MessageView>(loadMessageView)
  const messageViewRef = useRef(messageView)
  messageViewRef.current = messageView
  const transcriptOptions = () => messageViewRef.current.mode === 'compact' ? { view: 'compact' as const, keep: messageViewRef.current.keep } : {}
  const [notifyEnabled, setNotifyEnabled] = useState<boolean>(loadNotifyPref)
  const [notifyPerm, setNotifyPerm] = useState<NotifyPermission>(currentPermission)
  const [pushReady, setPushReady] = useState(false)
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
  const [contextRequestId, setContextRequestId] = useState<string | null>(null)
  const [contextFocusId, setContextFocusId] = useState<string | null>(null)
  const [atBottom, setAtBottom] = useState(true)
  const [jumpToId, setJumpToId] = useState<string | null>(null)
  const [activeRequestId, setActiveRequestId] = useState<string | null>(null)
  const currentIdRef = useRef<string | null>(currentId)
  const viewRef = store
  const syncRef = useRef<SessionSyncController | null>(null)
  const abortedRuns = useRef(new Set<string>())
  const runningKnown = useRef(new Set<string>())
  const searchAc = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const chatControl = useRef<TranscriptScroll>(null)
  const [sessionRevision, setSessionRevision] = useState(0)
  const openAbort = useRef<AbortController | null>(null)
  const [openingId, setOpeningId] = useState<string | null>(null)
  const beforeHistoryCommit = useCallback((signal: AbortSignal) => chatControl.current?.preparePrepend(signal) ?? Promise.resolve(), [])
  const requestRecovery = useCallback(() => syncRef.current?.recover(true) ?? Promise.resolve(), [])
  const history = useTranscriptRequests(api, currentId, sessionRevision, store, messageView, beforeHistoryCommit, requestRecovery)
  const { requestIndex, refreshIndex, requestHydrate, loadOlder, loadingOlder, olderError, indexLoading, indexError } = history
  const jumpVersion = useRef(0)
  const [seekingId, setSeekingId] = useState<string | null>(null)
  const [jumpErrorId, setJumpErrorId] = useState<string | null>(null)
  const cancelJump = useCallback(() => {
    jumpVersion.current++
    setSeekingId(null)
    setJumpErrorId(null)
    setJumpToId(null)
  }, [])
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
    setSettingsPage(page)
    window.requestAnimationFrame(() => tabs[next]?.focus({ preventScroll: false }))
  }

  useEffect(() => {
    document.body.dataset.theme = dark ? 'dark' : 'light'
    document.body.toggleAttribute('data-ds-dark-theme', dark)
    try { localStorage.setItem('ki-theme', dark ? 'dark' : 'light') } catch {}
  }, [dark])

  const changeMessageView = useCallback((next: MessageView) => {
    setMessageView({ mode: next.mode, keep: clampCompactKeep(next.keep) })
    saveMessageView(next)
  }, [])

  const toggleNotify = useCallback(async (on: boolean) => {
    if (!on) {
      setNotifyEnabled(false)
      saveNotifyPref(false)
      setPushReady(false)
      void dropPushSubscription(api)
      return
    }
    const perm = currentPermission()
    setNotifyPerm(perm)
    if (perm === 'insecure' || perm === 'unsupported') {
      toast.error(t(perm === 'insecure' ? 'settings.notifyInsecure' : 'settings.notifyUnsupported'))
      return
    }
    const granted = perm === 'granted' ? perm : await requestPermission()
    setNotifyPerm(granted)
    if (granted === 'granted') {
      setNotifyEnabled(true)
      saveNotifyPref(true)
      void ensurePushSubscription(api).then(setPushReady)
    } else {
      setNotifyEnabled(false)
      saveNotifyPref(false)
      toast.error(t('settings.notifyDenied'))
    }
  }, [api, t])

  const sendTestNotification = useCallback(async () => {
    const perm = currentPermission()
    setNotifyPerm(perm)
    if (perm === 'insecure' || perm === 'unsupported') {
      toast.error(t(perm === 'insecure' ? 'settings.notifyInsecure' : 'settings.notifyUnsupported'))
      return
    }
    if (perm !== 'granted') { toast.error(t('settings.notifyDenied')); return }
    const shown = await showNotification({ title: t('settings.notifyTestTitle'), body: t('settings.notifyTestBody') })
    if (!shown) toast.error(t('settings.notifyTestFailed'))
  }, [t])

  useEffect(() => {
    if (settingsOpen) setNotifyPerm(currentPermission())
  }, [settingsOpen])

  useEffect(() => {
    if (!notifyEnabled || currentPermission() !== 'granted') return
    void ensurePushSubscription(api).then(setPushReady)
  }, [api, notifyEnabled])

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
      if (event.key === 'Escape' && !event.defaultPrevented && !menu) setMobileSidebarOpen(false)
    }
    window.addEventListener('keydown', close)
    return () => window.removeEventListener('keydown', close)
  }, [menu, mobileSidebarOpen])

  useEffect(() => {
    if (compactLayout) setMobileSidebarOpen(false)
  }, [compactLayout, currentId])

  useLayoutEffect(() => {
    const sidebar = sidebarRef.current
    const main = mainRef.current
    if (!sidebar || !main) return
    sidebar.toggleAttribute('inert', compactLayout && !mobileSidebarOpen)
    main.toggleAttribute('inert', compactLayout && mobileSidebarOpen)
    if (compactLayout && mobileSidebarOpen) {
      requestAnimationFrame(() => sidebar.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled)')?.focus({ preventScroll: true }))
    } else if (compactLayout && mobileSidebarWasOpen.current) {
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

  const modelRefresh = useRef(0)
	const refreshModels = useCallback(() => {
    const generation = ++modelRefresh.current
		void Promise.all([api.models(), api.meta()]).then(([list, meta]) => {
      if (generation !== modelRefresh.current) return
			setModels(list)
			setServerMeta(meta)
			setDefaultModel(meta.provider && meta.model ? `${meta.provider}/${meta.model}` : '')
      setView(v => reconcileComposerModel(v, !!currentIdRef.current || !!store.sessionId, list, meta))
		}).catch(() => {
      if (generation === modelRefresh.current) setModels([])
    })
	}, [api, store, setView])
  useEffect(() => { refreshModels() }, [refreshModels])
	const refreshExtensions = useCallback(async (silent = false) => {
		try {
			setGlobalExtensions(await api.extensions())
			setExtensionCatalogRev(rev => rev + 1)
		} catch (e) {
			if (!silent) toast.from(e)
		}
	}, [api])

  const [listLoaded, setListLoaded] = useState(false)
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
            const requestedId = currentIdRef.current
            const requestedWs = selectedWsRef.current
            const [ss, ws] = await Promise.all([api.list(listEtag.current ?? undefined), api.workspaces()])
            if (!ss.notModified) {
              listEtag.current = ss.etag
              setListLoaded(true)
              sessionsRef.current = ss.sessions
              setSessions(ss.sessions)
              for (const session of ss.sessions) {
                if (session.running) runningKnown.current.add(session.id)
              }
              if (requestedId && currentIdRef.current === requestedId && !ss.sessions.some(s => s.id === requestedId)) {
                openAbort.current?.abort()
                currentIdRef.current = null
                syncRef.current?.select(null)
                store.reset(null, keepComposer(store.current))
                setCurrentId(null)
                setOpeningId(null)
                setEdit(null)
              }
            }
            if (requestedWs && selectedWsRef.current === requestedWs && !ws.some(w => w.id === requestedWs)) {
              selectedWsRef.current = null
              setSelectedWs(null)
            }
            const nextWs = JSON.stringify(ws)
            if (nextWs !== wsKey.current) {
              wsKey.current = nextWs
              setWorkspaces(ws)
            }
          }
        } catch (e) {
          if (!isNetworkError(e)) toast.from(e)
        } finally {
          gate.active = false
          const waiters = gate.waiters
          gate.waiters = []
          for (const w of waiters) w()
        }
      })()
    })
  }, [api, store])

	useEffect(() => { void refreshList() }, [refreshList])

  useLayoutEffect(() => {
    currentIdRef.current = currentId
    if (!currentId && store.sessionId) {
      syncRef.current?.select(null)
      store.reset(null, keepComposer(store.current))
    }
  }, [currentId, store])

  useTabFocus(api.serverId, currentId)
  const handleRunComplete = useCallback((id: string) => {
    const session = sessionsRef.current.find(s => s.id === id)
    void notifyCompletion({
      enabled: notifyEnabled,
      sessionId: id,
      focusedSession: focusedSession(api.serverId),
      subagent: session?.forkMode === 'tree',
      title: session?.title || t('session.untitled'),
      body: session?.cwd ? t('notify.doneBodyDir', { cwd: session.cwd }) : t('notify.doneBody'),
    })
    void refreshList()
  }, [notifyEnabled, refreshList, t])

  const catchUpMissedCompletions = useCallback(() => {
    reconcileFinishedRuns({
      known: runningKnown.current,
      sessions: sessionsRef.current,
      aborted: abortedRuns.current,
      announce: handleRunComplete,
    })
  }, [handleRunComplete])

	useEffect(() => { void refreshExtensions() }, [refreshExtensions])
	useEffect(() => {
		if (currentId) return
		let active = true
		const ticket = store.capture('body')
		void api.commands(selectedWs).then(commands => {
			if (active && store.isCurrent(ticket)) setView(v => v.commands === commands ? v : { ...v, commands })
		}).catch(() => {})
		return () => { active = false }
	}, [api, currentId, selectedWs, store, setView])
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

  const syncActions = useRef({ refreshIndex, refreshList })
  syncActions.current = { refreshIndex, refreshList }
  const sync = useMemo(() => new SessionSyncController(api, store, {
    transcriptOptions: () => messageViewRef.current.mode === 'compact'
      ? { view: 'compact', keep: messageViewRef.current.keep } : {},
    onListening: id => {
      runningKnown.current.add(id)
      setSessions(ss => ss.map(s => s.id === id ? { ...s, running: true } : s))
    },
    onSnapshot: (id, detail) => syncActions.current.refreshIndex(id, detail.leafId),
    onSettled: () => { void syncActions.current.refreshList() },
    onError: error => toast.from(error),
  }), [api, store])
  syncRef.current = sync
  const syncStatus = useSyncExternalStore(sync.subscribe, sync.getSnapshot, sync.getSnapshot)
  useEffect(() => {
    sync.activate()
    return () => { openAbort.current?.abort(); sync.dispose() }
  }, [sync])
  const listen = useCallback((id: string, snapshotLeaf?: string) => sync.listen(id, snapshotLeaf), [sync])
  const recoverOpenTranscript = useCallback((restart = false) => sync.recover(restart), [sync])

  const refreshOpenRuntime = useCallback(async () => {
    const id = currentIdRef.current
    if (!id) return
    const ticket = store.capture('body')
    try {
      const detail = await api.get(id, { fields: 'runtime' })
      if (currentIdRef.current !== id || !store.isCurrent(ticket)) return
      setView(v => ({ ...applyRuntimeCatalog(v, detail), busy: v.busy || !!detail.running }))
      if (detail.running && !sync.isListening(id)) void listen(id)
    } catch {
    }
  }, [api, listen, store, sync])

  const serverChanged = useRef(false)
  const onServerEvent = useCallback((ev: PushEvent) => {
    if (serverChanged.current) return
    if (ev.type === 'ready') {
      if (ev.serverId !== api.serverId) {
        serverChanged.current = true
        onServerChange()
        return
      }
      void refreshList().then(catchUpMissedCompletions)
      void refreshExtensions(true)
      refreshModels()
      void recoverOpenTranscript()
      return
    }
    if (ev.type === 'invalidate') {
      switch (ev.scope) {
        case 'sessions':
        case 'workspaces':
          void refreshList().then(() => {
            const id = currentIdRef.current
            if (!id) return
            const info = sessionsRef.current.find(s => s.id === id)
            if (!info) {
              sync.select(null)
              store.reset(null, keepComposer(store.current))
              setCurrentId(null)
              setView(v => keepComposer(v))
              return
            }
            if (info.running && !sync.isListening(id)) void listen(id)
          })
          return
        case 'extensions':
          void refreshExtensions(true)
          refreshModels()
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
      abortedRuns.current.add(sessionId)
      if (sessionId === currentIdRef.current) setView(v => applyEvent(v, ev))
      return
    }
    if (ev.type === 'agent_end') {
      const wasAborted = abortedRuns.current.delete(sessionId)
      const known = runningKnown.current.delete(sessionId)
      if (!wasAborted && known) handleRunComplete(sessionId)
      return
    }
    if (sessionId !== currentIdRef.current) return
    switch (ev.type) {
      case 'compaction_start':
      case 'compaction_end':
        setView(v => applyEvent(v, ev))
        return
      case 'context_usage':
        setView(v => applyEvent(v, ev))
        return
      case 'process_updated':
      case 'agent_updated':
        setView(v=>applyEvent(v,ev))
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
  }, [api, onServerChange, handleRunComplete, listen, refreshExtensions, refreshList, refreshModels, refreshOpenRuntime, recoverOpenTranscript, t, catchUpMissedCompletions, store, sync])
  useServerEvents(api, onServerEvent, onServerChange)

  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState !== 'visible') return
      void refreshList().then(catchUpMissedCompletions)
      void refreshExtensions(true)
      refreshModels()
      void sync.resume()
    }
    const onPageShow = (event: PageTransitionEvent) => { if (event.persisted) onVisibility() }
    const onPageHide = () => {
      openAbort.current?.abort()
      if (currentIdRef.current && sync.getSnapshot().sessionId !== currentIdRef.current) sync.select(currentIdRef.current)
      sync.suspend()
    }
    document.addEventListener('visibilitychange', onVisibility)
    window.addEventListener('pageshow', onPageShow)
    window.addEventListener('online', onVisibility)
    window.addEventListener('pagehide', onPageHide)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('pageshow', onPageShow)
      window.removeEventListener('online', onVisibility)
      window.removeEventListener('pagehide', onPageHide)
    }
  }, [catchUpMissedCompletions, refreshExtensions, refreshList, refreshModels, sync])

  const openSession = useCallback(async (id: string): Promise<boolean> => {
    setMobileSidebarOpen(false)
    history.cancel()
    cancelJump()
    openAbort.current?.abort()
    const ac = new AbortController()
    openAbort.current = ac
    currentIdRef.current = id
    setSessionRevision(n => n + 1)
    store.reset(id, { ...keepComposer(store.current), title: sessionsRef.current.find(s => s.id === id)?.title ?? '', runtimeReady: false })
    sync.select(null)
    setOpeningId(id)
    setAtBottom(true)
    setCurrentId(id)
	setEdit(null)
    try {
      const detail = await api.get(id, { ...transcriptOptions(), signal: ac.signal })
      if (ac.signal.aborted || currentIdRef.current !== id) return false
      const next = loadHistory(detail)
      setView(next)
      sync.select(id)
      setSelectedWs(detail.workspaceId ?? null)
      if (detail.workspaceId) setExpanded(e => ({ ...e, [detail.workspaceId!]: true }))
      if (detail.running) void listen(id, detail.leafId)
      return true
    } catch (e) {
      if (!ac.signal.aborted && currentIdRef.current === id) {
        sync.select(id)
        void sync.recover()
      }
      return false
    } finally {
      if (openAbort.current === ac) setOpeningId(null)
    }
  }, [api, listen, history.cancel, cancelJump, store, sync])

  useEffect(() => {
    if (!currentId || openingId === currentId || view.runtimeReady !== false) return
    const ac = new AbortController()
    const ticket = store.capture('body')
    const valid = () => !ac.signal.aborted && store.isCurrent(ticket)
    const started = Date.now()
    void (async () => {
      while (valid() && Date.now() - started < 25_000) {
        await new Promise<void>(resolve => {
          const onAbort = () => { window.clearTimeout(timer); resolve() }
          const timer = window.setTimeout(() => { ac.signal.removeEventListener('abort', onAbort); resolve() }, 400)
          ac.signal.addEventListener('abort', onAbort, { once: true })
        })
        if (!valid()) return
        try {
          const detail = await api.get(currentId, { fields: 'runtime' })
          if (!valid()) return
          if (detail.runtime?.ready) {
            setView(v => applyRuntimeCatalog(v, detail))
            return
          }
        } catch {}
      }
      if (valid()) setView(v => ({ ...v, runtimeReady: true }))
    })()
    return () => ac.abort()
  }, [api, currentId, sessionRevision, openingId, view.runtimeReady, store, setView])

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
    history.cancel()
    openAbort.current?.abort()
    const ac = new AbortController()
    openAbort.current = ac
    currentIdRef.current = s.id
    store.reset(s.id, { ...keepComposer(store.current), cwd: s.cwd, title: s.title, runtimeReady: false })
    sync.select(null)
    setOpeningId(s.id)
    const ticket = store.capture('tail')
    setCurrentId(s.id)
    setSessionRevision(n => n + 1)
    setSelectedWs(s.workspaceId ?? workspaceId ?? null)
    try {
      const detail = await api.get(s.id, { ...transcriptOptions(), signal: ac.signal })
      if (store.isCurrent(ticket) && !ac.signal.aborted) {
        setView(loadHistory(detail))
        sync.select(s.id)
      }
    } catch {
      if (store.isCurrent(ticket) && !ac.signal.aborted) {
        setView({ ...emptyView(), cwd: s.cwd, model: s.model, provider: s.provider, thinkingEffort: s.thinkingEffort ?? '' })
        sync.select(s.id)
      }
    } finally {
      if (openAbort.current === ac) setOpeningId(null)
    }
    setTab('conversation')
	setEdit(null)
    if (s.workspaceId) {
      setExpanded(e => ({ ...e, [s.workspaceId!]: true }))
      setShowAll(a => ({ ...a, [s.workspaceId!]: true }))
    }
    return s
  }, [api, models, refreshList, view.model, view.provider, view.thinkingEffort, history.cancel, store, sync, setView])

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
    let consumed = false
    const fail = () => { if (!consumed) restore?.() }
    try {
      let id = currentId
      if (!id) {
        const s = await makeSession(selectedWs)
        id = s.id
      }
      const requestId = clientRequestId()
      const ticket = store.capture('body')
      let result: Awaited<ReturnType<Client['prompt']>> | undefined
      try {
        const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
		result = await api.prompt(id, content, spec || undefined, parentId, delivery, undefined, requestId)
        consumed = true
        if (!store.isCurrent(ticket) || store.sessionId !== id) {
          void refreshList()
          return
        }
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
		  if (store.isCurrent(ticket) && store.sessionId === id) setView(v => ({ ...applyRuntimeCatalog(v, detail), busy: true }))
		} catch (e) { toast.from(e) }
		return
	  }
	  if (result?.accepted === 'steered') {
		setView(v => appendOptimisticUser(v, content, result.clientRequestId ?? requestId))
		return
	  }
	  if (editedMessageId) {
		setView(v => {
		  const cut = v.nodes.findIndex(n => n.id === editedMessageId)
		  return appendOptimisticUser({ ...v, nodes: cut >= 0 ? v.nodes.slice(0, cut) : v.nodes }, content, result?.clientRequestId ?? requestId)
		})
	  } else {
		setView(v => appendOptimisticUser(v, content, result?.clientRequestId ?? requestId))
	  }
      void listen(id)
    } catch (e) {
      fail()
      toast.from(e)
    }
	}, [api, currentId, listen, makeSession, openSession, selectedWs, view.model, view.provider, store, setView, refreshList])

	const send = useCallback((delivery?: 'steer' | 'queue') => {
	  const content: Content[] = [...(draft.text.trim() ? [{ type: 'text', text: draft.text } as Content] : []), ...draft.attachments]
	  const snapshot = draft
	  setDraft({ text: '', attachments: [] })
	  return sendContent(content, undefined, undefined, delivery, () => setDraft(d => (d.text || d.attachments.length) ? d : snapshot))
	}, [draft, sendContent])

	const steerQueued = useCallback(async (queueId?: string) => {
	  const items = view.queued ?? []
	  const item = queueId ? items.find(q => q.id === queueId) : items.at(-1)
	  if (!currentId || !item) return
	  try {
	    const spec = view.provider && view.model ? `${view.provider}/${view.model}` : view.model
	    const requestId = item.clientRequestId ?? clientRequestId()
	    const ticket = store.capture('body')
	    const result = await api.prompt(currentId, [], spec || undefined, undefined, 'steer', item.id, requestId)
	    if (!store.isCurrent(ticket)) return
	    if (result?.accepted === 'steered') {
	      setView(v => ({
	        ...appendOptimisticUser(v, item.content ?? [], result.clientRequestId ?? requestId),
	        queued: (v.queued ?? []).filter(q => q.id !== item.id),
	      }))
	      return
	    }
	    const detail = await api.get(currentId, { fields: 'runtime' })
	    if (store.isCurrent(ticket) && store.sessionId === currentId) setView(v => ({ ...v, queued: detail.queued ?? [], busy: v.busy || !!detail.running }))
	  } catch (e) { toast.from(e) }
	}, [api, currentId, view.model, view.provider, view.queued, store, setView])

	const sendEdit = useCallback(() => {
	  if (!edit) return Promise.resolve()
	  const content: Content[] = [...(edit.draft.text.trim() ? [{ type: 'text', text: edit.draft.text } as Content] : []), ...edit.draft.attachments]
	  const snapshot = edit
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
    if (!next) return
    const provider = next.provider
    const model = next.id
    const thinkingEffort = clampThinkingEffort(view.thinkingEffort, next)
    setView(v => ({ ...v, provider, model, thinkingEffort }))
    setModelOpen(false)
    if (!currentId) return
    const ticket = store.capture('body')
    try {
      const out = await api.patch(currentId, { model: spec, thinkingEffort })
      if (!store.isCurrent(ticket)) return
	  setView(v => ({ ...v, liveRevision: v.liveRevision + 1, model: out.model ?? v.model, provider: out.provider ?? v.provider, thinkingEffort: out.thinkingEffort ?? thinkingEffort }))
    } catch (e) {
      toast.from(e)
    }
  }, [api, currentId, models, view.model, view.provider, view.thinkingEffort, store, setView])

  const selectedModel = useMemo(() => models.find(m => m.provider === view.provider && m.id === view.model), [models, view.model, view.provider])
	const switchThinking = useCallback(async (thinkingEffort: string) => {
		setView(v => ({ ...v, thinkingEffort }))
		if (!currentId) return
		const ticket = store.capture('body')
		try {
			const out = await api.patch(currentId, { thinkingEffort })
			if (!store.isCurrent(ticket)) return
			setView(v => ({ ...v, liveRevision: v.liveRevision + 1, thinkingEffort: out.thinkingEffort ?? thinkingEffort }))
		} catch (e) { toast.from(e) }
	}, [api, currentId, view.model, view.provider, store, setView])

  const byId = useMemo(() => new Map(sessions.map(s => [s.id, s])), [sessions])

  useEffect(() => {
    const runningWorkspaces = new Set(sessions.filter(s => s.running && s.workspaceId).map(s => s.workspaceId!))
    if (!runningWorkspaces.size) return
    setExpanded(current => {
      let changed = false
      const next = { ...current }
      for (const id of runningWorkspaces) {
        if (next[id] === undefined) {
          next[id] = true
          changed = true
        }
      }
      return changed ? next : current
    })
  }, [sessions])

  const forest = useMemo(() => buildSessionForest(sessions, workspaces), [sessions, workspaces])

  const trees = useMemo(() => {
    const rootsOf = (rows: SessionInfo[]) => rows.filter(session => !forest.parentOf.has(session.id))
    const used = new Set<string>()
    const groups = workspaces.map(ws => {
      const order = ws.sessionIds?.length
        ? [...ws.sessionIds, ...sessions.filter(s => s.workspaceId === ws.id && !ws.sessionIds!.includes(s.id)).map(s => s.id)]
        : sessions.filter(s => s.workspaceId === ws.id).map(s => s.id)
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

  const inspect = (n: ChatNode) => {
    setContextRequestId(null)
    setContextFocusId(n.id)
    setTab('context')
    if (currentId && !view.indexLoaded) void requestIndex(currentId)
  }
  const focusedContextRequest = useMemo(() => {
    if (!contextFocusId) return contextRequestId
    const record = view.records.find(item => item.id === contextFocusId)
    const requestId = record?.requestId ?? view.requests.find(request => request.resultId === contextFocusId)?.id
    if (record?.kind === 'tool' && requestId) {
      const index = view.requests.findIndex(request => request.id === requestId)
      return index < 0 ? null : view.requests[index + 1]?.id ?? null
    }
    return requestId ?? null
  }, [contextFocusId, contextRequestId, view.records, view.requests])

	const startEdit = useCallback((node: Extract<ChatNode, { kind: 'user' }>) => {
	  if (view.busy) return
	  setEdit({
		messageId: node.id,
		parentId: node.parentId ?? '',
		draft: { text: node.text, attachments: node.content.filter(c => c.type === 'image' || c.type === 'file' || c.type === 'workspace_file') },
	  })
	}, [view.busy])

	const forkMessage = useCallback(async (node: Extract<ChatNode, { kind: 'assistant' }>) => {
	  if (!currentId) return
	  try {
		const child = await api.fork(currentId, node.id)
		void refreshList()
		await openSession(child.id)
	  } catch (e) { toast.from(e) }
	}, [api, currentId, openSession, refreshList])

	const regenerate = useCallback((node: Extract<ChatNode, { kind: 'assistant' }>) => {
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

  const empty = view.nodes.length === 0 && !view.hasMore && !view.compactTurns?.length
  const stats = useMemo(() => sessionStats(view), [view])
  const requestItems = useMemo(() => userRequests(view.allEntries, view.leafId, view.nodes, view.compactTurns), [view.allEntries, view.leafId, view.nodes, view.compactTurns])

  useEffect(() => {
    setJumpToId(null)
    setActiveRequestId(null)
    setContextRequestId(null)
    setContextFocusId(null)
  }, [currentId])

  useEffect(() => {
    if (tab === 'context' && currentId && openingId !== currentId && !view.indexLoaded) void requestIndex(currentId)
  }, [tab, currentId, openingId, view.indexLoaded, requestIndex])

  const jumpToRequest = useCallback(async (id: string) => {
    chatControl.current?.read()
    setJumpToId(null)
    const version = ++jumpVersion.current
    const sessionId = currentIdRef.current
    let target = conversationTarget(viewRef.current, id)
    setSeekingId(id)
    setJumpErrorId(null)
    try {
      while (!target || !viewRef.current.nodes.some(node => node.id === target)) {
        const turnId = target && conversationTargetTurn(viewRef.current, target)
        if (turnId && !viewRef.current.loadedTurnIds?.includes(turnId)) {
          const ok = await history.requestTurn(turnId)
          if (version !== jumpVersion.current || sessionId !== currentIdRef.current) return
          if (!ok) { setJumpErrorId(id); return }
          target = conversationTarget(viewRef.current, id)
          if (target && viewRef.current.nodes.some(node => node.id === target)) break
        }
        const page = await loadOlder(500)
        if (version !== jumpVersion.current || sessionId !== currentIdRef.current) return
        if (!page) { setJumpErrorId(id); return }
        target = conversationTarget(viewRef.current, id)
        const pendingTurn = target && conversationTargetTurn(viewRef.current, target)
        const canLoadTurn = pendingTurn && !viewRef.current.loadedTurnIds?.includes(pendingTurn)
        if ((!target || !viewRef.current.nodes.some(node => node.id === target)) && !viewRef.current.hasMore && !canLoadTurn) { setJumpErrorId(id); return }
      }
      if (version === jumpVersion.current && sessionId === currentIdRef.current) setJumpToId(target)
    } finally {
      if (version === jumpVersion.current) setSeekingId(null)
    }
  }, [loadOlder, history.requestTurn])
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
                const ticket = store.capture('body')
                const ids = queued.filter(q => q.id !== item.id).map(q => q.id)
                void api.patch(currentId, { queued: ids }).then(() => api.get(currentId, { fields: 'runtime' })).then(detail => {
                  if (store.isCurrent(ticket)) setView(v => ({ ...v, queued: detail.queued ?? [] }))
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
      disabled={!!currentId && (openingId === currentId || view.runtimeReady === false)}
      loading={!!currentId && (openingId === currentId || view.runtimeReady === false)}
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
    const anchorId = currentId ? topLevelRoot(forest, currentId) : null
    const idx = anchorId ? rows.findIndex(s => s.id === anchorId) : -1
    const n = idx >= SHOW ? idx + 1 : SHOW
    return rows.slice(0, n)
  }

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
    <MarkdownLinkProvider cwd={view.cwd} openFile={openMarkdownFile} loadFile={loadMarkdownFile}>
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
        <div className="session-list" data-testid="session-list" data-ready={listLoaded ? 'true' : undefined}>
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
            const open = expanded[ws.id] === true
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
                  <button type="button" className="ws-toggle" aria-expanded={open} onClick={() => { setSelectedWs(ws.id); setExpanded(e => ({ ...e, [ws.id]: !open })) }}>
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
            {currentId && (openingId === currentId || ['recovering', 'retrying', 'error'].includes(syncStatus.phase)) ? (
              <div className="transcript-sync-status" data-testid="transcript-sync-status" role="status" aria-live="polite">
                <span>{t(openingId === currentId || syncStatus.phase === 'recovering' ? 'chat.syncRecovering' : syncStatus.phase === 'error' ? 'chat.syncError' : 'chat.syncRetrying')}</span>
                {syncStatus.phase === 'error' && openingId !== currentId ? <button type="button" onClick={() => void sync.recover(true)}>{t('chat.syncRetry')}</button> : null}
              </div>
            ) : null}
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
            <button type="button" className={`tab${tab === 'context' ? ' active' : ''}`} data-testid="tab-context" onClick={() => setTab('context')}>{t('tab.context')}</button>
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
              runtime={{
                processes: view.processes ?? [],
                agents: view.agents ?? [],
                onStop: async processID => {
                  const owner = view.processes?.find(p => p.session_id === processID)?.owner_session_id || currentId
                  if (!owner) return
                  try { await api.abort(owner, 'process', processID); await refreshOpenRuntime() } catch (error) { toast.from(error) }
                },
                onInterrupt: async sessionID => { try { await api.abort(sessionID); await refreshOpenRuntime() } catch (error) { toast.from(error) } },
              }}
            />
          </div>
        ) : tab === 'context' ? (
          <div className="conv-body">
            <ContextView
              key={currentId ?? 'none'}
              view={view}
              selectedRequestId={focusedContextRequest}
              onSelectRequest={id => { setContextFocusId(null); setContextRequestId(id) }}
              onHydrate={requestHydrate}
              onLocateEntry={id => { setTab('conversation'); void jumpToRequest(id) }}
              indexLoading={indexLoading}
              indexError={indexError}
              onRetryIndex={() => { if (currentId) void requestIndex(currentId, true) }}
            />
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
                  tabIndex={0}
                >
				  <ChatView
                    key={`${currentId}:${sessionRevision}`}
                    controlRef={chatControl}
                    navigationPending={!!seekingId}
                    onAtBottom={setAtBottom}
                    onReadIntent={cancelJump}
                    compactTurns={view.compactTurns}
                    loadedTurnIds={view.loadedTurnIds}
                    onLoadTurn={history.requestTurn}
                    onLoadOlder={loadOlder}
                    hasMore={view.hasMore}
                    olderError={olderError}
					api={api}
					nodes={view.nodes}
					busy={view.busy}
					scrollRef={scrollRef}
					onHydrate={requestHydrate}
					processes={view.processes}
					onOpenFile={setPreviewPath}
                    onVisibleEntries={history.protect}
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
					branches={view.indexLoaded ? branchInfo : undefined}
                    onShowBranches={() => { if (currentId) void requestIndex(currentId) }}
					onBranch={(node, delta) => void switchBranch(node, delta)}
					jumpToId={jumpToId}
					onJumped={clearJump}
					onActiveRequest={setActiveRequestId}
					loadingOlder={loadingOlder}
					turnBase={view.turnBase}
					mode={messageView.mode}
					keep={messageView.keep}
				  />
                </div>
                <PerfHud target={scrollRef as React.RefObject<HTMLElement | null>} />
                <RequestNav
                  key={currentId ?? 'none'}
                  items={requestItems}
                  activeId={seekingId ?? jumpToId ?? activeRequestId}
                  onJump={jumpToRequest}
                  loading={indexLoading}
                  failed={indexError}
                  onOpen={() => { if (currentId) void requestIndex(currentId, indexError) }}
                  onRetry={() => { if (currentId) void requestIndex(currentId, true) }}
                />
                {seekingId ? <button type="button" className="history-seeking" data-testid="cancel-jump" onClick={cancelJump}>{t('chat.cancelJump')}</button> : null}
                {jumpErrorId ? <button type="button" className="history-seeking" data-testid="retry-jump" onClick={() => void jumpToRequest(jumpErrorId)}>{t('chat.retryJump')}</button> : null}
                {!atBottom ? (
                  <div className="to-bottom-slot">
                    <button type="button" className="to-bottom" data-testid="to-bottom" aria-label={t('chat.toBottom')} onClick={() => {
                      cancelJump()
                      chatControl.current?.latest()
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
	  <OutputPreview api={api} path={previewPath} onClose={() => setPreviewPath(null)} />
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
              ['mcp', t('settings.mcp')],
              ['extensions', t('settings.extensions')],
              ['prompt', t('settings.prompt')],
              ['message', t('settings.message')],
              ['notifications', t('settings.notifications')],
              ['appearance', t('settings.appearanceLanguage')],
              ['about', t('settings.about')],
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
                  <SettingsToggles kind="extensions" api={api} onConfigure={openExtensionConfig} onChanged={refreshExtensions} refreshKey={extensionCatalogRev} />
                ) : page === 'mcp' ? (
                  <SettingsToggles kind="mcp" api={api} sessionId={currentId} workspaceId={selectedWs} />
                ) : page === 'prompt' ? (
                  <PromptSettings api={api} workspaceId={selectedWs} />
                ) : page === 'message' ? (
                  <MessageSettings api={api} view={messageView} onView={changeMessageView} />
                ) : page === 'notifications' ? (
                  <NotificationSettings
                    enabled={notifyEnabled}
                    permission={notifyPerm}
                    pushReady={pushReady}
                    onToggle={on => void toggleNotify(on)}
                    onTest={sendTestNotification}
                  />
                ) : page === 'about' ? (
                  <AboutSettings meta={serverMeta} />
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
    </MarkdownLinkProvider>
  )
}
