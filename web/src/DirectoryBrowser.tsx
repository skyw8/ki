import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ICheck, IChevRight, IFolder, IFolderOpen, IPencil, IPlus } from './icons'
import type { Client } from './api'
import type { FsEntry, FsListing } from './types'

const PREVIEW_MS = 250
const SLOW_MS = 300

function samePath(a: string, b: string): boolean {
  const n = (p: string) => p.replace(/[\\/]+$/, '') || p
  return n(a) === n(b)
}

function displayCrumbs(listing: FsListing): FsEntry[] {
  const crumbs = listing.crumbs ?? []
  const homeIndex = crumbs.findIndex(c => samePath(c.path, listing.home))
  if (homeIndex === -1) return crumbs
  return [{ name: '主目录', path: listing.home, hidden: false }, ...crumbs.slice(homeIndex + 1)]
}

function levelDir(listing: FsListing): string {
  const sep = listing.separator || '/'
  return listing.path.endsWith(sep) ? listing.path : listing.path + sep
}

function draftDir(listing: FsListing, draft: string): string | null {
  const sep = listing.separator || '/'
  const cut = sep === '\\'
    ? Math.max(draft.lastIndexOf('\\'), draft.lastIndexOf('/'))
    : draft.lastIndexOf('/')
  return cut === -1 ? null : draft.slice(0, cut + 1)
}

function visibleEntries(entries: FsEntry[], selected: string | null, showHidden: boolean, prefix: string | null): FsEntry[] {
  const needle = (prefix ?? '').toLowerCase()
  const displayable = (e: FsEntry) => showHidden || !e.hidden || needle.startsWith('.')
  const matches = (e: FsEntry) => displayable(e) && e.name.toLowerCase().startsWith(needle)
  const narrowing = needle !== '' && entries.some(matches)
  return entries.filter(e => {
    if (e.path === selected) return true
    if (narrowing) return matches(e)
    return showHidden || !e.hidden
  })
}

function Column({
  entries, selected, prefix, showHidden, editing, onPick,
}: {
  entries: FsEntry[]
  selected: string | null
  prefix: string | null
  showHidden: boolean
  editing: boolean
  onPick: (e: FsEntry) => void
}) {
  return (
    <div className="dir-col" role="list">
      {visibleEntries(entries ?? [], selected, showHidden, prefix).map(e => {
        const on = e.path === selected
        return (
          <button
            key={e.path}
            type="button"
            role="listitem"
            className={`dir-row${on ? ' on' : ''}`}
            data-testid="dir-row"
            aria-current={on || undefined}
            onMouseDown={ev => ev.preventDefault()}
            onClick={ev => { ev.preventDefault(); ev.stopPropagation(); onPick(e) }}
          >
            {on ? <span className="dir-ico on"><IFolderOpen /></span> : <span className="dir-ico"><IFolder /></span>}
            <span className="dir-name">{e.name}</span>
            <span className="dir-row-chev"><IChevRight /></span>
          </button>
        )
      })}
    </div>
  )
}

