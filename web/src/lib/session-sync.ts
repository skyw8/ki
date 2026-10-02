import type { Client } from '../api/client'
import type { SessionDetail } from '../api/types'
import { isNetworkError } from './errors'
import { reconnectDelay, streamBatch, waitForReconnect, type FrameClock } from './stream-batch'
import { TranscriptStore } from './transcript-store'

export type SyncPhase = 'idle' | 'listening' | 'recovering' | 'retrying' | 'paused' | 'error'
export type SyncStatus = { sessionId: string | null; phase: SyncPhase }
type Job = { abort: AbortController; ready: Promise<void>; resolve: () => void }
type SyncOptions = {
  transcriptOptions: () => { view?: 'compact'; keep?: number }
  onListening?: (id: string) => void
  onSnapshot?: (id: string, detail: SessionDetail) => void
  onSettled?: () => void
  onError?: (error: unknown) => void
  clock?: FrameClock
  wait?: (ms: number, signal: AbortSignal) => Promise<void>
}

/**
 * One run reader/reconciler per open transcript. Lifecycle, push invalidation,
 * EOF and failed transport all use this state machine, never competing loops.
 * Reconnection only reads snapshots/events; it has no prompt-writing API.
 */
export class SessionSyncController {
  private id: string | null = null
  private paused = false
  private active = true
  private job?: Job
  private cursor?: string
  private runningHint = 0
  private status: SyncStatus = { sessionId: null, phase: 'idle' }
  private listeners = new Set<() => void>()

  constructor(
    private api: Pick<Client, 'get' | 'events'>,
    private store: TranscriptStore,
    private options: SyncOptions,
  ) {}

  getSnapshot = (): SyncStatus => this.status
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  private phase(phase: SyncPhase): void {
    if (this.status.sessionId === this.id && this.status.phase === phase) return
    this.status = { sessionId: this.id, phase }
    for (const listener of this.listeners) listener()
  }

  private stop(): void {
    const job = this.job
    this.job = undefined
    job?.abort.abort()
    job?.resolve()
  }

  /** A navigation is explicit, including reopening the same selected branch. */
  select(id: string | null): void {
    if (!this.active) return
    this.stop()
    this.id = id
    // An ACK is a capability backed by applied state, not a session bookmark.
    // Opening discards transient overlays; replay must reconstruct them.
    this.cursor = undefined
    // Navigation is not a page lifecycle resume. A delayed opening response
    // must never reopen a socket after pagehide or bfcache suspension.
    this.phase(this.paused ? 'paused' : 'idle')
  }

  isListening(id: string): boolean {
    return this.id === id && this.status.phase === 'listening' && !!this.job && !this.job.abort.signal.aborted
  }

  /** A running hint cannot reopen subscriptions while the page is in bfcache. */
  listen(id: string, through?: string): Promise<void> {
    if (!this.active || this.id !== id || this.paused || this.store.sessionId !== id) return Promise.resolve()
    this.runningHint++
    if (this.job && !['idle', 'error'].includes(this.status.phase)) return this.job.ready
    return this.launch(false, through)
  }

  recover(restart = false): Promise<void> {
    if (!this.active || !this.id || this.paused || this.store.sessionId !== this.id) return Promise.resolve()
    if (!restart && this.job && ['recovering', 'retrying'].includes(this.status.phase)) return this.job.ready
    return this.launch(true)
  }

  resume(): Promise<void> {
    if (!this.active) return Promise.resolve()
    this.paused = false
    return this.recover(true)
  }

  suspend(): void {
    this.paused = true
    this.stop()
    this.phase('paused')
  }

  dispose(): void {
    this.active = false
    this.stop()
    this.listeners.clear()
  }

  /** Restartable effect lifetime; only mounting, never a network callback. */
  activate(): void { this.active = true }

