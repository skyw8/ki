import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { IClose } from './icons'
import { useI18n } from './i18n'

export type ToastKind = 'info' | 'error'

export type ToastItem = {
  id: number
  kind: ToastKind
  text: string
	actionLabel?: string
	onAction?: () => void | Promise<void>
}

const MAX = 3
const INFO_MS = 3500

type Listener = (items: ToastItem[]) => void

const listeners = new Set<Listener>()
let items: ToastItem[] = []
let nextId = 1

function publish() {
  const snapshot = items.slice()
  listeners.forEach(fn => fn(snapshot))
}

function show(kind: ToastKind, text: string, actionLabel?: string, onAction?: () => void | Promise<void>): number {
  const msg = String(text ?? '').trim()
  if (!msg) return 0
  const id = nextId++
  items = [...items.filter(item => !(item.kind === kind && item.text === msg)), { id, kind, text: msg, actionLabel, onAction }].slice(-MAX)
  publish()
  return id
}

export const toast = Object.assign(show, {
  info: (text: string) => show('info', text),
  error: (text: string) => show('error', text),
	action: (kind: ToastKind, text: string, actionLabel: string, onAction: () => void | Promise<void>) => show(kind, text, actionLabel, onAction),
  from(err: unknown) {
    return show('error', err instanceof Error ? err.message : String(err))
  },
  dismiss(id: number) {
    const next = items.filter(item => item.id !== id)
    if (next.length === items.length) return
    items = next
    publish()
  },
})

export function subscribeToasts(fn: Listener): () => void {
  listeners.add(fn)
  fn(items.slice())
  return () => { listeners.delete(fn) }
}

function ToastCard({ item, closeLabel }: { item: ToastItem; closeLabel: string }) {
  const remain = useRef(INFO_MS)
  const started = useRef(0)
  const timer = useRef(0)
  const close = () => toast.dismiss(item.id)

  useEffect(() => {
    if (item.kind !== 'info' || item.actionLabel) return
    const arm = (ms: number) => {
      started.current = Date.now()
      timer.current = window.setTimeout(close, ms)
    }
    arm(remain.current)
    return () => window.clearTimeout(timer.current)
  }, [item.id, item.kind]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div
      className={`toast ${item.kind}`}
      data-testid="toast"
      data-kind={item.kind}
      role={item.kind === 'error' ? 'alert' : 'status'}
      onMouseEnter={() => {
        if (item.kind !== 'info' || !timer.current) return
        window.clearTimeout(timer.current)
        timer.current = 0
        remain.current = Math.max(0, remain.current - (Date.now() - started.current))
      }}
      onMouseLeave={() => {
        if (item.kind !== 'info' || timer.current) return
        started.current = Date.now()
        timer.current = window.setTimeout(close, remain.current)
      }}
    >
      <span className="toast-text">{item.text}</span>
	  {item.actionLabel ? <button type="button" className="toast-action" onClick={() => { close(); void item.onAction?.() }}>{item.actionLabel}</button> : null}
      <button type="button" className="toast-close" aria-label={closeLabel} onClick={close}><IClose /></button>
    </div>
  )
}

export function Toaster() {
  const { t } = useI18n()
  const [list, setList] = useState<ToastItem[]>([])
  useEffect(() => subscribeToasts(setList), [])
  if (!list.length) return null
  return createPortal(
    <div className="toaster" data-testid="toaster">
      {[...list].reverse().map(item => (
        <ToastCard key={item.id} item={item} closeLabel={t('close')} />
      ))}
    </div>,
    document.body,
  )
}
