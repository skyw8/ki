import { useEffect, useId, useMemo, useRef, useState, type CSSProperties } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { IMenu } from './icons'
import { useI18n } from './i18n'
import type { UserRequest } from './model'

const FILTER_AFTER = 12
const VIRTUALIZE_AFTER = 24
const HOVER_CLOSE_MS = 180

function useFineHover(): boolean {
  const [fine, setFine] = useState(() => window.matchMedia('(hover: hover) and (pointer: fine)').matches)
  useEffect(() => {
    const query = window.matchMedia('(hover: hover) and (pointer: fine)')
    const update = () => setFine(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])
  return fine
}

function labelOf(item: UserRequest, untitled: string): string {
  return item.title || untitled
}

export function RequestNav({
  items,
  activeId,
  onJump,
}: {
  items: UserRequest[]
  activeId: string | null
  onJump: (id: string) => void
}) {
  const { t } = useI18n()
  const hoverable = useFineHover()
  const panelId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const closeTimer = useRef(0)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const untitled = t('chat.requestUntitled')

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return items
    return items.filter(item => labelOf(item, untitled).toLowerCase().includes(q))
  }, [items, query, untitled])

  const showFilter = items.length >= FILTER_AFTER
  const virtualize = visible.length > VIRTUALIZE_AFTER
  const virtualizer = useVirtualizer({
    count: visible.length,
    getScrollElement: () => listRef.current,
    estimateSize: () => (window.matchMedia('(pointer: coarse)').matches ? 40 : 36),
    overscan: 8,
    enabled: virtualize && open,
    getItemKey: index => visible[index]?.id ?? index,
  })

  useEffect(() => () => window.clearTimeout(closeTimer.current), [])

  useEffect(() => {
    if (!open) {
      setQuery('')
      return
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        setOpen(false)
      }
    }
    const onPointer = (event: PointerEvent) => {
      if (rootRef.current?.contains(event.target as Node)) return
      setOpen(false)
    }
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('pointerdown', onPointer)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('pointerdown', onPointer)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const index = visible.findIndex(item => item.id === activeId)
    if (index < 0) return
    if (virtualize) {
      virtualizer.scrollToIndex(index, { align: 'auto' })
      return
    }
    const list = listRef.current
    const el = list?.querySelector<HTMLElement>('.req-nav-item.active')
    if (!list || !el) return
    // Why: scrollIntoView can also move the conversation scroller; keep the
    // highlight inside this panel without disturbing the chat.
    const top = el.offsetTop
    const bottom = top + el.offsetHeight
    if (top < list.scrollTop) list.scrollTop = top
    else if (bottom > list.scrollTop + list.clientHeight) list.scrollTop = bottom - list.clientHeight
  }, [open, activeId, visible, virtualize, virtualizer])

  if (items.length < 2) return null

  const current = items.find(item => item.id === activeId) ?? items[0]
  const currentLabel = labelOf(current, untitled)

  const cancelClose = () => window.clearTimeout(closeTimer.current)
  const scheduleClose = () => {
    if (!hoverable) return
    cancelClose()
    closeTimer.current = window.setTimeout(() => setOpen(false), HOVER_CLOSE_MS)
  }
  const openPanel = () => {
    cancelClose()
    setOpen(true)
  }

  const jump = (id: string) => {
    onJump(id)
    if (!hoverable) setOpen(false)
  }

  const renderItem = (item: UserRequest, style?: CSSProperties) => {
    const label = labelOf(item, untitled)
    const active = item.id === activeId
    return (
      <button
        key={item.id}
        type="button"
        role="option"
        className={`req-nav-item${active ? ' active' : ''}`}
        data-testid="request-nav-item"
        data-request-id={item.id}
        aria-selected={active}
        title={label}
        style={style}
        onClick={() => jump(item.id)}
      >
        {label}
      </button>
    )
  }

  return (
    <div
      ref={rootRef}
      className={`req-nav${open ? ' open' : ''}`}
      data-testid="request-nav"
      onMouseEnter={hoverable ? openPanel : undefined}
      onMouseLeave={hoverable ? scheduleClose : undefined}
    >
      {open ? (
        <div className="req-nav-panel" id={panelId} data-testid="request-nav-panel">
          {showFilter ? (
            <input
              className="req-nav-filter"
              data-testid="request-nav-filter"
              type="search"
              value={query}
              aria-label={t('chat.requestFilter')}
              placeholder={t('chat.requestFilterPlaceholder')}
              onChange={e => setQuery(e.target.value)}
            />
          ) : null}
          <div
            ref={listRef}
            className={`req-nav-list${virtualize ? ' virtual' : ''}`}
            role="listbox"
            aria-label={t('chat.requests')}
          >
            {visible.length === 0 ? (
              <div className="req-nav-empty">{t('chat.requestEmpty')}</div>
            ) : virtualize ? (
              <div className="req-nav-virtual" style={{ height: virtualizer.getTotalSize() }}>
                {virtualizer.getVirtualItems().map(row => {
                  const item = visible[row.index]
                  if (!item) return null
                  return renderItem(item, {
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    transform: `translateY(${row.start}px)`,
                  })
                })}
              </div>
            ) : visible.map(item => renderItem(item))}
          </div>
        </div>
      ) : null}
      <button
        type="button"
        className="req-nav-toggle"
        data-testid="request-nav-toggle"
        aria-label={open ? t('chat.requestsClose') : t('chat.requestsOpen')}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        title={currentLabel}
        onClick={e => {
          cancelClose()
          // Why: desktop hover already opens the panel on mouseenter; a
          // pointer click then would toggle it shut. Keyboard clicks have
          // detail 0 and still need to toggle.
          if (hoverable && e.detail > 0) {
            setOpen(true)
            return
          }
          setOpen(v => !v)
        }}
      >
        <IMenu />
      </button>
    </div>
  )
}
