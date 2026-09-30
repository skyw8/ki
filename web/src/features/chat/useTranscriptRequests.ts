import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { ApiError, type Client } from '../../api/client'
import type { SessionDetail, ViewState } from '../../api/types'
import { applyTail, applyIndex, compactBoundary, evictBodies, hydrateEntries, hydrateTurn } from '../../lib/model'
import type { MessageView } from '../../lib/messageView'

type BodyRequest = { promise: Promise<boolean>; resolve: (ok: boolean) => void }
type Boundary = { cursor?: string; hasMore: boolean }
type Scope = {
  id: string | null
  abort: AbortController
  pendingBoundary?: Boundary & { sources: Boundary[] }
  page?: Promise<SessionDetail | null>
  pageRequest?: { abort: AbortController; boundary: Boundary }
  index?: Promise<boolean>
  indexFrontier?: string
  indexCovered?: string
  indexRefresh?: { frontier?: string }
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
  const [indexLoading, setIndexLoading] = useState(false)
  const [indexError, setIndexError] = useState(false)
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
    s.pageRequest?.abort.abort()
    window.clearTimeout(evictionTimer.current)
    window.clearTimeout(s.timer)
    for (const request of s.bodies.values()) request.resolve(false)
    s.bodies.clear()
    s.pending.clear()
  }
  useLayoutEffect(() => {
    setLoadingOlder(false)
    setOlderError(false)
    setIndexLoading(false)
    setIndexError(false)
    return () => dispose(scope)
  }, [scope])

  const cancel = useCallback(() => dispose(current.current), [])

  const boundary = (s: Scope) => {
    const observed = { cursor: view.current.oldestId, hasMore: !!view.current.hasMore }
    const pending = s.pendingBoundary
    if (!pending) return observed
    const acknowledged = observed.cursor === pending.cursor && observed.hasMore === pending.hasMore
    if (acknowledged || !pending.sources.some(source => source.cursor === observed.cursor && source.hasMore === observed.hasMore)) {
      // A page committed, or recovery reset a disconnected window. A cached
      // false must never override the authoritative reducer boundary.
      s.pendingBoundary = undefined
      return observed
    }
    return pending
  }

  useEffect(() => {
    const s = current.current
    const request = s.pageRequest
    if (!request) return
    const next = boundary(s)
    if (next.cursor !== request.boundary.cursor || next.hasMore !== request.boundary.hasMore) {
      // A dead request for the abandoned window must not block paging the
      // recovered window until a remote connection eventually times out.
      request.abort.abort()
    }
  })

  const loadOlder = useCallback((limit = 100): Promise<SessionDetail | null> => {
    const s = current.current
    if (!s.id || !valid(s)) return Promise.resolve(null)
    if (s.page) return s.page
    const start = boundary(s)
    const cursor = start.cursor
    if (!start.hasMore || !cursor) return Promise.resolve(null)
    setOlderError(false)
    setLoadingOlder(true)
    const compact = mode.current.mode === 'compact'
    const request = new AbortController()
    s.pageRequest = { abort: request, boundary: { cursor, hasMore: start.hasMore } }
    const page = api.get(s.id, { before: cursor, limit, view: compact ? 'compact' : undefined, keep: compact ? mode.current.keep : undefined, signal: request.signal }).then(async out => {
      await beforeCommit(s.abort.signal)
      if (!valid(s)) return null
      if (out.hasMore && (!out.entries?.length || !out.oldestId || out.oldestId === cursor)) {
        throw new Error('History cursor did not advance')
      }
      const currentBoundary = boundary(s)
      // A tail may have reset the active window while this page travelled.
      if (currentBoundary.cursor !== cursor || currentBoundary.hasMore !== start.hasMore) return null
      const source = { cursor: view.current.oldestId, hasMore: !!view.current.hasMore }
      const next = { cursor: out.oldestId || cursor, hasMore: !!out.hasMore }
      // Bridge only the response-to-commit interval, not the entire session.
      s.pendingBoundary = { ...next, sources: [...s.pendingBoundary?.sources ?? [], source, { cursor, hasMore: start.hasMore }] }
      const meta = { hasMore: next.hasMore, oldestId: next.cursor, compactTurns: out.compactTurns }
      setView(v => {
        if (!valid(s)) return v
        const sameBoundary = (v.oldestId === cursor && !!v.hasMore === start.hasMore)
          || (v.oldestId === source.cursor && !!v.hasMore === source.hasMore)
        return hydrateEntries(v, out.entries ?? [], sameBoundary ? meta : { compactTurns: out.compactTurns })
      })
      return out
    }).catch(async error => {
      if (request.signal.aborted) return null
      if (valid(s) && error instanceof ApiError && error.status === 409) {
        // The cursor no longer belongs to this branch. Refresh its boundary,
        // retain cached history, and leave retry explicit instead of silently
        // declaring the beginning or looping the rejected cursor forever.
        const liveRevision = view.current.liveRevision
        const out = await api.get(s.id!, { view: compact ? 'compact' : undefined,
          keep: compact ? mode.current.keep : undefined, signal: s.abort.signal }).catch(() => null)
        if (out && valid(s)) {
          s.pendingBoundary = undefined
          setView(v => {
            if (!valid(s)) return v
            const next = applyTail(v, out, liveRevision)
            // A connected tail normally preserves a reader-owned cursor, but
            // this specific cursor was rejected: even an unchanged leaf must
            // replace it. Never replace a boundary updated in the meantime.
            return v.oldestId === cursor && v.liveRevision === liveRevision
              ? { ...next, oldestId: out.oldestId, hasMore: !!out.hasMore }
              : next
          })
        }
      }
      if (valid(s)) setOlderError(true)
      return null
    }).finally(() => {
      if (s.page !== page) return
      s.page = undefined
      s.pageRequest = undefined
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
      s.page = (async () => {
        let pendingSource = view.current
        while (valid(s)) {
          const source = pendingSource
          if (!source.oldestId || source.compactTurns?.length) return null
          const page = await api.get(s.id!, { turn: source.oldestId, view: 'compact', keep: presentation.keep, signal: s.abort.signal })
          await beforeCommit(s.abort.signal)
          if (!valid(s)) return null
          if (!page.compactTurns?.length) throw new Error('Compact projection missing')
          const latest = view.current
          // Unlike ordinary pagination, conversion can share an unchanged
          // scope with a recovered window. Retry that window instead of
          // adopting the old projection's cursor or cross-branch summaries.
          if (latest.oldestId !== source.oldestId || !!latest.hasMore !== !!source.hasMore || latest.leafId !== source.leafId) {
            pendingSource = latest
            continue
          }
          // React may defer the updater past another SSE/recovery commit.
          // Publish the temporary cursor only after that updater accepts the
          // captured source; a rejected projection must retry, not leave an
          // unacknowledged hasMore=false bridge suppressing real pagination.
          const commit = await new Promise<{ accepted: boolean; state: ViewState } | null>(resolve => {
            const onAbort = () => resolve(null)
            s.abort.signal.addEventListener('abort', onAbort, { once: true })
            setView(v => {
              s.abort.signal.removeEventListener('abort', onAbort)
              if (!valid(s)) { resolve(null); return v }
              if (v.oldestId !== source.oldestId || !!v.hasMore !== !!source.hasMore || v.leafId !== source.leafId) {
                resolve({ accepted: false, state: v })
                return v
              }
              const next = compactBoundary(v, page, source)
              s.pendingBoundary = { cursor: next.oldestId, hasMore: !!next.hasMore,
                sources: [{ cursor: source.oldestId, hasMore: !!source.hasMore }] }
              resolve({ accepted: true, state: next })
              return next
            })
          })
          if (!commit || !valid(s)) return null
          if (!commit.accepted) { pendingSource = commit.state; continue }
          return page
        }
        return null
      })().catch(() => { if (valid(s)) setOlderError(true); return null }).finally(() => {
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

  const requestIndex = useCallback((sessionId: string, refresh = false, frontier = view.current.leafId): Promise<boolean> => {
    const s = current.current
    if (s.id !== sessionId || !valid(s)) return Promise.resolve(false)
    if (s.index) {
      // Recovery arriving during a fetch invalidates its captured snapshot.
      // Coalesce another refresh rather than trusting that older response.
      if (refresh && frontier !== s.indexFrontier) s.indexRefresh = { frontier }
      return s.index
    }
    if (view.current.indexLoaded && !refresh) return Promise.resolve(true)
    setIndexLoading(true)
    setIndexError(false)
    s.indexFrontier = frontier
    const task = api.get(sessionId, { fields: 'index', signal: s.abort.signal }).then(out => {
      if (!valid(s)) return false
      // Missing metadata is not an empty branch; keep an explicit retry.
      if (!Array.isArray(out.index)) throw new Error('Session index missing')
      setView(v => valid(s) ? applyIndex(v, out) : v)
      s.indexCovered = frontier
      return true
    }).catch(() => {
      if (valid(s)) setIndexError(true)
      return false
    }).finally(() => {
      if (s.index === task) s.index = undefined
      if (valid(s)) {
        const refresh = s.indexRefresh
        s.indexRefresh = undefined
        if (refresh) void requestIndex(sessionId, true, refresh.frontier)
        else setIndexLoading(false)
      }
    })
    s.index = task
    return task
  }, [api, setView, view])

  const refreshIndex = useCallback((sessionId: string, frontier?: string) => {
    const s = current.current
    // Visibility and the push-ready event can recover the same frontier twice.
    // An append-only branch needs only one metadata read per recovered leaf.
    if (frontier && (s.indexCovered === frontier || (s.index && s.indexFrontier === frontier) || s.indexRefresh?.frontier === frontier)) return
    // Keep initial opening cheap; only refresh metadata someone requested.
    if (view.current.indexLoaded || s.index) void requestIndex(sessionId, true, frontier)
  }, [requestIndex, view])

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

  return { cancel, loadOlder, loadingOlder, olderError, requestHydrate, requestIndex, refreshIndex, indexLoading, indexError, requestTurn, protect }
}
