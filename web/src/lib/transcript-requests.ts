import { ApiError, type Client } from '../api/client'
import type { Entry, SessionDetail } from '../api/types'
import type { MessageView } from './messageView'
import { evictBodies } from './model'
import { bodyKind } from './transcriptCoverage'
import { TranscriptStore, type TranscriptTicket } from './transcript-store'

export type TranscriptRequestStatus = Readonly<{
  loadingOlder: boolean
  olderError: boolean
  indexLoading: boolean
  indexError: boolean
}>

type Options = {
  api: Pick<Client, 'get' | 'getEntries'>
  id: string | null
  store: TranscriptStore
  presentation: MessageView
  beforeCommit: (signal: AbortSignal) => Promise<void>
  onRecoverNeeded?: () => Promise<void>
}
type BodyRequest = { promise: Promise<boolean>; resolve: (ok: boolean) => void }
type Job<T> = {
  abort: AbortController
  ticket: TranscriptTicket
  promise: Promise<T>
  resolve: (value: T) => void
  committing?: boolean
}
type Scope = {
  ticket: TranscriptTicket
  abort: AbortController
  jobs: Set<{ abort: AbortController }>
  unsubscribe: () => void
  page?: Job<SessionDetail | null>
  index?: Job<boolean>
  indexFrontier?: string
  indexCovered?: string
  indexRefresh?: { frontier?: string }
  recovery?: Promise<void>
  turns: Map<string, Job<boolean>>
  keeps: Map<string, Job<boolean>>
  bodies: Map<string, BodyRequest>
  pending: Set<string>
  hydrating: boolean
  bodyTimer?: ReturnType<typeof setTimeout>
  evictionTimer?: ReturnType<typeof setTimeout>
  protectedIds: ReadonlySet<string>
  projections: Map<string, string>
  projectionQueued: boolean
}

const idle: TranscriptRequestStatus = Object.freeze({
  loadingOlder: false, olderError: false, indexLoading: false, indexError: false,
})

/** Also settles when a test transport, gesture gate, or remote peer ignores abort. */
function wait<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => reject(new DOMException('Request canceled', 'AbortError'))
    if (signal.aborted) {
      // Observe a transport rejection even when it was canceled synchronously.
      void promise.catch(() => {})
      abort()
      return
    }
    signal.addEventListener('abort', abort, { once: true })
    promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}

/**
 * Owns network lifetimes, not transcript truth. Every projected response must
 * present its captured capability to the synchronous store at commit time.
 */
export class TranscriptRequests {
  private scope?: Scope
  private status = idle
  private listeners = new Set<() => void>()

  constructor(private readonly options: Options) {}

  getSnapshot = (): TranscriptRequestStatus => this.status
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  private setStatus(update: Partial<TranscriptRequestStatus>): void {
    const next = { ...this.status, ...update }
    if (Object.keys(next).every(key => next[key as keyof typeof next] === this.status[key as keyof typeof next])) return
    this.status = Object.freeze(next)
    for (const listener of this.listeners) listener()
  }

  /** Restartable for React's setup-cleanup-setup lifetime probe. */
  start = (): void => {
    if (this.scope && this.valid(this.scope)) return
    const { store, id } = this.options
    if (store.sessionId !== id) return
    const s: Scope = {
      ticket: store.capture('body'), abort: new AbortController(), jobs: new Set(), unsubscribe: () => {},
      turns: new Map(), keeps: new Map(), bodies: new Map(), pending: new Set(),
      hydrating: false, protectedIds: new Set(), projections: new Map(), projectionQueued: false,
    }
    this.scope = s
    this.setStatus(idle)
    s.unsubscribe = store.subscribe(() => {
      if (!this.valid(s)) { this.stop(s); return }
      const page = s.page
      if (page && !page.committing && !store.canCommit(page.ticket)) {
        // Disconnected windows must release the paging slot immediately, not
        // after an abandoned request eventually returns over a slow link.
        page.abort.abort()
        if (page.ticket.kind === 'page') {
          s.page = undefined
          this.setStatus({ loadingOlder: false })
        }
      }
      for (const job of s.turns.values()) {
        if (!job.committing && !store.canCommit(job.ticket)) job.abort.abort()
      }
      this.scheduleProjection(s)
    })
    this.scheduleProjection(s)
  }

