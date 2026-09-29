import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import type { Client } from '../../api/client'
import type { SessionDetail, ViewState } from '../../api/types'
import { applyIndex, compactBoundary, evictBodies, hydrateEntries, hydrateTurn } from '../../lib/model'
import type { MessageView } from '../../lib/messageView'

type BodyRequest = { promise: Promise<boolean>; resolve: (ok: boolean) => void }
type Scope = {
  id: string | null
  abort: AbortController
  cursor?: string
  hasMore?: boolean
  page?: Promise<SessionDetail | null>
  index?: Promise<void>
  bodies: Map<string, BodyRequest>
  pending: Set<string>
  timer: number
  hydrating: boolean
  turns: Map<string, Promise<boolean>>
}

/** Network state belongs to an opened branch, not to the lifetime of App. */
export function useTranscriptRequests(
  api: Client,
  id: string | null,
  revision: number,
  view: { current: ViewState },
  setView: Dispatch<SetStateAction<ViewState>>,
  presentation: MessageView,
  beforeCommit: (signal: AbortSignal) => Promise<void>,
) {
  const scope = useMemo<Scope>(() => ({ id, abort: new AbortController(), bodies: new Map(), pending: new Set(), turns: new Map(), timer: 0, hydrating: false }), [api, id, revision, presentation.mode, presentation.keep])
  const mode = useRef(presentation)
  mode.current = presentation
  const current = useRef(scope)
  current.current = scope
  const [loadingOlder, setLoadingOlder] = useState(false)
  const [olderError, setOlderError] = useState(false)
  const protectedIds = useRef<ReadonlySet<string>>(new Set())
  const evictionTimer = useRef(0)
  const protect = useCallback((ids: string[]) => {
    protectedIds.current = new Set(ids)
    window.clearTimeout(evictionTimer.current)
    // Wait until the visible range settles; rows must not lose their bodies
    // during a gesture or while the virtualizer is measuring a new page.
    evictionTimer.current = window.setTimeout(() => setView(v => evictBodies(v, protectedIds.current)), 500)
  }, [setView])
  const valid = (s: Scope) => current.current === s && !s.abort.signal.aborted
  const dispose = (s: Scope) => {
    s.abort.abort()
    window.clearTimeout(evictionTimer.current)
    window.clearTimeout(s.timer)
    for (const request of s.bodies.values()) request.resolve(false)
    s.bodies.clear()
    s.pending.clear()
  }
  useLayoutEffect(() => {
    setLoadingOlder(false)
    setOlderError(false)
    return () => dispose(scope)
  }, [scope])

  const cancel = useCallback(() => dispose(current.current), [])

  const loadOlder = useCallback((limit = 100): Promise<SessionDetail | null> => {
    const s = current.current
    if (!s.id || !valid(s)) return Promise.resolve(null)
    if (s.page) return s.page
    const cursor = s.cursor ?? view.current.oldestId
    if (!(s.hasMore ?? view.current.hasMore) || !cursor) return Promise.resolve(null)
    setOlderError(false)
    setLoadingOlder(true)
    const compact = mode.current.mode === 'compact'
    const page = api.get(s.id, { before: cursor, limit, view: compact ? 'compact' : undefined, keep: compact ? mode.current.keep : undefined, signal: s.abort.signal }).then(async out => {
      await beforeCommit(s.abort.signal)
      if (!valid(s)) return null
      if (out.hasMore && (!out.entries?.length || !out.oldestId || out.oldestId === cursor)) {
        throw new Error('History cursor did not advance')
      }
      // Advance with the response, not with a later React ref effect. A jump
      // can otherwise request the same page repeatedly before React commits.
      // `|| cursor`, not `??`: a page that legitimately ends the history
      // reports an empty oldestId, and storing that as the cursor would make
      // every later `before=` request a no-op (dead paging until reload).
      s.cursor = out.oldestId || cursor
      s.hasMore = !!out.hasMore
      const meta = { hasMore: s.hasMore, oldestId: s.cursor, compactTurns: out.compactTurns }
      setView(v => valid(s) ? hydrateEntries(v, out.entries ?? [], meta) : v)
      return out
    }).catch(() => {
      if (valid(s)) setOlderError(true)
      return null
    }).finally(() => {
      s.page = undefined
      if (valid(s)) setLoadingOlder(false)
    })
    s.page = page
    return page
  }, [api, setView, view, beforeCommit])

  const requestTurn = useCallback((turnId: string): Promise<boolean> => {
    const s = current.current
    if (!s.id || !valid(s)) return Promise.resolve(false)
    if (view.current.loadedTurnIds?.includes(turnId)) return Promise.resolve(true)
    const pending = s.turns.get(turnId)
    if (pending) return pending
    const task = (async () => {
      const entries: NonNullable<SessionDetail['entries']> = []
      let before: string | undefined
      try {
        do {
          const page = await api.get(s.id!, { turn: turnId, before, limit: 500, signal: s.abort.signal })
          if (!valid(s)) return false
          entries.unshift(...page.entries ?? [])
          if (!page.hasMore) break
          if (!page.oldestId || page.oldestId === before) throw new Error('Turn cursor did not advance')
          before = page.oldestId
        } while (valid(s))
        // Publish once: partial expansion would repeatedly change the fold
        // count and move its final reply while the reader waits on the link.
        await beforeCommit(s.abort.signal)
        if (!valid(s)) return false
        setView(v => valid(s) ? hydrateTurn(v, turnId, entries) : v)
        return true
      } catch { return false }
      finally { s.turns.delete(turnId) }
    })()
    s.turns.set(turnId, task)
    return task
  }, [api, setView, view, beforeCommit])

  useEffect(() => {
    // Switching to detailed (or asking for more visible replies) is an
    // explicit request for bodies omitted by an earlier compact projection.
    const s = current.current
    const state = view.current
    if (presentation.mode === 'compact' && !state.compactTurns?.length && state.entries.length && state.oldestId && s.id) {
      setLoadingOlder(true)
      s.page = api.get(s.id, { turn: state.oldestId, view: 'compact', keep: presentation.keep, signal: s.abort.signal }).then(async page => {
        await beforeCommit(s.abort.signal)
        if (!valid(s)) return null
        s.cursor = page.oldestId
        s.hasMore = page.hasMore
        setView(v => valid(s) ? compactBoundary(v, page) : v)
        return page
      }).catch(() => { if (valid(s)) setOlderError(true); return null }).finally(() => {
        s.page = undefined
        if (valid(s)) setLoadingOlder(false)
      })
    }
    for (const turn of state.compactTurns ?? []) {
      if (state.loadedTurnIds?.includes(turn.id)) continue
      if (presentation.mode === 'detailed') void requestTurn(turn.id)
      else if (presentation.keep > turn.visibleNodeIds.length - 1 && s.id) {
        void api.get(s.id, { turn: turn.id, view: 'compact', keep: presentation.keep, signal: s.abort.signal }).then(page => {
          if (valid(s)) setView(v => valid(s) ? hydrateEntries(v, page.entries ?? [], { compactTurns: page.compactTurns }) : v)
        }).catch(() => {})
      }
    }
  }, [presentation.mode, presentation.keep, requestTurn, scope, view, api, setView, beforeCommit])

  const requestIndex = useCallback((sessionId: string): Promise<void> => {
    const s = current.current
    if (s.id !== sessionId || !valid(s) || view.current.indexLoaded) return Promise.resolve()
    if (s.index) return s.index
    s.index = api.get(sessionId, { fields: 'index', signal: s.abort.signal }).then(out => {
      if (valid(s)) setView(v => valid(s) ? applyIndex(v, out) : v)
    }).catch(() => { /* Opening the navigation again retries. */ }).finally(() => { s.index = undefined })
    return s.index
  }, [api, setView, view])

  const requestBody = useCallback((entryId: string): Promise<boolean> => {
    const s = current.current
    if (!s.id || !entryId || !valid(s)) return Promise.resolve(false)
    const body = view.current.entries.find(e => e.id === entryId)
    if (body && !body.truncated) return Promise.resolve(true)
    const existing = s.bodies.get(entryId)
    if (existing) return existing.promise
    let resolve!: (ok: boolean) => void
    const promise = new Promise<boolean>(done => { resolve = done })
    s.bodies.set(entryId, { promise, resolve })
    s.pending.add(entryId)
    const flush = async () => {
      s.timer = 0
      if (!valid(s) || s.hydrating || !s.id) return
      s.hydrating = true
      // One batch at a time keeps a phone from downloading overscan bodies in
      // parallel; the server accepts at most forty ids in one request.
      while (valid(s) && s.pending.size) {
        const ids = [...s.pending].slice(0, 40)
        ids.forEach(key => s.pending.delete(key))
        let received = new Set<string>()
        try {
          const out = await api.getEntries(s.id, ids, s.abort.signal)
          if (valid(s)) {
            received = new Set((out.entries ?? []).map(e => e.id))
            setView(v => valid(s) ? hydrateEntries(v, out.entries ?? []) : v)
          }
        } catch { /* A row keeps its preview and offers an explicit retry. */ }
        for (const key of ids) {
          s.bodies.get(key)?.resolve(received.has(key))
          s.bodies.delete(key)
        }
      }
      s.hydrating = false
    }
    if (!s.timer && !s.hydrating) s.timer = window.setTimeout(() => { void flush() }, 32)
    return promise
  }, [api, setView, view])

  const requestHydrate = useCallback((nodeId: string): Promise<boolean> => {
    const id = nodeId.replace(/^system:/, '')
    // Tool rows are keyed by call id, while body endpoints accept persisted
    // entry ids. Both the call arguments and its result may need hydration.
    const matches = view.current.allEntries.filter(e => e.id === id || e.message?.toolCallId === id || e.message?.content?.some(c => c.type === 'toolCall' && c.id === id))
    const ids = matches.length ? matches.map(e => e.id) : [id]
    return Promise.all(ids.map(requestBody)).then(results => results.every(Boolean))
  }, [requestBody, view])

  return { cancel, loadOlder, loadingOlder, olderError, requestHydrate, requestIndex, requestTurn, protect }
}
