import type { Entry, LoopEvent, SessionDetail, ViewState } from '../api/types'
import { applyEvent, applyIndex, applyTail, compactBoundary, emptyView, hydrateEntries, hydrateTurn, leafEntries } from './model'

export type TranscriptRequestKind = 'tail' | 'page' | 'index' | 'body' | 'turn' | 'keep' | 'compact'

/** A local request capability, not a server branch identifier. */
export type TranscriptTicket = {
  kind: TranscriptRequestKind
  sessionId: string | null
  epoch: number
  frontier: number
  leafId?: string
  oldestId?: string
  hasMore: boolean
  liveRevision: number
  turnId?: string
  turnTailId?: string
}

export type ViewUpdate = ViewState | ((current: ViewState) => ViewState)

/**
 * The transcript's atomic commit boundary lives outside React scheduling.
 * Requests observe exactly the state that has committed, even before React
 * renders it. Payload hydration cannot grant authority over branch coverage.
 */
export class TranscriptStore {
  private value: ViewState
  private id: string | null
  private epoch = 0
  private frontier = 0
  private listeners = new Set<() => void>()

  constructor(initial = emptyView(), sessionId: string | null = null) {
    this.value = initial
    this.id = sessionId
  }

  get current(): ViewState { return this.value }
  get sessionId(): string | null { return this.id }
  getSnapshot = (): ViewState => this.value
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  update = (update: ViewUpdate): void => {
    const next = typeof update === 'function' ? update(this.value) : update
    if (next === this.value) return
    this.value = next
    for (const listener of this.listeners) listener()
  }

  /** Opening even the same session again revokes every old response. */
  reset(sessionId: string | null, initial: ViewState): void {
    this.epoch++
    this.frontier++
    this.id = sessionId
    // Even a same-object reset changes request authority. Controllers must
    // hear it although React may correctly skip rendering an identical view.
    this.value = initial
    for (const listener of this.listeners) listener()
  }

  capture(kind: TranscriptRequestKind, turnId?: string): TranscriptTicket {
    const state = this.value
    return {
      kind, sessionId: this.id, epoch: this.epoch, frontier: this.frontier,
      leafId: state.leafId, oldestId: state.oldestId, hasMore: !!state.hasMore,
      liveRevision: state.liveRevision, turnId,
      turnTailId: state.compactTurns?.find(turn => turn.id === turnId)?.tailId,
    }
  }

  isCurrent(ticket: TranscriptTicket): boolean {
    return ticket.epoch === this.epoch && ticket.sessionId === this.id
  }

  canCommit(ticket: TranscriptTicket): boolean {
    if (!this.isCurrent(ticket)) return false
    if (ticket.kind === 'body' || ticket.kind === 'index') return true
    if (ticket.frontier !== this.frontier) return false
    if (ticket.kind === 'page' || ticket.kind === 'compact') {
      if (ticket.oldestId !== this.value.oldestId || ticket.hasMore !== !!this.value.hasMore) return false
    }
    if (ticket.kind === 'compact' && ticket.leafId !== this.value.leafId) return false
    if (ticket.kind === 'keep' || ticket.kind === 'turn') {
      if (ticket.turnTailId !== this.value.compactTurns?.find(turn => turn.id === ticket.turnId)?.tailId) return false
    }
    return true
  }

  /** No promise resolution or network acknowledgement runs inside a reducer. */
  commit(ticket: TranscriptTicket, detail: SessionDetail, options?: { rejectedCursor?: string }): boolean {
    if (!this.canCommit(ticket)) return false
    const state = this.value
    let next: ViewState
    switch (ticket.kind) {
      case 'tail':
        next = applyTail(state, detail, ticket.liveRevision)
        if (options?.rejectedCursor && state.oldestId === options.rejectedCursor && state.liveRevision === ticket.liveRevision) {
          next = { ...next, oldestId: detail.oldestId, hasMore: !!detail.hasMore }
        }
        // A confirmed snapshot revokes projections issued against its
        // predecessor, even if both snapshots share the same human input.
        this.frontier++
        break
      case 'page':
        next = hydrateEntries(state, detail.entries ?? [], {
          oldestId: detail.oldestId ?? ticket.oldestId,
          hasMore: !!detail.hasMore,
          compactTurns: detail.compactTurns,
        })
        break
      case 'compact': {
        // The server may select another sibling while the request travels.
        // A stable local ticket alone cannot authorize that response's tail.
        // Only recovery can choose a previously unknown branch frontier.
        const active = new Set(leafEntries(state.allEntries, state.leafId, state.compactTurns, false).map(entry => entry.id))
        if (!detail.compactTurns?.length || detail.compactTurns.some(turn => !active.has(turn.tailId))) return false
        next = compactBoundary(state, detail, ticket)
        break
      }
      case 'keep': {
        const turn = detail.compactTurns?.find(turn => turn.id === ticket.turnId)
        // A projection of a different tail is not authority to choose it.
        // The controller must recover the selected branch before retrying.
        if (!turn || turn.tailId !== ticket.turnTailId) return false
        next = hydrateEntries(state, detail.entries ?? [], { compactTurns: [turn] })
        break
      }
      case 'turn':
        if (!ticket.turnId || (ticket.turnTailId && !(detail.entries ?? []).some(entry => entry.id === ticket.turnTailId))) return false
        next = hydrateTurn(state, ticket.turnId, detail.entries ?? [])
        break
      case 'index':
        next = applyIndex(state, detail)
        break
      case 'body':
        next = hydrateEntries(state, detail.entries ?? [])
        break
    }
    this.update(next)
    return true
  }

  applyEvents(events: LoopEvent[]): void {
    this.update(state => events.reduce(applyEvent, state))
  }

  hydrate(entries: Entry[]): void {
    this.commit(this.capture('body'), { id: this.id ?? '', entries })
  }
}