  private valid(s: Scope): boolean {
    return this.scope === s && !s.abort.signal.aborted && this.options.store.isCurrent(s.ticket)
  }

  private stop(s: Scope): void {
    s.unsubscribe()
    s.abort.abort()
    clearTimeout(s.bodyTimer)
    clearTimeout(s.evictionTimer)
    for (const job of s.jobs) job.abort.abort()
    for (const request of s.bodies.values()) request.resolve(false)
    s.bodies.clear()
    s.pending.clear()
    if (this.scope === s) {
      this.scope = undefined
      this.setStatus(idle)
    }
  }

  dispose = (): void => { if (this.scope) this.stop(this.scope) }
  cancel = this.dispose

  private job<T>(s: Scope, ticket: TranscriptTicket, canceled: T): Job<T> {
    let resolve!: (value: T) => void
    const promise = new Promise<T>(done => { resolve = done })
    const job: Job<T> = { ticket, abort: new AbortController(), promise, resolve: value => {
      s.jobs.delete(job)
      resolve(value)
    } }
    job.abort.signal.addEventListener('abort', () => job.resolve(canceled), { once: true })
    s.jobs.add(job)
    return job
  }

  private async gate(s: Scope, job: { abort: AbortController }): Promise<boolean> {
    if (!this.valid(s) || job.abort.signal.aborted) return false
    await wait(this.options.beforeCommit(job.abort.signal), job.abort.signal)
    return this.valid(s) && !job.abort.signal.aborted
  }

  loadOlder = (limit = 100): Promise<SessionDetail | null> => {
    const s = this.scope
    const { store, api, id, presentation } = this.options
    if (!s || !id || !this.valid(s)) return Promise.resolve(null)
    if (s.page) return s.page.promise
    const state = store.current
    if (presentation.mode === 'compact' && !state.compactTurns?.length && state.entries.length && state.oldestId) {
      return this.convert(s)
    }
    if (!state.hasMore || !state.oldestId) return Promise.resolve(null)
    const ticket = store.capture('page')
    const job = this.job(s, ticket, null as SessionDetail | null)
    s.page = job
    this.setStatus({ loadingOlder: true, olderError: false })
    void (async () => {
      try {
        const compact = presentation.mode === 'compact'
        const out = await wait(api.get(id, { before: ticket.oldestId, limit,
          view: compact ? 'compact' : undefined, keep: compact ? presentation.keep : undefined,
          signal: job.abort.signal }), job.abort.signal)
        if (!await this.gate(s, job) || !store.canCommit(ticket)) return null
        if (out.hasMore && (!out.entries?.length || !out.oldestId || out.oldestId === ticket.oldestId)) {
          throw new Error('History cursor did not advance')
        }
        job.committing = true
        return store.commit(ticket, out) ? out : null
      } catch (error) {
        if (!this.valid(s) || job.abort.signal.aborted) return null
        if (error instanceof ApiError && error.status === 409 && store.canCommit(ticket)) {
          // A rejected cursor is not the beginning of history. Reconcile its
          // boundary, but leave loading the replacement page an explicit retry.
          const recovery = store.capture('tail')
          try {
            const compact = presentation.mode === 'compact'
            const out = await wait(api.get(id, { view: compact ? 'compact' : undefined,
              keep: compact ? presentation.keep : undefined, signal: job.abort.signal }), job.abort.signal)
            if (await this.gate(s, job)) {
              job.committing = true
              store.commit(recovery, out, { rejectedCursor: ticket.oldestId })
            }
          } catch { /* The explicit retry remains available if recovery fails. */ }
        }
        if (this.valid(s) && !job.abort.signal.aborted) this.setStatus({ olderError: true })
        return null
      } finally {
        if (s.page === job) {
          s.page = undefined
          if (this.valid(s)) this.setStatus({ loadingOlder: false })
        }
      }
    })().then(job.resolve)
    return job.promise
  }

