import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { ICheck, IChevDown } from './icons'

export type SelectOption = { value: string; label: string; disabled?: boolean }

type Props = {
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  ariaLabel: string
  className?: string
  testid?: string
  disabled?: boolean
}

export function Select({ value, options, onChange, ariaLabel, className = '', testid, disabled }: Props) {
  const id = useId()
  const trigger = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const selectedIndex = Math.max(0, options.findIndex(option => option.value === value))
  const [active, setActive] = useState(selectedIndex)
  const [position, setPosition] = useState<CSSProperties>({})
  const selected = options[selectedIndex]

  const place = () => {
    const rect = trigger.current?.getBoundingClientRect()
    if (!rect) return
    const gap = 6
    const menuWidth = Math.max(112, rect.width)
    const below = window.innerHeight - rect.bottom - gap - 8
    const above = rect.top - gap - 8
    const upward = below < 180 && above > below
    setPosition({
      left: Math.max(8, Math.min(rect.left, window.innerWidth - menuWidth - 8)),
      width: menuWidth,
      maxHeight: Math.max(96, Math.min(240, upward ? above : below)),
      ...(upward ? { bottom: window.innerHeight - rect.top + gap } : { top: rect.bottom + gap }),
    })
  }

  useLayoutEffect(() => { if (open) place() }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) return
    const closeOutside = (event: PointerEvent) => {
      const node = event.target as Node
      if (!trigger.current?.contains(node) && !menu.current?.contains(node)) setOpen(false)
    }
    const closeOnViewportChange = (event: Event) => {
      // Why: the capture listener also receives scroll events from this
      // portaled menu; closing here prevents its own scrollbar from moving.
      if (event.target instanceof Node && menu.current?.contains(event.target)) return
      setOpen(false)
    }
    document.addEventListener('pointerdown', closeOutside)
    window.addEventListener('resize', closeOnViewportChange)
    window.addEventListener('scroll', closeOnViewportChange, true)
    return () => {
      document.removeEventListener('pointerdown', closeOutside)
      window.removeEventListener('resize', closeOnViewportChange)
      window.removeEventListener('scroll', closeOnViewportChange, true)
    }
  }, [open])

  const move = (direction: 1 | -1) => {
    if (!options.length) return
    let next = active
    do { next = (next + direction + options.length) % options.length } while (options[next].disabled && next !== active)
    setActive(next)
  }
  const choose = (index: number) => {
    const option = options[index]
    if (!option || option.disabled) return
    onChange(option.value)
    setActive(index)
    setOpen(false)
    trigger.current?.focus()
  }
  const show = () => {
    if (disabled) return
    setActive(selectedIndex)
    setOpen(true)
  }

  return <span className={`ds-select${open ? ' open' : ''}${className ? ` ${className}` : ''}`}>
    <button
      ref={trigger}
      type="button"
      className="ds-select-trigger"
      role="combobox"
      aria-label={ariaLabel}
      aria-expanded={open}
      aria-controls={`${id}-listbox`}
      aria-haspopup="listbox"
      aria-activedescendant={open ? `${id}-option-${active}` : undefined}
      data-testid={testid}
      disabled={disabled}
      onClick={() => open ? setOpen(false) : show()}
      onKeyDown={event => {
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault()
          if (!open) show()
          else move(event.key === 'ArrowDown' ? 1 : -1)
        } else if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          if (open) choose(active)
          else show()
        } else if (event.key === 'Escape' && open) {
          event.preventDefault()
          event.stopPropagation()
          setOpen(false)
        } else if (event.key === 'Home' && open) {
          event.preventDefault(); setActive(0)
        } else if (event.key === 'End' && open) {
          event.preventDefault(); setActive(options.length - 1)
        } else if (event.key === 'Tab') setOpen(false)
      }}
    >
      <span className="ds-select-value">{selected?.label ?? value}</span>
      <IChevDown className="ds-select-chevron" aria-hidden />
    </button>
    {open ? createPortal(
      <div ref={menu} id={`${id}-listbox`} className="ds-select-menu" role="listbox" aria-label={ariaLabel} style={position}>
        {options.map((option, index) => <div
          id={`${id}-option-${index}`}
          key={option.value}
          className={`ds-select-option${index === active ? ' active' : ''}${option.value === value ? ' selected' : ''}`}
          role="option"
          aria-selected={option.value === value}
          aria-disabled={option.disabled || undefined}
          onMouseEnter={() => { if (!option.disabled) setActive(index) }}
          onMouseDown={event => event.preventDefault()}
          onClick={() => choose(index)}
        >
          <span>{option.label}</span><span className="ds-select-check">{option.value === value ? <ICheck /> : null}</span>
        </div>)}
      </div>,
      document.body,
    ) : null}
  </span>
}
