import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { SessionCommand } from './types'

function oneLine(s?: string) {
  return (s ?? '').replace(/\s+/g, ' ').trim()
}

export type PalettePick =
  | { kind: 'command'; item: SessionCommand }
  | { kind: 'completion'; item: SessionCommand; value: string }

type Row =
  | { key: string; kind: 'command'; item: SessionCommand; desc: string }
  | { key: string; kind: 'completion'; item: SessionCommand; value: string; desc: string }

function paletteRows(query: string, items: SessionCommand[]): Row[] {
  const after = query.startsWith('/') ? query.slice(1) : query
  const space = after.indexOf(' ')
  const namePart = (space < 0 ? after : after.slice(0, space)).toLowerCase()
  const rest = space < 0 ? '' : after.slice(space + 1)
  const exact = items.find(item => item.name.toLowerCase() === namePart)
  if (exact?.completions?.length && (space >= 0 || query === `/${exact.name}`)) {
    const q = rest.trim().toLowerCase()
    const hits = exact.completions.filter(value => !q || value.toLowerCase().startsWith(q))
    if (rest.trim() === '' || hits.length > 0) {
      return hits.map(value => ({
        key: `${exact.source}:${exact.name}:${value}`,
        kind: 'completion' as const,
        item: exact,
        value,
        desc: '',
      }))
    }
    return []
  }
  return items.filter(item => {
    if (!namePart) return true
    return item.name.toLowerCase().includes(namePart) || oneLine(item.description).toLowerCase().includes(namePart)
  }).map(item => ({
    key: `${item.source}:${item.name}`,
    kind: 'command' as const,
    item,
    desc: oneLine(item.description),
  }))
}

function matchesDraft(query: string, row: Row) {
  if (row.kind === 'command') {
    const prefix = `/${row.item.name}`
    return query === prefix || query.startsWith(`${prefix} `)
  }
  const prefix = `/${row.item.name} ${row.value}`
  return query === prefix || query === `${prefix} `
}

export function CommandPalette({
  open,
  query,
  items,
  anchor,
  onPick,
  onClose,
}: {
  open: boolean
  query: string
  items: SessionCommand[]
  anchor: RefObject<HTMLElement | null>
  onPick: (pick: PalettePick) => void
  onClose: () => void
}) {
  const id = useId()
  const menu = useRef<HTMLDivElement>(null)
  const [active, setActive] = useState(0)
  const [position, setPosition] = useState<CSSProperties>({})
  const rows = paletteRows(query, items)
  const rowsRef = useRef(rows)
  rowsRef.current = rows
  const activeRef = useRef(active)
  activeRef.current = active
  const queryRef = useRef(query)
  queryRef.current = query
  const onPickRef = useRef(onPick)
  onPickRef.current = onPick
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  const place = () => {
    const rect = anchor.current?.getBoundingClientRect()
    if (!rect) return
    const gap = 8
    // Why: the palette used to anchor to the / button in the composer row.
    // "Upward" then sat on top of the textarea and hid the input. Anchor the
    // whole composer card and prefer sitting above it so the field stays visible.
    const width = Math.min(Math.max(rect.width, 280), window.innerWidth - 16)
    const above = rect.top - gap - 8
    const below = window.innerHeight - rect.bottom - gap - 8
    const upward = above >= 96
    setPosition({
      left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)),
      width,
      maxHeight: Math.max(96, Math.min(320, upward ? above : below)),
      ...(upward ? { bottom: window.innerHeight - rect.top + gap } : { top: rect.bottom + gap }),
    })
  }

  useLayoutEffect(() => { if (open) place() }, [open, rows.length]) // eslint-disable-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    if (!open) return
    menu.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, open])
  useEffect(() => { setActive(0) }, [query, open])
  useEffect(() => {
    if (!open) return
    const closeOutside = (event: PointerEvent) => {
      const node = event.target as Node
      if (!anchor.current?.contains(node) && !menu.current?.contains(node)) onCloseRef.current()
    }
    const onViewport = () => place()
    document.addEventListener('pointerdown', closeOutside)
    window.addEventListener('resize', onViewport)
    window.addEventListener('scroll', onViewport, true)
    const node = anchor.current
    const ro = typeof ResizeObserver !== 'undefined' && node ? new ResizeObserver(onViewport) : null
    if (node && ro) ro.observe(node)
    return () => {
      document.removeEventListener('pointerdown', closeOutside)
      window.removeEventListener('resize', onViewport)
      window.removeEventListener('scroll', onViewport, true)
      ro?.disconnect()
    }
  }, [open, anchor])
  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return
      const list = rowsRef.current
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        event.stopPropagation()
        if (list.length) setActive(i => (i + 1) % list.length)
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        event.stopPropagation()
        if (list.length) setActive(i => (i - 1 + list.length) % list.length)
        return
      }
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Enter' || event.shiftKey) return
      const row = list[activeRef.current]
      if (!row) {
        onCloseRef.current()
        return
      }
      // Already-typed `/name` or `/name args` (or a completed subcommand): let composer Enter send.
      if (matchesDraft(queryRef.current, row)) {
        onCloseRef.current()
        return
      }
      event.preventDefault()
      event.stopPropagation()
      if (row.kind === 'completion') onPickRef.current({ kind: 'completion', item: row.item, value: row.value })
      else onPickRef.current({ kind: 'command', item: row.item })
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [open])

  if (!open || rows.length === 0) return null
  return createPortal(
    <div
      ref={menu}
      className="command-palette"
      data-testid="command-palette"
      role="listbox"
      id={id}
      style={position}
    >
      {rows.map((row, i) => {
        const name = row.kind === 'completion' ? row.value : `/${row.item.name}`
        const hint = row.kind === 'command' ? row.item.argumentHint : undefined
        const testid = row.kind === 'completion' ? `command-item-${row.item.name}-${row.value}` : `command-item-${row.item.name}`
        return (
          <button
            type="button"
            key={row.key}
            role="option"
            aria-selected={i === active}
            className={`command-item${i === active ? ' active' : ''}`}
            data-testid={testid}
            title={row.desc || undefined}
            onMouseEnter={() => setActive(i)}
            onMouseDown={event => event.preventDefault()}
            onClick={() => {
              if (row.kind === 'completion') onPick({ kind: 'completion', item: row.item, value: row.value })
              else onPick({ kind: 'command', item: row.item })
            }}
          >
            <span className="command-label">
              <span className="command-name">{name}</span>
              {hint ? <span className="command-hint">{hint}</span> : null}
            </span>
            {row.desc ? <span className="command-desc">{row.desc}</span> : null}
          </button>
        )
      })}
    </div>,
    document.body,
  )
}
