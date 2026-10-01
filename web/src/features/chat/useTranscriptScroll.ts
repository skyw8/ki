import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { flushSync } from 'react-dom'
import { elementScroll, type Virtualizer, type VirtualizerOptions } from '@tanstack/react-virtual'

export type TranscriptScroll = {
  read: () => void
  latest: () => void
  preparePrepend: (signal: AbortSignal) => Promise<void>
}

export type TranscriptIntent = 'following' | 'reading' | 'seeking'

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
  pageBudget?: number
  endPadding?: number
  initialIntent?: TranscriptIntent
}) {
  const state = useRef(options)
  state.current = options
  const virtual = useRef<Virtualizer<HTMLDivElement, Element> | null>(null)
  const intent = useRef<TranscriptIntent>(options.initialIntent ?? 'following')
  const [scrollIntent, setScrollIntent] = useState<TranscriptIntent>(options.initialIntent ?? 'following')
  // Compatibility with the virtualizer's boolean API is a projection, not a
  // second intent owner. Seeking and reading both disable all end pinning.
  const following = useMemo(() => ({ get current() { return intent.current === 'following' } }), [])
  useLayoutEffect(() => {
    // A terminal message can add/remove the running placeholder by changing
    // paddingEnd, outside resizeItem's measured delta. Reconcile that extent
    // in the same paint (otherwise a persistent 36px tail gap remains), but
    // never re-arm a reader or replace an in-flight request-navigation target.
    // Row layout effects can still publish a new sizer in this commit. One
    // microtask sees that committed extent; pinning earlier is clamped to the
    // old height and the library's next-frame retry exposes a 36px flash.
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
    // Reaching the native start consumes any remaining upward motion. There
    // is no above-root anchor to compensate, so expose the actual first row.
    if (offset <= 0 && deferredOffset.current) clearDeferredOffset()
    return offset + deferredOffset.current
  }, [clearDeferredOffset])
  const deferResize = useCallback((delta: number, instance: Virtualizer<HTMLDivElement, Element>) => {
    const el = instance.scrollElement
    if (!el || !iosWebKit || intent.current !== 'reading' || !touchMotion.current || !userScrolling.current) return false
    // iOS virtual-core queues its private correction before scrollToFn runs,
    // but publishes the resized transforms immediately. Consume the public
    // resize predicate here instead: the SAME visual debt used by wheel owns
    // every delta, and the public logical offset keeps the visible range on
    // the reader. No library compensation is queued for an already-owned delta.
    const logical = Math.max(0, (instance.scrollOffset ?? el.scrollTop + deferredOffset.current) + delta)
    deferredOffset.current = logical - el.scrollTop
    instance.scrollOffset = logical
    // A ref measurement can be inside an outer flushSync range commit: queue
    // the offset now so that commit cannot shrink the sizer before our flush.
    setGeometryOffset(deferredOffset.current)
    if (!geometryQueued.current) {
      geometryQueued.current = true
      // The predicate precedes resizeItem's cache commit. Flush after that
      // stack (including all ref measurements), still before this paint.
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
      // Desktop WebKit cancels its wheel animation on any scrollTop write.
      // Keep the library's logical compensation, but represent it as a visual
      // offset until native motion settles. Dropping it would reintroduce the
      // >1000px content drift of the old backward-measurement skip.
      deferredOffset.current = offset + options.adjustments - el.scrollTop
      setGeometryOffset(deferredOffset.current)
      return
    }
    const physical = offset - deferredOffset.current
    // virtual-core may retry a temporarily clamped logical adjustment after
    // the sizer commits. A no-op DOM write still cancels WebKit's animation.
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
      // iOS virtual-core defers scrollTop during touch/momentum, but commits
      // prepended transforms immediately. That exposes a whole-turn flash
      // before the deferred correction (measured ~500px). Wait to publish
      // the page until both the gesture and the library have settled; then
      // capture the CURRENT anchor and commit geometry in one layout pass.
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
      // virtual-core 3.17.10 also pins on resize, independently of
      // followOnAppend. Distances are nonnegative: -1 disables both paths
      // immediately, before React renders after the upward gesture.
      v.options.followOnAppend = follow
      v.options.scrollEndThreshold = follow ? 80 : -1
    }
  }, [])
  const read = useCallback(() => {
    // An explicit jump supersedes the previous touch momentum and paging
    // budget; neither may re-arm following while its target is settling.
    direction.current = 0
    budget.current = 0
    userScrolling.current = false
    wheelMotion.current = false
    touchMotion.current = false
    setIntent('reading')
    // Turning follow off does not cancel virtual-core's in-flight end jump.
    // Replace that target before a fold or request jump can take ownership,
    // otherwise its next reconciliation still drags the reader toward the end.
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
    // One atomic transfer, not a scrollTop reconciliation loop: remove the
    // visual offset and settle the library's compensated physical position in
    // the same paint. Native input has already ended.
    if (deferredOffset.current < 0) {
      // Shrinking the sizer first silently clamps scrollTop and makes that
      // geometry correction look like new user motion. Move up while the old
      // extent still exists, then commit the smaller sizer in the same paint.
      flushSync(() => {
        clearDeferredOffset()
        virtual.current?.scrollToOffset(target)
      })
    } else {
      // Growth must commit first, otherwise the compensated write is clamped
      // against the smaller, still-offset sizer.
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
    // Paging follows the logical visible range, not an unsettled wheel offset.
    if (Math.max(0, el.scrollTop + deferredOffset.current) > Math.max(160, el.clientHeight)) return
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
    read()
    budget.current = 0
    // A retry deliberately bypasses the automatic error gate.
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
      // Disabling follow flags does not retire an indexed scrollToEnd target.
      // Its late measurements can still reclaim an upward wheel/touch gesture.
      // Replace that target before marking the new motion as native input.
      if (intent.current === 'seeking' || (delta < 0 && following.current)) read()
      userScrolling.current = nativeMotion
      direction.current = Math.sign(delta)
      state.current.onReadIntent?.()
      if (delta < 0) {
        setIntent('reading')
        // Repeated wheel/touch events while a page is pending belong to the
        // same gesture. Re-arming here silently queues another whole turn.
        if (!pending.current && !state.current.loadingOlder) budget.current = state.current.pageBudget ?? 2
        checkOlder()
      }
    }
    const wheel = (e: WheelEvent) => {
      if (Math.abs(e.deltaY) < Math.abs(e.deltaX)) return
      if (e.deltaY > 0) settleGeometry.current()
      // Synthetic wheel is useful for explicit reading intent, but the
      // following controlled scrollTop writes are not native iOS momentum.
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
      // A long held touch can outlive the last scroll timer. Always schedule
      // its transfer after release, including the library's 150ms grace tail.
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
      // A scrollbar drag has no wheel/touch direction. Ordinary clicks and
      // selection inside a message must not change the reader's intent.
      dragging.current = e.target === el && e.pointerType === 'mouse'
    }
    const up = () => { dragging.current = false }
    const finish = () => {
      if (touchY != null || dragging.current) return
      // A scrollend from the superseded end jump can arrive after the first
      // wheel frame. Do not let that stale event release geometry debt while
      // WebKit's new native animation is still moving.
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
      // Keyboard/rotation changes the reachable end without appending a row.
      // This owner alone may follow it; geometry never re-arms reading.
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
