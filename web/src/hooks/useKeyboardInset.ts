import { useEffect } from 'react'
import { fieldOutsideBand, keyboardInset } from '../lib/keyboard-inset'

function isEditable(element: Element | null): boolean {
  if (!(element instanceof HTMLElement)) return false
  if (element.isContentEditable) return true
  if (element instanceof HTMLTextAreaElement || element instanceof HTMLSelectElement) return true
  if (element instanceof HTMLInputElement) {
    return /^(text|search|url|tel|email|password|number|date|time|datetime-local|month|week)$/.test(element.type)
  }
  return false
}

export function useKeyboardInset(): void {
  useEffect(() => {
    const viewport = window.visualViewport
    if (!viewport) return
    const root = document.documentElement
    let frame = 0
    let baseline = window.innerHeight
    const timers = new Set<number>()
    const later = (ms: number) => {
      const id = window.setTimeout(() => {
        timers.delete(id)
        schedule()
      }, ms)
      timers.add(id)
    }
    const reveal = () => {
      const el = document.activeElement
      if (!(el instanceof HTMLElement) || !isEditable(el)) return
      if (viewport.scale > 1.05) return
      const rect = el.getBoundingClientRect()
      if (fieldOutsideBand(rect.top, rect.bottom, viewport.offsetTop, viewport.offsetTop + viewport.height)) {
        el.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      }
    }
    const apply = () => {
      frame = 0
      const editable = isEditable(document.activeElement)
      const inset = keyboardInset(
        {
          innerHeight: window.innerHeight,
          height: viewport.height,
          scale: viewport.scale,
        },
        editable,
      )
      root.style.setProperty('--keyboard-inset', `${inset}px`)
      baseline = Math.max(baseline, window.innerHeight)
      const keyboardOpen = editable && viewport.height < baseline * 0.75
      if (keyboardOpen) root.dataset.keyboard = 'open'
      else delete root.dataset.keyboard
      if (editable) reveal()
    }
    const schedule = () => {
      if (!frame) frame = window.requestAnimationFrame(apply)
    }
    const onFocusIn = () => {
      schedule()
      later(300)
      later(600)
    }
    const onFocusOut = () => later(0)
    viewport.addEventListener('resize', schedule)
    viewport.addEventListener('scroll', schedule)
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('focusout', onFocusOut)
    schedule()
    return () => {
      viewport.removeEventListener('resize', schedule)
      viewport.removeEventListener('scroll', schedule)
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('focusout', onFocusOut)
      for (const id of timers) window.clearTimeout(id)
      if (frame) window.cancelAnimationFrame(frame)
      root.style.removeProperty('--keyboard-inset')
      delete root.dataset.keyboard
    }
  }, [])
}
