import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { flushSync } from 'react-dom'
import { elementScroll, type Virtualizer, type VirtualizerOptions } from '@tanstack/react-virtual'

export type TranscriptScroll = {
  read: () => void
  latest: () => void
  preparePrepend: (signal: AbortSignal) => Promise<void>
}

export type TranscriptIntent = 'following' | 'reading' | 'seeking'

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

export function useTranscriptScroll(options: {
  scrollRef: RefObject<HTMLDivElement | null>
  onAtBottom?: (bottom: boolean) => void
  onReadIntent?: () => void
  onLoadOlder?: () => Promise<unknown>
  hasMore?: boolean
  loadingOlder?: boolean
  olderError?: boolean
  pageBudget?: number
  endPadding?: number
  initialIntent?: TranscriptIntent
}) {
  const state = useRef(options)
  state.current = options
  const virtual = useRef<Virtualizer<HTMLDivElement, Element> | null>(null)
  const intent = useRef<TranscriptIntent>(options.initialIntent ?? 'following')
  const [scrollIntent, setScrollIntent] = useState<TranscriptIntent>(options.initialIntent ?? 'following')
  const following = useMemo(() => ({ get current() { return intent.current === 'following' } }), [])
  useLayoutEffect(() => {
    let current = true
    queueMicrotask(() => {
      if (current && intent.current === 'following') virtual.current?.scrollToEnd()
    })
    return () => { current = false }
  }, [options.endPadding])
  const budget = useRef(0)
  const pending = useRef(false)
  const frame = useRef(0)
  const dragging = useRef(false)
  const direction = useRef(0)
  const active = useRef(false)
  const touching = useRef(false)
  const lastMotion = useRef(0)
  const userScrolling = useRef(false)
  const seekFrame = useRef(0)
  const wheelMotion = useRef(false)
  const touchMotion = useRef(false)
  const deferredOffset = useRef(0)
  const [geometryOffset, setGeometryOffset] = useState(0)
  const geometryQueued = useRef(false)
  const settleGeometry = useRef<() => void>(() => {})
  const iosWebKit = typeof navigator !== 'undefined' &&
    (/iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1))
  const desktopWebKit = typeof navigator !== 'undefined' &&
    /AppleWebKit/.test(navigator.userAgent) && !/Chrome|Chromium|CriOS|Edg|OPR|iPhone|iPad|iPod/.test(navigator.userAgent) &&
    !(navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  const clearDeferredOffset = useCallback(() => {
    const offset = deferredOffset.current
    deferredOffset.current = 0
    if (offset) setGeometryOffset(0)
    return offset
  }, [])
  const observeOffset = useCallback((offset: number) => {
    if (offset <= 0 && deferredOffset.current) clearDeferredOffset()
    return offset + deferredOffset.current
  }, [clearDeferredOffset])
  const deferResize = useCallback((delta: number, instance: Virtualizer<HTMLDivElement, Element>) => {
    const el = instance.scrollElement
    if (!el || !iosWebKit || intent.current !== 'reading' || !touchMotion.current || !userScrolling.current) return false
    const logical = Math.max(0, (instance.scrollOffset ?? el.scrollTop + deferredOffset.current) + delta)
    deferredOffset.current = logical - el.scrollTop
    instance.scrollOffset = logical
    setGeometryOffset(deferredOffset.current)
    if (!geometryQueued.current) {
      geometryQueued.current = true
      queueMicrotask(() => {
        geometryQueued.current = false
        if (active.current) flushSync(() => setGeometryOffset(deferredOffset.current))
      })
    }
    return true
  }, [iosWebKit])
  const scrollToFn = useCallback<VirtualizerOptions<HTMLDivElement, Element>['scrollToFn']>((offset, options, instance) => {
    const el = instance.scrollElement
    if (el && wheelMotion.current && userScrolling.current && direction.current < 0 && options.adjustments) {
      deferredOffset.current = offset + options.adjustments - el.scrollTop
      setGeometryOffset(deferredOffset.current)
      return
    }
    const physical = offset - deferredOffset.current
    if (el && deferredOffset.current && wheelMotion.current && userScrolling.current && Math.abs(physical + (options.adjustments ?? 0) - el.scrollTop) < .5) return
    elementScroll(physical, options, instance)
  }, [])
  const preparePrepend = useCallback((signal: AbortSignal): Promise<void> => new Promise(resolve => {
    let frame = 0
    const finish = () => {
      cancelAnimationFrame(frame)
      signal.removeEventListener('abort', finish)
      if (!signal.aborted) {
        userScrolling.current = false
        if (active.current) settleGeometry.current()
      }
      resolve()
    }
    const check = () => {
      if (signal.aborted || !active.current || (!touching.current && !virtual.current?.isScrolling && performance.now() - lastMotion.current >= 180)) finish()
      else frame = requestAnimationFrame(check)
    }
    signal.addEventListener('abort', finish, { once: true })
    check()
  }), [])
  const setIntent = useCallback((next: TranscriptIntent) => {
    intent.current = next
    setScrollIntent(next)
    if (next !== 'seeking') cancelAnimationFrame(seekFrame.current)
    const follow = next === 'following'
    const v = virtual.current
    if (v) {
      v.options.followOnAppend = follow
      v.options.scrollEndThreshold = follow ? 80 : -1
    }
  }, [])
  const read = useCallback(() => {
    direction.current = 0
    budget.current = 0
    userScrolling.current = false
    wheelMotion.current = false
    touchMotion.current = false
    setIntent('reading')
    const el = state.current.scrollRef.current
    settleGeometry.current()
    if (el) virtual.current?.scrollToOffset(el.scrollTop)
  }, [setIntent])
  const latest = useCallback(() => {
    direction.current = 0
    budget.current = 0
    userScrolling.current = false
    wheelMotion.current = false
    touchMotion.current = false
    settleGeometry.current()
    setIntent('following')
    virtual.current?.scrollToEnd()
    state.current.onAtBottom?.(true)
  }, [setIntent])
  settleGeometry.current = () => {
    const el = state.current.scrollRef.current
    if (!el || !deferredOffset.current) return
    const target = el.scrollTop + deferredOffset.current
    if (deferredOffset.current < 0) {
      flushSync(() => {
        clearDeferredOffset()
        virtual.current?.scrollToOffset(target)
      })
    } else {
      flushSync(clearDeferredOffset)
      virtual.current?.scrollToOffset(target)
    }
  }
  const seek = useCallback((id: string, index: number, onSettled?: () => void) => {
    read()
    setIntent('seeking')
    const v = virtual.current
    v?.scrollToIndex(index, { align: 'start' })
    let stable = 0
    const settle = () => {
      const el = state.current.scrollRef.current
      if (!el || intent.current !== 'seeking') return
      const row = el.querySelector<HTMLElement>(`[data-item-key="${CSS.escape(id)}"]`)
      const offset = row ? row.getBoundingClientRect().top - el.getBoundingClientRect().top : Infinity
      const remaining = Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop)
      if (row && Math.abs(Math.min(offset, remaining)) <= 1) stable++
      else {
        stable = 0
        v?.scrollToIndex(index, { align: 'start' })
      }
      if (stable >= 2) {
        setIntent('reading')
        onSettled?.()
      } else seekFrame.current = requestAnimationFrame(settle)
    }
    seekFrame.current = requestAnimationFrame(settle)
    return () => cancelAnimationFrame(seekFrame.current)
  }, [read, setIntent])

  const checkOlder = useCallback(() => {
    const s = state.current
    const el = s.scrollRef.current
    if (!el || following.current || pending.current || s.loadingOlder || s.olderError || !s.hasMore || budget.current <= 0 || !s.onLoadOlder) return
    if (Math.max(0, el.scrollTop + deferredOffset.current) > Math.max(160, el.clientHeight)) return
    pending.current = true
    budget.current--
    void s.onLoadOlder().finally(() => {
      pending.current = false
      if (active.current) frame.current = requestAnimationFrame(() => checkOlder())
    })
  }, [])
  const loadOlder = useCallback(() => {
    state.current.onReadIntent?.()
    read()
    budget.current = 0
    void state.current.onLoadOlder?.()
  }, [read])

  useEffect(() => {
    const el = options.scrollRef.current
    if (!el) return
    active.current = true
    let touchY: number | null = null
    let lastTop = el.scrollTop
    let settleTimer = 0
    const gesture = (delta: number, target: EventTarget | null, nativeMotion = true) => {
      if (!delta || innerScrolls(target, el, delta)) return
      if (intent.current === 'seeking' || (delta < 0 && following.current)) read()
      userScrolling.current = nativeMotion
      direction.current = Math.sign(delta)
      state.current.onReadIntent?.()
      if (delta < 0) {
        setIntent('reading')
        if (!pending.current && !state.current.loadingOlder) budget.current = state.current.pageBudget ?? 2
        checkOlder()
      }
    }
    const wheel = (e: WheelEvent) => {
      if (Math.abs(e.deltaY) < Math.abs(e.deltaX)) return
      if (e.deltaY > 0) settleGeometry.current()
      gesture(e.deltaY, e.target, e.isTrusted)
      wheelMotion.current = desktopWebKit && e.isTrusted
      if (wheelMotion.current) lastMotion.current = performance.now()
    }
    const touchStart = (e: TouchEvent) => {
      settleGeometry.current()
      wheelMotion.current = false
      touchMotion.current = iosWebKit
      touching.current = true; userScrolling.current = true; touchY = e.touches[0]?.clientY ?? null
    }
    const touchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY
      if (y != null && touchY != null) {
        gesture(touchY - y, e.target)
        touchMotion.current = iosWebKit
      }
      touchY = y ?? null
    }
    const touchEnd = () => {
      touching.current = false; lastMotion.current = performance.now(); touchY = null
      window.clearTimeout(settleTimer)
      settleTimer = window.setTimeout(finish, 180)
    }
    const key = (e: KeyboardEvent) => {
      if (e.defaultPrevented || (e.target instanceof Element && e.target.closest('input,textarea,select,button,[contenteditable="true"]'))) return
      settleGeometry.current()
      wheelMotion.current = false
      touchMotion.current = false
      if (['ArrowUp', 'PageUp', 'Home'].includes(e.key) || (e.key === ' ' && e.shiftKey)) gesture(-1, e.target)
      if (['ArrowDown', 'PageDown', 'End'].includes(e.key) || (e.key === ' ' && !e.shiftKey)) gesture(1, e.target)
    }
    const down = (e: PointerEvent) => {
      if (e.pointerType === 'mouse') {
        settleGeometry.current()
        wheelMotion.current = false
        touchMotion.current = false
      }
      dragging.current = e.target === el && e.pointerType === 'mouse'
    }
    const up = () => { dragging.current = false }
    const finish = () => {
      if (touchY != null || dragging.current) return
      const quietFor = performance.now() - lastMotion.current
      if ((wheelMotion.current || touchMotion.current) && userScrolling.current && quietFor < 180) {
        window.clearTimeout(settleTimer)
        settleTimer = window.setTimeout(finish, Math.ceil(180 - quietFor))
        return
      }
      userScrolling.current = false
      wheelMotion.current = false
      touchMotion.current = false
      settleGeometry.current()
      const gap = Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop)
      if (direction.current > 0 && gap <= 8) setIntent('following')
      direction.current = 0
    }
    const scroll = () => {
      lastMotion.current = performance.now()
      if (dragging.current && el.scrollTop !== lastTop) gesture(el.scrollTop - lastTop, el)
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
    let height = el.clientHeight
    const observer = new ResizeObserver(() => {
      if (height !== el.clientHeight) {
        height = el.clientHeight
        if (following.current) virtual.current?.scrollToEnd()
      }
      checkOlder()
    })
    observer.observe(el)
    return () => {
      active.current = false
      touching.current = false
      userScrolling.current = false
      wheelMotion.current = false
      touchMotion.current = false
      observer.disconnect()
      window.clearTimeout(settleTimer)
      cancelAnimationFrame(frame.current)
      cancelAnimationFrame(seekFrame.current)
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
  }, [options.scrollRef, checkOlder, setIntent, read, following, desktopWebKit, iosWebKit])

  return { virtual, following, isFollowing: scrollIntent === 'following', scrollIntent, read, latest, seek, loadOlder, preparePrepend, userScrolling, geometryOffset, observeOffset, scrollToFn, deferResize }
}