  private convert(s: Scope): Promise<SessionDetail | null> {
    if (s.page) return s.page.promise
    const { store, api, id, presentation, onRecoverNeeded } = this.options
    // Conversion retries are one public operation with independently canceled
    // transports, so a stale projection can never publish a temporary cursor.
    const outer = this.job(s, store.capture('body'), null as SessionDetail | null)
    this.setStatus({ loadingOlder: true, olderError: false })
    let reconciled = false
    const run = async (): Promise<SessionDetail | null> => {
      while (this.valid(s) && !outer.abort.signal.aborted) {
        const source = store.current
        if (!id || !source.oldestId || source.compactTurns?.length) return null
        const ticket = store.capture('compact')
        // Record manual attempts and changed-window retries too. An unrelated
        // body/status commit must not automatically repeat a failed conversion.
        s.projections.set('compact', `compact:${ticket.frontier}:${ticket.leafId}:${ticket.oldestId}:${ticket.hasMore}`)
        const job = this.job(s, ticket, null as SessionDetail | null)
        // Callers deduplicate against the whole conversion, not this transport.
        job.promise = outer.promise
        s.page = job
        try {
          const page = await wait(api.get(id, { turn: ticket.oldestId, view: 'compact',
            keep: presentation.keep, signal: job.abort.signal }), job.abort.signal)
          if (!await this.gate(s, job)) {
            // The gate may settle just before a synchronous recovery aborts
            // this transport; that window still needs the same fresh retry.
            if (this.valid(s) && !store.canCommit(ticket)) continue
            return null
          }
          if (!store.canCommit(ticket)) continue
          if (!page.compactTurns?.length) throw new Error('Compact projection missing')
          job.committing = true
          if (store.commit(ticket, page)) return page
          job.committing = false
          if (!store.canCommit(ticket)) continue
          // A still-current ticket cannot authorize a sibling tail selected
          // remotely. Reconcile once instead of looping a valid-but-foreign
          // response or admitting its sparse branch edges as body metadata.
          if (reconciled || !onRecoverNeeded) {
            this.setStatus({ olderError: true })
            return null
          }
          reconciled = true
          await wait(this.recover(s), outer.abort.signal)
        } catch {
          if (!this.valid(s) || outer.abort.signal.aborted) return null
          if (!store.canCommit(ticket)) continue
          this.setStatus({ olderError: true })
          return null
        } finally {
          job.resolve(null)
        }
      }
      return null
    }
    void (async () => {
      try { return await run() }
      finally {
        if (s.page?.promise === outer.promise) {
          s.page = undefined
          if (this.valid(s)) this.setStatus({ loadingOlder: false })
        }
      }
    })().then(outer.resolve)
    return outer.promise
  }

  requestTurn = (turnId: string): Promise<boolean> => {
    const s = this.scope
    const { store, api, id } = this.options
    if (!s || !id || !this.valid(s)) return Promise.resolve(false)
    if (store.current.loadedTurnIds?.includes(turnId)) return Promise.resolve(true)
    const pending = s.turns.get(turnId)
    if (pending && !pending.abort.signal.aborted) return pending.promise
    const ticket = store.capture('turn', turnId)
    const job = this.job(s, ticket, false)
    s.turns.set(turnId, job)
    void (async () => {
      const entries: Entry[] = []
      const cursors = new Set<string>()
      let before: string | undefined
      try {
        do {
          const page = await wait(api.get(id, { turn: turnId, before, limit: 500, signal: job.abort.signal }), job.abort.signal)
          if (!this.valid(s) || !store.canCommit(ticket)) return false
          entries.unshift(...page.entries ?? [])
          if (!page.hasMore) break
          if (!page.oldestId || cursors.has(page.oldestId)) throw new Error('Turn cursor did not advance')
          before = page.oldestId
          cursors.add(before)
        } while (this.valid(s))
        // Publish one complete expansion, never a sequence of partial folds.
        if (!await this.gate(s, job)) return false
        job.committing = true
        return store.commit(ticket, { id, entries })
      } catch { return false }
      finally { if (s.turns.get(turnId) === job) s.turns.delete(turnId) }
    })().then(job.resolve)
    return job.promise
  }

