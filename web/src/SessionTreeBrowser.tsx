import { useCallback, useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { ICheck, IChevRight, IClose, ITraj } from './icons'
import { useI18n } from './i18n'
import { buildSessionTree, orderedChildren, sessionLabel, type SessionTreeModel } from './session-tree'
import type { SessionInfo, WorkspaceInfo } from './types'
import { useDialogFocus } from './useDialogFocus'

type TreeView = {
  path: SessionInfo[]
  left: SessionInfo[]
  selected: SessionInfo | null
  right: SessionInfo[]
}

function SessionColumn({
  sessions,
  selectedId,
  model,
  onPick,
}: {
  sessions: SessionInfo[]
  selectedId: string | null
  model: SessionTreeModel
  onPick: (id: string) => void
}) {
  const { t } = useI18n()
  return (
    <ul className="dir-col session-tree-col">
      {sessions.map(session => {
        const selected = session.id === selectedId
        const children = orderedChildren(model, session.id)
        return (
          <li key={session.id}>
            <button
              type="button"
              className={`dir-row session-tree-row${selected ? ' on' : ''}`}
              data-testid="session-tree-row"
              aria-current={selected || undefined}
              onMouseDown={event => event.preventDefault()}
              onClick={event => { event.preventDefault(); event.stopPropagation(); onPick(session.id) }}
            >
              <span className={`dir-ico${selected ? ' on' : ''}`}><ITraj /></span>
              <span className="session-tree-copy">
                <span className="dir-name">{sessionLabel(session)}</span>
                <span className="session-tree-meta">
                  {session.model || '—'}
                  {session.running ? ` · ${t('tree.running')}` : ''}
                </span>
              </span>
              <span className="session-tree-flag session-tree-kind">{t('tree.label')}</span>
              {model.unresolved.has(session.id) ? <span className="session-tree-flag">{t('tree.unresolved')}</span> : null}
              {selected ? <span className="session-tree-check"><ICheck /></span> : children.length ? <span className="dir-row-chev"><IChevRight /></span> : null}
            </button>
          </li>
        )
      })}
      {!sessions.length ? <li className="session-tree-empty">{t('tree.noChildren')}</li> : null}
    </ul>
  )
}

export function SessionTreeBrowser({
  open,
  sessions,
  workspaces,
  currentId,
  onClose,
  onSelect,
}: {
  open: boolean
  sessions: SessionInfo[]
  workspaces: WorkspaceInfo[]
  currentId: string | null
  onClose: () => void
  onSelect: (id: string) => boolean | Promise<boolean>
}) {
  const { t } = useI18n()
  const [view, setView] = useState<TreeView | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const dialogRef = useDialogFocus<HTMLDivElement>({ open, onEscape: () => { if (!busy) onClose() } })
  const model = useMemo(() => buildSessionTree(sessions, workspaces, currentId), [currentId, sessions, workspaces])
  const byId = useMemo(() => new Map(sessions.map(session => [session.id, session])), [sessions])

  const pathFor = useCallback((targetId: string, tree: SessionTreeModel): SessionInfo[] => {
    const path: SessionInfo[] = []
    const seen = new Set<string>()
    let cursor: string | undefined = targetId
    while (cursor && !seen.has(cursor)) {
      seen.add(cursor)
      const session = byId.get(cursor)
      if (!session) break
      path.unshift(session)
      cursor = tree.parentOf.get(cursor)
    }
    return path.length ? path : [tree.root]
  }, [byId])

  const viewFor = useCallback((targetId: string, tree: SessionTreeModel): TreeView => {
    const target = byId.get(targetId) ?? tree.root
    const parentId = tree.parentOf.get(target.id)
    if (!parentId) {
      if (tree.unresolved.has(target.id)) {
        return {
          path: [target],
          left: [target],
          selected: target,
          right: orderedChildren(tree, target.id),
        }
      }
      const roots = target.id === tree.root.id
        ? tree.unresolvedRoots.filter(session => session.id !== target.id)
        : []
      return {
        path: [target],
        left: [...orderedChildren(tree, target.id), ...roots],
        selected: null,
        right: [],
      }
    }
    return {
      path: pathFor(target.id, tree),
      left: orderedChildren(tree, parentId),
      selected: target,
      right: orderedChildren(tree, target.id),
    }
  }, [byId, pathFor])

  useEffect(() => {
    if (!open) {
      setView(null)
      setBusy(false)
      setErr(null)
      return
    }
    if (model) setView(viewFor(currentId ?? model.root.id, model))
    else setView(null)
    setErr(null)
  }, [currentId, model, open, viewFor])

  const pick = useCallback((id: string) => {
    if (!model || busy) return
    setErr(null)
    setView(viewFor(id, model))
  }, [busy, model, viewFor])

  const confirm = useCallback(async () => {
    const targetId = view?.selected?.id
    if (!targetId || busy) return
    setBusy(true)
    setErr(null)
    try {
      if (await onSelect(targetId)) onClose()
    } catch (error) {
      setErr(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }, [busy, onClose, onSelect, view])

  if (!open) return null

  const picker = (
    <div className="modal-mask" onClick={() => { if (!busy) onClose() }} data-testid="session-tree-browser-mask">
      <div
        ref={dialogRef}
        className="dir-browser session-tree-browser"
        data-testid="session-tree-browser"
        onClick={event => event.stopPropagation()}
        onKeyDown={event => {
          if (event.key === 'Enter' && event.target === event.currentTarget && view?.selected && !busy) {
            event.preventDefault()
            void confirm()
          }
        }}
        role="dialog"
        aria-modal="true"
        aria-label={t('tree.title')}
        tabIndex={-1}
      >
        <div className="dir-head">
          <div className="session-tree-title-row">
            <h2>{t('tree.title')}</h2>
            <button type="button" className="icon-btn" onClick={onClose} disabled={busy} aria-label={t('dialog.close')}><IClose /></button>
          </div>
          <div className="dir-crumb-bar">
            <span className="dir-crumb-trail" role="navigation">
              {(view?.path ?? []).map((session, index) => (
                <span key={session.id} className="dir-crumb-seat">
                  {index > 0 ? <span className="dir-crumb-chev"><IChevRight /></span> : null}
                  <button
                    type="button"
                    className={`dir-crumb${index === (view?.path.length ?? 0) - 1 ? ' current' : ''}`}
                    disabled={busy}
                    aria-current={index === (view?.path.length ?? 0) - 1 ? 'location' : undefined}
                    onClick={() => model && setView(viewFor(session.id, model))}
                  >
                    {sessionLabel(session)}
                  </button>
                </span>
              ))}
            </span>
          </div>
        </div>
        <div className="dir-body">
          {model && view ? (
            <div className="dir-miller session-tree-miller">
              <SessionColumn sessions={view.left} selectedId={view.selected?.id ?? null} model={model} onPick={pick} />
              <span className="dir-split" />
              <SessionColumn sessions={view.right} selectedId={null} model={model} onPick={pick} />
            </div>
          ) : (
            <div className="session-tree-empty session-tree-empty-main">{t('tree.empty')}</div>
          )}
          {err ? <div className="dir-error" role="alert">{err}</div> : null}
        </div>
        <div className="dir-foot">
          <div className="dir-foot-left">
            <span className="session-tree-hint">{t('tree.hint')}</span>
          </div>
          <div className="dir-foot-right">
            <button type="button" className="dir-cancel" disabled={busy} onClick={onClose}>{t('tree.cancel')}</button>
            <button type="button" className="primary-btn" data-testid="session-tree-confirm" disabled={!view?.selected || busy} onClick={() => void confirm()}>{t('tree.confirm')}</button>
          </div>
        </div>
      </div>
    </div>
  )

  return createPortal(picker, document.body)
}
