import { useEffect } from 'react'
import { fieldOutsideBand, keyboardInset } from '../lib/keyboard-inset'

function isEditable(element: Element | null): boolean {
  if (!(element instanceof HTMLElement)) return false
  if (element.isContentEditable) return true
  if (element instanceof HTMLTextAreaElement || element instanceof HTMLSelectElement) return true
  if (element instanceof HTMLInputElement) {
    // Only text-like inputs summon the software keyboard.
    return /^(text|search|url|tel|email|password|number|date|time|datetime-local|month|week)$/.test(element.type)
  }
  return false
}

/**
 * Publishes the software-keyboard occlusion as `--keyboard-inset` on :root so
 * CSS can lift the bottom composer and shrink full-screen dialogs above it.
 * Stays 0 wherever the layout viewport already resizes (Chrome's
 * resizes-content) or no editable control is focused.
 *
 * Also keeps the focused field inside the visible band above the keyboard.
 * The browser's own reveal loses against our fixed-height shell and nested
 * scrollers (modal bodies, the transcript): it pans the wrong scroller or
 * aligns against a band that is still animating. Revealing on every viewport
 * resize (not once) converges as the keyboard finishes its animation; inner
 * scrollers never emit visualViewport events, so a user scrolling away from a
 * focused field is never yanked back.
 */
export function useKeyboardInset(): void {
  useEffect(() => {
    const viewport = window.visualViewport
    if (!viewport) return
    const root = document.documentElement
    let frame = 0
    // Largest layout height seen this session: the no-keyboard reference. The
    // visible band dropping under three quarters of it means the software
    // keyboard is open even where innerHeight itself shrank (resizes-content),
    // so this works on both Android Chrome and iOS Safari.
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
      // data-keyboard lets CSS reclaim dialog chrome (page titles, section
      // headers) while typing: on a phone the band above the keyboard is a
      // few hundred px tall and the form needs it more than the headings do.
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
      // Keyboard animations run ~300ms; recheck after they settle so the
      // reveal lands against the final band, not a mid-animation one.
      later(300)
      later(600)
    }
    // focusout fires before the next field's focusin; defer so a focus move
    // between two inputs never flashes the inset to 0.
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
