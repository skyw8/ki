import type { CompactTurn, Entry } from '../api/types'

/**
 * Body quality is independent of immutable entry identity and branch coverage.
 * Slim bodies contain every node with shortened payloads. Projection helpers
 * can omit entire assistant/tool nodes, so a contiguous helper ID chain is not
 * evidence that expanding a fold needs no fetch.
 */
export type BodyKind = 'index' | 'helper' | 'slim' | 'full'

export function bodyKind(entry: Entry): BodyKind {
  return entry.bodyKind ?? (entry.truncated ? 'slim' : 'full')
}

export function projectedBodies(entries: Entry[], turns: CompactTurn[] = []): Entry[] {
  const omitted = new Set(turns.flatMap(t => t.omittedNodeIds ?? []))
  return entries.map(entry => {
    return entry.truncated && entry.message?.role === 'assistant' && omitted.has(entry.id) && entry.bodyKind !== 'helper'
      ? { ...entry, bodyKind: 'helper' } : entry
  })
}

export function bodyRank(entry: Entry): number {
  return { index: 0, helper: 1, slim: 2, full: 3 }[bodyKind(entry)]
}

/** Coverage requires canonical parent edges and every node-bearing body. */
export function coveredTurns(entries: Entry[], turns: CompactTurn[]): string[] {
  const byId = new Map(entries.map(e => [e.id, e]))
  return turns.filter(turn => {
    let id: string | undefined = turn.tailId
    const seen = new Set<string>()
    let anchor = false
    while (id && !seen.has(id)) {
      seen.add(id)
      const entry = byId.get(id)
      if (!entry || bodyKind(entry) === 'index' || bodyKind(entry) === 'helper') return false
      anchor ||= id === turn.id || (entry.message?.role === 'toolResult' && entry.message.toolCallId === turn.id)
      if (anchor && (entry.parentId || undefined) === (turn.parentId || undefined)) return true
      id = entry.parentId
    }
    return false
  }).map(turn => turn.id)
}
