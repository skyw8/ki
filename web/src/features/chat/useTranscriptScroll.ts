import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'
import type { Virtualizer } from '@tanstack/react-virtual'

export type TranscriptScroll = {
  read: () => void
  latest: () => void
}

/** Whether an inner scroller consumes this gesture before the transcript. */
function innerScrolls(target: EventTarget | null, root: HTMLElement, delta: number): boolean {
  let el = target instanceof Element ? target : null
  while (el && el !== root) {
    if (el instanceof HTMLElement && el.scrollHeight > el.clientHeight + 1 && /auto|scroll/.test(getComputedStyle(el).overflowY)) {
      if (delta < 0 ? el.scrollTop > 0 : el.scrollTop + el.clientHeight < el.scrollHeight - 1) return true
    }
    el = el.parentElement
  }
  return false
}

/** One owner for intent, pagination triggers and explicit scroll operations. */
export function useTranscriptScroll(options: {
  scrollRef: RefObject<HTMLDivElement | null>
  onAtBottom?: (bottom: boolean) => void
  onReadIntent?: () => void
  onLoadOlder?: () => Promise<unknown>
  hasMore?: boolean
  loadingOlder?: boolean
  olderError?: boolean
}) {
  const state = useRef(options)
  state.current = options
  const virtual = useRef<Virtualizer<HTMLDivElement, Element> | null>(null)
  const following = useRef(true)
  const [isFollowing, setFollowing] = useState(true)
  const budget = useRef(0)
  const pending = useRef(false)
  const frame = useRef(0)
  const dragging = useRef(false)
  const direction = useRef(0)
  const active = useRef(false)
  const setIntent = useCallback((follow: boolean) => {
    following.current = follow
    setFollowing(follow)
    const v = virtual.current
    if (v) {
      // virtual-core 3.17.10 also pins on resize, independently of
      // followOnAppend. Distances are nonnegative: -1 disables both paths
      // immediately, before React renders after the upward gesture.
      v.options.followOnAppend = follow
      v.options.scrollEndThreshold = follow ? 80 : -1
    }
  }, [])
  const read = useCallback(() => setIntent(false), [setIntent])
  const latest = useCallback(() => {
    direction.current = 0
    budget.current = 0
    setIntent(true)
    virtual.current?.scrollToEnd()
    state.current.onAtBottom?.(true)
  }, [setIntent])

  const checkOlder = useCallback(() => {
    const s = state.current
    const el = s.scrollRef.current
    if (!el || following.current || pending.current || s.loadingOlder || s.olderError || !s.hasMore || budget.current <= 0 || !s.onLoadOlder) return
    if (Math.max(0, el.scrollTop) > Math.max(160, el.clientHeight)) return
    pending.current = true
    budget.current--
    void s.onLoadOlder().finally(() => {
      pending.current = false
      // Recheck after the committed page even if there is still no overflow.
      if (active.current) frame.current = requestAnimationFrame(() => checkOlder())
    })
  }, [])
  const loadOlder = useCallback(() => {
    state.current.onReadIntent?.()
    setIntent(false)
    budget.current = 1
    // A retry deliberately bypasses the automatic error gate.
    void state.current.onLoadOlder?.()
  }, [setIntent])

  useEffect(() => {
    const el = options.scrollRef.current
    if (!el) return
    active.current = true
    let touchY: number | null = null
    let lastTop = el.scrollTop
    let settleTimer = 0
    const intent = (delta: number, target: EventTarget | null) => {
      if (!delta || innerScrolls(target, el, delta)) return
      direction.current = Math.sign(delta)
      state.current.onReadIntent?.()
      if (delta < 0) {
        setIntent(false)
        budget.current = 2
        checkOlder()
      }
    }
    const wheel = (e: WheelEvent) => { if (Math.abs(e.deltaY) >= Math.abs(e.deltaX)) intent(e.deltaY, e.target) }
    const touchStart = (e: TouchEvent) => { touchY = e.touches[0]?.clientY ?? null }
    const touchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY
      if (y != null && touchY != null) intent(touchY - y, e.target)
      touchY = y ?? null
    }
    const touchEnd = () => { touchY = null }
    const key = (e: KeyboardEvent) => {
      if (e.defaultPrevented || (e.target instanceof Element && e.target.closest('input,textarea,select,button,[contenteditable="true"]'))) return
      if (['ArrowUp', 'PageUp', 'Home'].includes(e.key) || (e.key === ' ' && e.shiftKey)) intent(-1, e.target)
      if (['ArrowDown', 'PageDown', 'End'].includes(e.key) || (e.key === ' ' && !e.shiftKey)) intent(1, e.target)
    }
    const down = (e: PointerEvent) => {
      // A scrollbar drag has no wheel/touch direction. Ordinary clicks and
      // selection inside a message must not change the reader's intent.
      dragging.current = e.target === el && e.pointerType === 'mouse'
    }
    const up = () => { dragging.current = false }
    const finish = () => {
      if (touchY != null || dragging.current) return
      const gap = Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop)
      if (direction.current > 0 && gap <= 8) setIntent(true)
      direction.current = 0
    }
    const scroll = () => {
      if (dragging.current && el.scrollTop !== lastTop) intent(el.scrollTop - lastTop, el)
      lastTop = el.scrollTop
      state.current.onAtBottom?.(Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop) <= 8)
      checkOlder()
      window.clearTimeout(settleTimer)
      settleTimer = window.setTimeout(finish, 180)
    }
    el.addEventListener('wheel', wheel, { passive: true, capture: true })
    el.addEventListener('touchstart', touchStart, { passive: true })
    el.addEventListener('touchmove', touchMove, { passive: true, capture: true })
    el.addEventListener('touchend', touchEnd, { passive: true })
    el.addEventListener('touchcancel', touchEnd, { passive: true })
    el.addEventListener('keydown', key)
    el.addEventListener('pointerdown', down)
    window.addEventListener('pointerup', up)
    el.addEventListener('scroll', scroll, { passive: true })
    el.addEventListener('scrollend', finish)
    const observer = new ResizeObserver(checkOlder)
    observer.observe(el)
    return () => {
      active.current = false
      observer.disconnect()
      window.clearTimeout(settleTimer)
      cancelAnimationFrame(frame.current)
      el.removeEventListener('wheel', wheel, true)
      el.removeEventListener('touchstart', touchStart)
      el.removeEventListener('touchmove', touchMove, true)
      el.removeEventListener('touchend', touchEnd)
      el.removeEventListener('touchcancel', touchEnd)
      el.removeEventListener('keydown', key)
      el.removeEventListener('pointerdown', down)
      window.removeEventListener('pointerup', up)
      el.removeEventListener('scroll', scroll)
      el.removeEventListener('scrollend', finish)
    }
  }, [options.scrollRef, checkOlder, setIntent])

  return { virtual, following, isFollowing, read, latest, loadOlder }
}
