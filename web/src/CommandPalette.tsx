import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import type { SessionCommand } from './types'

function oneLine(s?: string) {
  return (s ?? '').replace(/\s+/g, ' ').trim()
}

function matchesDraft(query: string, item: SessionCommand) {
  const prefix = `/${item.name}`
  return query === prefix || query.startsWith(`${prefix} `)
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
  onPick: (item: SessionCommand) => void
  onClose: () => void
}) {
  const id = useId()
  const menu = useRef<HTMLDivElement>(null)
  const [active, setActive] = useState(0)
  const [position, setPosition] = useState<CSSProperties>({})
  const namePart = query.replace(/^\//, '').split(/\s/, 1)[0].toLowerCase()
  const filtered = items.filter(item => {
    if (!namePart) return true
    return item.name.toLowerCase().includes(namePart) || oneLine(item.description).toLowerCase().includes(namePart)
  })
  const filteredRef = useRef(filtered)
  filteredRef.current = filtered
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
    const gap = 6
    const width = Math.min(440, Math.max(320, window.innerWidth - 16))
    const below = window.innerHeight - rect.bottom - gap - 8
    const above = rect.top - gap - 8
    const upward = below < 180 && above > below
    setPosition({
      left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)),
      width,
      maxHeight: Math.max(96, Math.min(280, upward ? above : below)),
      ...(upward ? { bottom: window.innerHeight - rect.top + gap } : { top: rect.bottom + gap }),
    })
  }

  useLayoutEffect(() => { if (open) place() }, [open, filtered.length]) // eslint-disable-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    if (!open) return
    menu.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, open])
  useEffect(() => { setActive(0) }, [namePart, open])
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
    return () => {
      document.removeEventListener('pointerdown', closeOutside)
      window.removeEventListener('resize', onViewport)
      window.removeEventListener('scroll', onViewport, true)
    }
  }, [open, anchor])
  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return
      const list = filteredRef.current
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
      const item = list[activeRef.current]
      if (!item) {
        event.preventDefault()
        event.stopPropagation()
        onCloseRef.current()
        return
      }
      // Already-typed `/name` or `/name args`: let composer Enter send.
      // Otherwise insert the highlighted command and wait for a later Enter.
      if (matchesDraft(queryRef.current, item)) {
        onCloseRef.current()
        return
      }
      event.preventDefault()
      event.stopPropagation()
      onPickRef.current(item)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [open])

  if (!open) return null
  return createPortal(
    <div
      ref={menu}
      className="command-palette"
      data-testid="command-palette"
      role="listbox"
      id={id}
      style={position}
    >
      {filtered.length === 0 ? <div className="command-empty">/</div> : filtered.map((item, i) => {
        const desc = oneLine(item.description)
        return (
          <button
            type="button"
            key={`${item.source}:${item.name}`}
            role="option"
            aria-selected={i === active}
            className={`command-item${i === active ? ' active' : ''}`}
            data-testid={`command-item-${item.name}`}
            title={desc || undefined}
            onMouseEnter={() => setActive(i)}
            onMouseDown={event => event.preventDefault()}
            onClick={() => onPick(item)}
          >
            <span className="command-label">
              <span className="command-name">/{item.name}</span>
              {item.argumentHint ? <span className="command-hint">{item.argumentHint}</span> : null}
              {item.completions?.length ? <span className="command-hint">{item.completions.join(' ')}</span> : null}
            </span>
            {desc ? <span className="command-desc">{desc}</span> : null}
          </button>
        )
      })}
    </div>,
    document.body,
  )
}