  private requestKeep(s: Scope, turnId: string): Promise<boolean> {
    const existing = s.keeps.get(turnId)
    if (existing) return existing.promise
    const { store, api, id, presentation, onRecoverNeeded } = this.options
    const job = this.job(s, store.capture('keep', turnId), false)
    s.keeps.set(turnId, job)
    void (async () => {
      try {
        for (let attempt = 0; attempt < 2 && this.valid(s); attempt++) {
          if (!id || !store.current.compactTurns?.some(turn => turn.id === turnId)) return false
          const ticket = job.ticket = store.capture('keep', turnId)
          const page = await wait(api.get(id, { turn: turnId, view: 'compact',
            keep: presentation.keep, signal: job.abort.signal }), job.abort.signal)
          if (!await this.gate(s, job)) return false
          if (store.commit(ticket, page)) return true
          if (attempt || !onRecoverNeeded) return false
          // A server-selected tail cannot be adopted by a keep response.
          // Recovery first establishes authority, then one fresh projection.
          await wait(this.recover(s), job.abort.signal)
        }
      } catch { /* Keep the existing projection and allow another mode change. */ }
      finally { if (s.keeps.get(turnId) === job) s.keeps.delete(turnId) }
      return false
    })().then(job.resolve)
    return job.promise
  }

  private recover(s: Scope): Promise<void> {
    if (s.recovery) return s.recovery
    // Several stale turns share one reconciliation. Restarting the main
    // reader for each response would cancel another turn's recovery waiter.
    const task = wait(Promise.resolve().then(() => {
      if (this.valid(s)) return this.options.onRecoverNeeded?.()
    }), s.abort.signal)
      .finally(() => { if (s.recovery === task) s.recovery = undefined })
    s.recovery = task
    return task
  }

  private scheduleProjection(s: Scope): void {
    if (s.projectionQueued || !this.valid(s)) return
    s.projectionQueued = true
    queueMicrotask(() => {
      s.projectionQueued = false
      if (!this.valid(s)) return
      const { store, presentation } = this.options
      const state = store.current
      if (presentation.mode === 'compact' && !state.compactTurns?.length && state.entries.length && state.oldestId) {
        const ticket = store.capture('compact')
        const key = `compact:${ticket.frontier}:${ticket.leafId}:${ticket.oldestId}:${ticket.hasMore}`
        if (!s.page && s.projections.get('compact') !== key) {
          s.projections.set('compact', key)
          void this.convert(s)
        }
        return
      }
      const frontier = store.capture('body').frontier
      const loaded = new Set(state.loadedTurnIds)
      for (const turn of state.compactTurns ?? []) {
        if (loaded.has(turn.id)) continue
        const key = `${presentation.mode}:${frontier}:${turn.id}:${turn.tailId}`
        if (s.projections.get(`turn:${turn.id}`) === key) continue
        // Remember only the latest attempt per turn, not every recovery
        // generation of a session that may remain open for hours.
        s.projections.set(`turn:${turn.id}`, key)
        if (presentation.mode === 'detailed') void this.requestTurn(turn.id)
        else if (presentation.keep > turn.visibleNodeIds.length - 1) void this.requestKeep(s, turn.id)
      }
    })
  }