  private launch(recover: boolean, through?: string): Promise<void> {
    this.stop()
    let resolve!: () => void
    const ready = new Promise<void>(done => { resolve = done })
    const job: Job = { abort: new AbortController(), ready, resolve }
    this.job = job
    void this.run(job, recover, through).catch(error => {
      if (this.job !== job || job.abort.signal.aborted) return
      this.phase('error')
      this.options.onError?.(error)
    }).finally(() => {
      job.resolve()
      if (this.job === job) this.job = undefined
    })
    return ready
  }

  private async run(job: Job, recovering: boolean, through?: string): Promise<void> {
    const id = this.id!
    const signal = job.abort.signal
    const scope = this.store.capture('body')
    const valid = () => this.active && this.job === job && !signal.aborted && !this.paused && this.id === id && this.store.isCurrent(scope)
    const wait = this.options.wait ?? waitForReconnect
    let attempt = 0
    let streamEnded = false
    while (valid()) {
      if (recovering) {
        this.phase('recovering')
        const ticket = this.store.capture('tail')
        const runningHint = this.runningHint
        let detail: SessionDetail
        try {
          detail = await this.api.get(id, { ...this.options.transcriptOptions(), signal })
        } catch (error) {
          if (!valid()) return
          if (!isNetworkError(error)) {
            this.phase('error')
            this.options.onError?.(error)
            return
          }
          this.phase('retrying')
          await wait(reconnectDelay(attempt++), signal).catch(() => {})
          // A failed history read must not also strand a known live run.
          // Retain its applied state/ACK and try the reader again; its next
          // close goes through this same reconciliation path. An idle/empty
          // replica has no such evidence and continues retrying the snapshot.
          if (valid() && this.store.current.busy) recovering = false
          continue
        }
        if (!valid()) return
        // A list/accepted-run hint received after this request began may
        // describe a new run. Confirm it rather than stopping on an old idle
        // response, or canceling recovery before it can fill missed history.
        if (!detail.running && runningHint !== this.runningHint) continue
        if (!this.store.commit(ticket, detail)) continue
        // Sideband or other committed work can make this GET too old to
        // settle execution. Do not leave a busy replica without a reader:
        // acquire runtime authority with a fresh snapshot first.
        if (!!detail.running !== this.store.current.busy) continue
        this.options.onSnapshot?.(id, detail)
        job.resolve()
        if (!detail.running) {
          this.phase('idle')
          this.options.onSettled?.()
          return
        }
        through = detail.leafId
        // Repeated short EOFs are a broken transport, not completed work.
        // Bound reconnects without delaying the first authoritative tail read.
        if (streamEnded) {
          this.phase('retrying')
          await wait(reconnectDelay(attempt++), signal).catch(() => {})
          if (!valid()) return
        }
      }
      this.phase('listening')
      this.options.onListening?.(id)
      job.resolve()
      const connectedAt = performance.now()
      // Detailed tails also omit older bodies. Without snapshot coverage a
      // reconnect replays every old notification as fresh transcript content.
      const replayThrough = through ?? this.store.current.leafId
      const batcher = streamBatch(events => {
        if (!valid()) return
        this.store.applyEvents(events)
        // A cursor advances only after the synchronous atomic commit, not
        // receipt, rAF scheduling or a deferred React state updater.
        const last = events.at(-1)
        if (last?.runId && last.seq !== undefined) this.cursor = `${last.runId}:${last.seq}`
      }, this.options.clock)
      try {
        for await (const event of this.api.events(id, signal, this.cursor, replayThrough)) {
          if (!valid()) break
          batcher.enqueue(event)
        }
      } catch {
        // The same snapshot path handles EOF and transport/read errors.
        // HTTP/auth failures are reported by the authoritative GET below.
      } finally {
        batcher.flush()
        batcher.dispose()
      }
      if (!valid()) return
      if (performance.now() - connectedAt >= 5000) attempt = 0
      recovering = streamEnded = true
      this.options.onSettled?.()
    }
  }
}
