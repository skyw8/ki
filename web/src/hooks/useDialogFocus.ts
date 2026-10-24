import { type RefObject, useLayoutEffect, useRef } from 'react'

type DialogFocusOptions = {
  open: boolean
  onEscape: () => void
  initialFocusRef?: RefObject<HTMLElement>
  restoreFocusRef?: RefObject<HTMLElement>
}

type DialogEntry = {
  element: HTMLElement
  initialFocus: () => HTMLElement | null
  onEscape: () => void
  restoreFocus: HTMLElement | null
  focusFrame: number
  isolation: AttributeSnapshot | null
}

type AttributeSnapshot = {
  ariaHidden: string | null
  inert: string | null
}

const focusableSelector = [
  'a[href]',
  'button:not(:disabled)',
  'input:not(:disabled):not([type="hidden"])',
  'select:not(:disabled)',
  'textarea:not(:disabled)',
  '[contenteditable="true"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

const dialogStack: DialogEntry[] = []
let scrollLockCount = 0
let bodyOverflow = ''

function isHidden(element: HTMLElement): boolean {
  let current: HTMLElement | null = element
  while (current) {
    if (
      current.hidden
      || current.getAttribute('aria-hidden') === 'true'
      || current.hasAttribute('inert')
      || current.style.display === 'none'
      || current.style.visibility === 'hidden'
    ) return true
    const style = window.getComputedStyle(current)
    if (style.display === 'none' || style.visibility === 'hidden') return true
    current = current.parentElement
  }
  return false
}

function focusableElements(dialog: HTMLElement): HTMLElement[] {
  return Array.from(dialog.querySelectorAll<HTMLElement>(focusableSelector))
    .filter(element => element.tabIndex >= 0 && !isHidden(element) && !element.closest('fieldset:disabled'))
}

function focusEntry(entry: DialogEntry): void {
  if (dialogStack.at(-1) !== entry || !entry.element.isConnected) return
  const initial = entry.initialFocus()
  const target = initial && entry.element.contains(initial)
    ? initial
    : entry.element.querySelector<HTMLElement>('[autofocus]') ?? focusableElements(entry.element)[0] ?? entry.element
  target.focus({ preventScroll: true })
}

function consume(event: KeyboardEvent): void {
  event.preventDefault()
  event.stopPropagation()
  event.stopImmediatePropagation()
}

function onDocumentKeyDown(event: KeyboardEvent): void {
  const entry = dialogStack.at(-1)
  if (!entry) return

  if (event.key === 'Escape') {
    if (event.isComposing || event.keyCode === 229) return
    consume(event)
    entry.onEscape()
    return
  }
  if (event.key !== 'Tab') return

  event.stopPropagation()
  event.stopImmediatePropagation()
  const focusable = focusableElements(entry.element)
  if (!focusable.length) {
    event.preventDefault()
    entry.element.focus({ preventScroll: true })
    return
  }
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const active = document.activeElement
  if (!entry.element.contains(active) || active === entry.element) {
    event.preventDefault()
    ;(event.shiftKey ? last : first).focus({ preventScroll: true })
  } else if (event.shiftKey && active === first) {
    event.preventDefault()
    last.focus({ preventScroll: true })
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus({ preventScroll: true })
  }
}

function lockBodyScroll(): void {
  if (scrollLockCount++ > 0) return
  bodyOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
}

function unlockBodyScroll(): void {
  if (scrollLockCount === 0 || --scrollLockCount > 0) return
  document.body.style.overflow = bodyOverflow
}

function restoreAttribute(element: HTMLElement, name: string, value: string | null): void {
  if (value === null) element.removeAttribute(name)
  else element.setAttribute(name, value)
}

function restoreIsolation(entry: DialogEntry): void {
  if (!entry.isolation) return
  restoreAttribute(entry.element, 'aria-hidden', entry.isolation.ariaHidden)
  restoreAttribute(entry.element, 'inert', entry.isolation.inert)
  entry.isolation = null
}

function syncDialogIsolation(): void {
  const topIndex = dialogStack.length - 1
  for (const [index, entry] of dialogStack.entries()) {
    if (index === topIndex) {
      restoreIsolation(entry)
      continue
    }
    if (!entry.isolation) {
      entry.isolation = {
        ariaHidden: entry.element.getAttribute('aria-hidden'),
        inert: entry.element.getAttribute('inert'),
      }
    }
    entry.element.setAttribute('aria-hidden', 'true')
    entry.element.setAttribute('inert', '')
  }
}

function register(entry: DialogEntry): void {
  const hasUnderlyingDialog = dialogStack.length > 0
  if (!hasUnderlyingDialog) document.addEventListener('keydown', onDocumentKeyDown)
  dialogStack.push(entry)
  lockBodyScroll()

  if (hasUnderlyingDialog) focusEntry(entry)
  syncDialogIsolation()
  entry.focusFrame = window.requestAnimationFrame(() => {
    if (!entry.element.contains(document.activeElement)) focusEntry(entry)
  })
}

function unregister(entry: DialogEntry): void {
  window.cancelAnimationFrame(entry.focusFrame)
  const index = dialogStack.indexOf(entry)
  if (index === -1) return
  const wasTop = index === dialogStack.length - 1
  dialogStack.splice(index, 1)

  restoreIsolation(entry)
  syncDialogIsolation()

  for (const remaining of dialogStack.slice(index)) {
    if (!remaining.restoreFocus || entry.element.contains(remaining.restoreFocus)) {
      remaining.restoreFocus = entry.restoreFocus
    }
  }

  unlockBodyScroll()
  if (!dialogStack.length) document.removeEventListener('keydown', onDocumentKeyDown)
  if (!wasTop) return

  window.requestAnimationFrame(() => {
    const top = dialogStack.at(-1)
    if (top) {
      const restore = entry.restoreFocus
      if (restore?.isConnected && top.element.contains(restore)) {
        restore.focus({ preventScroll: true })
      } else if (!top.element.contains(document.activeElement)) {
        focusEntry(top)
      }
      return
    }
    if (entry.restoreFocus?.isConnected) entry.restoreFocus.focus({ preventScroll: true })
  })
}

export function useDialogFocus<T extends HTMLElement>({
  open,
  onEscape,
  initialFocusRef,
  restoreFocusRef,
}: DialogFocusOptions): RefObject<T> {
  const dialogRef = useRef<T>(null)
  const escapeRef = useRef(onEscape)
  const initialRef = useRef(initialFocusRef)
  const restoreRef = useRef(restoreFocusRef)
  escapeRef.current = onEscape
  initialRef.current = initialFocusRef
  restoreRef.current = restoreFocusRef

  useLayoutEffect(() => {
    const element = dialogRef.current
    if (!open || !element) return
    const entry: DialogEntry = {
      element,
      initialFocus: () => initialRef.current?.current ?? null,
      onEscape: () => escapeRef.current(),
      restoreFocus: restoreRef.current?.current ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null),
      focusFrame: 0,
      isolation: null,
    }
    register(entry)
    return () => unregister(entry)
  }, [open])

  return dialogRef
}