  requestIndex = (sessionId: string, refresh = false, frontier = this.options.store.current.leafId): Promise<boolean> => {
    const s = this.scope
    const { store, api } = this.options
    if (!s || sessionId !== this.options.id || !this.valid(s)) return Promise.resolve(false)
    if (s.index) {
      if (refresh && frontier !== s.indexFrontier) s.indexRefresh = { frontier }
      return s.index.promise
    }
    if (store.current.indexLoaded && !refresh) return Promise.resolve(true)
    const job = this.job(s, store.capture('index'), false)
    s.index = job
    s.indexFrontier = frontier
    this.setStatus({ indexLoading: true, indexError: false })
    void (async () => {
      try {
        const out = await wait(api.get(sessionId, { fields: 'index', signal: job.abort.signal }), job.abort.signal)
        if (!this.valid(s)) return false
        if (!Array.isArray(out.index)) throw new Error('Session index missing')
        if (!store.commit(job.ticket, out)) return false
        s.indexCovered = frontier
        return true
      } catch {
        if (this.valid(s) && !job.abort.signal.aborted) this.setStatus({ indexError: true })
        return false
      } finally {
        if (s.index === job) s.index = undefined
        if (this.valid(s)) {
          const refresh = s.indexRefresh
          s.indexRefresh = undefined
          if (refresh) void this.requestIndex(sessionId, true, refresh.frontier)
          else this.setStatus({ indexLoading: false })
        }
      }
    })().then(job.resolve)
    return job.promise
  }

  refreshIndex = (sessionId: string, frontier?: string): void => {
    const s = this.scope
    if (!s || !this.valid(s) || sessionId !== this.options.id) return
    if (frontier && (s.indexCovered === frontier || (s.index && s.indexFrontier === frontier) || s.indexRefresh?.frontier === frontier)) return
    if (this.options.store.current.indexLoaded || s.index) void this.requestIndex(sessionId, true, frontier)
  }

  private requestBody(s: Scope, entryId: string): Promise<boolean> {
    const { store, id } = this.options
    if (!id || !entryId || !this.valid(s)) return Promise.resolve(false)
    const body = store.current.entries.find(entry => entry.id === entryId)
    if (body && bodyKind(body) === 'full') return Promise.resolve(true)
    const existing = s.bodies.get(entryId)
    if (existing) return existing.promise
    let resolve!: (ok: boolean) => void
    const promise = new Promise<boolean>(done => { resolve = done })
    s.bodies.set(entryId, { promise, resolve })
    s.pending.add(entryId)
    if (!s.bodyTimer && !s.hydrating) s.bodyTimer = setTimeout(() => { void this.flushBodies(s) }, 32)
    return promise
  }

  private async flushBodies(s: Scope): Promise<void> {
    s.bodyTimer = undefined
    if (!this.valid(s) || s.hydrating) return
    const { api, store, id } = this.options
    if (!id) return
    s.hydrating = true
    try {
      while (this.valid(s) && s.pending.size) {
        const ids = [...s.pending].slice(0, 40)
        for (const key of ids) s.pending.delete(key)
        const ticket = store.capture('body')
        let received = new Set<string>()
        try {
          const out = await wait(api.getEntries(id, ids, s.abort.signal), s.abort.signal)
          // Body responses deliberately cannot carry compact coverage metadata.
          if (this.valid(s) && store.commit(ticket, { id, entries: out.entries })) {
            received = new Set((out.entries ?? []).map(entry => entry.id))
          }
        } catch { /* A preview remains visible with explicit row retry. */ }
        for (const key of ids) {
          s.bodies.get(key)?.resolve(received.has(key))
          s.bodies.delete(key)
        }
      }
    } finally { s.hydrating = false }
  }

  requestHydrate = (nodeId: string): Promise<boolean> => {
    const s = this.scope
    if (!s || !this.valid(s)) return Promise.resolve(false)
    const id = nodeId.replace(/^system:/, '')
    // Tool row IDs are call IDs; both their call and result entries need bodies.
    const matches = this.options.store.current.allEntries.filter(entry =>
      entry.id === id || entry.message?.toolCallId === id
      || entry.message?.content?.some(content => content.type === 'toolCall' && content.id === id))
    return Promise.all((matches.length ? matches.map(entry => entry.id) : [id])
      .map(entryId => this.requestBody(s, entryId))).then(results => results.every(Boolean))
  }

  protect = (ids: string[]): void => {
    const s = this.scope
    if (!s || !this.valid(s)) return
    s.protectedIds = new Set(ids)
    clearTimeout(s.evictionTimer)
    // Settle the visible range before evicting overscan bodies or fold content.
    s.evictionTimer = setTimeout(() => {
      if (this.valid(s)) this.options.store.update(state => evictBodies(state, s.protectedIds))
    }, 500)
  }
}