export function DirectoryBrowser({
  api, open, busy, onOpen, onClose,
}: {
  api: Client
  open: boolean
  busy?: boolean
  onOpen: (path: string) => void
  onClose: () => void
}) {
  const [parent, setParent] = useState<FsListing | null>(null)
  const [selected, setSelected] = useState<FsEntry | null>(null)
  const [child, setChild] = useState<FsListing | null>(null)
  const [draft, setDraft] = useState<string | null>(null)
  const [hidden, setHidden] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [slow, setSlow] = useState(false)
  const [mkdir, setMkdir] = useState(false)
  const [newName, setNewName] = useState('')
  const [createErr, setCreateErr] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const seq = useRef(0)
  const ac = useRef<AbortController | null>(null)
  const viewRef = useRef({ parent, child })
  const inputRef = useRef<HTMLInputElement | null>(null)
  const crumbRef = useRef<HTMLSpanElement | null>(null)

  useEffect(() => { viewRef.current = { parent, child } }, [parent, child])

  const supersede = () => {
    ac.current?.abort()
    ac.current = new AbortController()
    return ++seq.current
  }

  const list = useCallback((path?: string) => {
    const n = supersede()
    return { n, p: api.listFS(path, ac.current!.signal) }
  }, [api])

  const land = useCallback((path: string | undefined, closeEditor: boolean) => {
    const { n, p } = list(path)
    setLoading(true)
    if (closeEditor) setErr(null)
    p.then(async target => {
      if (n !== seq.current) return
      const crumbs = displayCrumbs(target)
      if (crumbs.length < 2) {
        setParent(target)
        setSelected(null)
        setChild(null)
        setLoading(false)
        if (closeEditor) setDraft(null)
        return
      }
      const parentCrumb = (target.crumbs ?? []).at(-2)
      if (!parentCrumb) {
        setParent(target)
        setSelected(null)
        setChild(null)
        setLoading(false)
        if (closeEditor) setDraft(null)
        return
      }
      try {
        const parentLevel = await api.listFS(parentCrumb.path, ac.current?.signal)
        if (n !== seq.current) return
        const match = (parentLevel.entries ?? []).find(e => samePath(e.path, target.path))
        if (!match) {
          setParent(target)
          setSelected(null)
          setChild(null)
        } else {
          setParent(parentLevel)
          setSelected(match)
          setChild(target)
        }
      } catch {
        if (n !== seq.current) return
        setParent(target)
        setSelected(null)
        setChild(null)
      }
      setLoading(false)
      if (closeEditor) setDraft(null)
    }).catch(e => {
      if (n !== seq.current) return
      setLoading(false)
      if (closeEditor) setErr(e instanceof Error ? e.message : String(e))
    })
  }, [api, list])

  const navigate = useCallback((path?: string) => { land(path, true) }, [land])

  const select = useCallback((entry: FsEntry) => {
    const { n, p } = list(entry.path)
    setDraft(null)
    setSelected(entry)
    setChild(null)
    setLoading(true)
    setErr(null)
    p.then(next => {
      if (n !== seq.current) return
      setChild(next)
      setLoading(false)
    }).catch(e => {
      if (n !== seq.current) return
      setLoading(false)
      setErr(e instanceof Error ? e.message : String(e))
      setSelected(null)
    })
  }, [list])

  const advance = useCallback((entry: FsEntry) => {
    if (!child) return
    setParent(child)
    select(entry)
  }, [child, select])

  useEffect(() => {
    if (!open) {
      ac.current?.abort()
      setParent(null)
      setSelected(null)
      setChild(null)
      setDraft(null)
      setHidden(false)
      setErr(null)
      setMkdir(false)
      setLoading(false)
      return
    }
    navigate()
  }, [open, navigate])

  useEffect(() => {
    if (!loading) {
      setSlow(false)
      return
    }
    const t = window.setTimeout(() => setSlow(true), SLOW_MS)
    return () => window.clearTimeout(t)
  }, [loading])

  useEffect(() => {
    if (draft === null) return
    const t = window.setTimeout(() => {
      const current = viewRef.current.child ?? viewRef.current.parent
      if (!current) return
      const directory = draftDir(current, draft)
      if (directory === null) return
      if (directory === levelDir(current)) return
      land(directory, false)
    }, PREVIEW_MS)
    return () => window.clearTimeout(t)
  }, [draft, land])

  const crumbSource = child ?? parent
  const crumbs = crumbSource ? displayCrumbs(crumbSource) : []
  useEffect(() => {
    const el = crumbRef.current
    if (el) el.scrollLeft = el.scrollWidth
  }, [crumbs.at(-1)?.path])

  const prefix = (() => {
    if (draft === null || !crumbSource) return null
    const directory = draftDir(crumbSource, draft)
    if (directory === null) return null
    if (directory !== levelDir(crumbSource)) return null
    return draft.slice(directory.length)
  })()

  const two = selected !== null
  const target = selected?.path ?? parent?.path ?? null
  const editing = draft !== null
  const inert = !!busy

  const startEdit = () => {
    if (!parent) {
      setDraft('')
      return
    }
    const base = selected?.path ?? parent.path
    const sep = parent.separator || '/'
    setDraft(base.endsWith(sep) ? base : base + sep)
  }

  const createFolder = async () => {
    if (!target || !newName.trim() || creating) return
    setCreating(true)
    setCreateErr(null)
    try {
      const out = await api.createFS(target, newName)
      setCreating(false)
      setMkdir(false)
      setNewName('')
      const { n, p } = list(target)
      setLoading(true)
      const level = await p
      if (n !== seq.current) return
      setParent(level)
      setLoading(false)
      select({ name: newName, path: out.path, hidden: false })
    } catch (e) {
      setCreating(false)
      setCreateErr(e instanceof Error ? e.message : String(e))
    }
  }

  const picker = (
    <div className="modal-mask" onClick={() => { if (!mkdir && !busy) onClose() }} data-testid="dir-browser-mask">
      <div
        className="dir-browser"
        data-testid="dir-browser"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-label="选择工作区目录"
        onKeyDown={e => {
          if (e.key !== 'Escape' || draft === null) return
          e.stopPropagation()
          setDraft(null)
          if (!child) setSelected(null)
        }}
      >
        <div className="dir-head">
          <h2>选择工作区目录</h2>
          <div className={`dir-crumb-bar${editing ? ' editing' : ''}`}>
            {editing ? (
              <input
                ref={inputRef}
                className="dir-path-input"
                data-testid="dir-path"
                aria-label="编辑路径"
                autoFocus
                value={draft}
                disabled={inert}
                onChange={e => setDraft(e.target.value)}
                onKeyDown={e => {
                  if (e.key === 'Enter' && draft.trim()) {
                    e.preventDefault()
                    navigate(draft)
                  }
                }}
              />
            ) : (
              <>
                <span className="dir-crumb-trail" role="navigation" ref={crumbRef}>
                  {crumbs.map((c, i) => (
                    <span key={c.path} className="dir-crumb-seat">
                      {i > 0 ? <span className="dir-crumb-chev"><IChevRight /></span> : null}
                      <button type="button" className="dir-crumb" disabled={inert} onClick={e => { e.preventDefault(); navigate(c.path) }}>{c.name}</button>
                    </span>
                  ))}
                </span>
                <button
                  type="button"
                  className="dir-edit-zone"
                  data-testid="dir-path"
                  aria-label="编辑路径"
                  title="编辑路径"
                  disabled={inert}
                  onClick={startEdit}
                >
                  <IPencil />
                </button>
              </>
            )}
          </div>
        </div>
        <div className="dir-body">
          <div className="dir-miller">
            {parent ? (
              <Column
                entries={parent.entries}
                selected={selected?.path ?? null}
                prefix={child ? null : prefix}
                showHidden={hidden}
                editing={editing}
                onPick={select}
              />
            ) : null}
            {two ? <span className="dir-split" /> : null}
            {two && child ? (
              <Column
                entries={child.entries}
                selected={null}
                prefix={prefix}
                showHidden={hidden}
                editing={editing}
                onPick={advance}
              />
            ) : null}
          </div>
          {loading && slow ? <div className="dir-loading-float">加载中…</div> : null}
          {err ? <div className="dir-error">{err}</div> : null}
        </div>
        <div className="dir-foot">
          <div className="dir-foot-left">
            <button
              type="button"
              className="dir-new"
              data-testid="dir-new-folder"
              disabled={!parent || inert}
              onClick={e => {
                e.preventDefault()
                e.stopPropagation()
                setDraft(null)
                setNewName('')
                setCreateErr(null)
                setMkdir(true)
              }}
            >
              <IPlus /> 新建文件夹
            </button>
            <button type="button" className={`dir-hidden${hidden ? ' on' : ''}`} aria-pressed={hidden} disabled={inert} onClick={() => setHidden(v => !v)}>
              显示隐藏文件{hidden ? <ICheck /> : null}
            </button>
          </div>
          <div className="dir-foot-right">
            <button type="button" className="dir-cancel" disabled={inert} onClick={onClose}>取消</button>
            <button
              type="button"
              className="primary-btn"
              data-testid="dir-open"
              disabled={!target || loading || inert || editing}
              onClick={ev => { ev.preventDefault(); if (target) onOpen(target) }}
            >
              打开
            </button>
          </div>
        </div>
      </div>

    </div>
  )

  if (!open) return null

  const createDlg = mkdir ? (
    <div className="dir-create-mask" data-testid="dir-create" onClick={() => { if (!creating) setMkdir(false) }}>
      <div className="dir-create" role="dialog" aria-label="新建文件夹" onClick={e => e.stopPropagation()}>
        <h3>新建文件夹</h3>
        <p>在 {selected?.name ?? crumbs.at(-1)?.name ?? '主目录'} 中创建</p>
        <input
          data-testid="dir-new-name"
          placeholder="未命名文件夹"
          autoFocus
          disabled={creating}
          value={newName}
          onChange={e => setNewName(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter') { e.preventDefault(); void createFolder() }
            if (e.key === 'Escape') { e.stopPropagation(); if (!creating) setMkdir(false) }
          }}
        />
        {createErr ? <div className="dir-error">{createErr}</div> : null}
        <div className="dir-create-actions">
          <button type="button" disabled={creating} onClick={() => setMkdir(false)}>取消</button>
          <button type="button" className="primary-btn" disabled={creating || !newName.trim()} onClick={() => void createFolder()}>创建</button>
        </div>
      </div>
    </div>
  ) : null

  return createPortal(
    <>
      {picker}
      {createDlg}
    </>,
    document.body,
  )
}
