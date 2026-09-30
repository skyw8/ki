import { expect, test } from 'bun:test'
import { applyTail, hydrateEntries, loadHistory, projectTurnStats, sessionStats } from '../src/lib/model.ts'
import type { CompactTurn, Entry, SessionDetail } from '../src/api/types.ts'

function projection(ordinal: number, id: string, tailId: string, parentId?: string, keep = 1) {
  const entries: Entry[] = [
    { type: 'message', id, parentId, message: { role: 'user', content: [{ type: 'text', text: id }] } },
    ...(keep ? [{ type: 'message', id: tailId, parentId: id, message: { role: 'assistant', content: [{ type: 'text', text: tailId }] } } satisfies Entry] : []),
  ]
  const summary: CompactTurn = {
    id, parentId, tailId, entryCount: 2, entryIds: entries.map(e => e.id),
    visibleNodeIds: entries.map(e => e.id), hiddenCount: keep ? 0 : 1,
    firstHiddenId: keep ? undefined : tailId, toolStates: [], stepCount: ordinal,
    cumulativeElapsedMs: 0,
    stats: {
      turn: ordinal, steps: 1, elapsedMs: 0, durationMs: 0,
      input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
      tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false,
      cost: 0, ttftMs: 0, tps: null, live: false,
    },
  }
  return { entries, summary }
}

function detail(overrides: Partial<SessionDetail>): SessionDetail {
  return { id: 'session', cwd: '.', provider: 'provider', model: 'model', title: 'session', ...overrides }
}

function suffix(parent: string, keep = 1) {
  return Array.from({ length: 4 }, (_, i) => {
    const ordinal = i + 3
    return projection(ordinal, `u${ordinal}`, `a${ordinal}`, i === 0 ? parent : `a${ordinal - 1}`, keep)
  })
}

test('compact recovery never treats consecutive turn numbers as proof of a shared branch', () => {
  const first = projection(1, 'u1', 'a1')
  const oldSecond = projection(2, 'old-u2', 'old-a2', 'a1')
  const old = loadHistory(detail({
    entries: [...first.entries, ...oldSecond.entries],
    compactTurns: [first.summary, oldSecond.summary],
    leafId: 'old-a2', oldestId: 'u1', hasMore: false,
  }))
  // The server changed its active branch while the client was away. Its four
  // newest turns start after a replacement turn 2 that this client never read.
  const incoming = suffix('new-a2')
  const recovered = applyTail(old, detail({
    entries: incoming.flatMap(t => t.entries), compactTurns: incoming.map(t => t.summary),
    leafId: 'a6', oldestId: 'u3', hasMore: true,
  }))

  expect(recovered.oldestId).toBe('u3')
  expect(recovered.hasMore).toBe(true)
  expect(recovered.nodes.map(n => n.id)).toEqual(incoming.flatMap(t => t.entries.map(e => e.id)))
  // Incompatible metadata must not become a synthetic ancestry edge again
  // when a later page fills the gap. Cached immutable bodies may remain.
  expect(recovered.compactTurns?.some(t => t.id === 'old-u2')).toBe(false)

  const replacement = projection(2, 'new-u2', 'new-a2', 'a1')
  let paged = hydrateEntries(recovered, replacement.entries, {
    compactTurns: [replacement.summary], oldestId: 'new-u2', hasMore: true,
  })
  paged = hydrateEntries(paged, first.entries, {
    compactTurns: [first.summary], oldestId: 'u1', hasMore: false,
  })
  expect(paged.nodes.map(n => n.id)).toEqual([
    ...first.entries, ...replacement.entries, ...incoming.flatMap(t => t.entries),
  ].map(e => e.id))
  expect(paged.hasMore).toBe(false)
  expect(paged.oldestId).toBe('u1')
})

test('same-branch compact recovery retains its root across canonically adjacent hidden tails', () => {
  const first = projection(1, 'u1', 'a1', undefined, 0)
  const second = projection(2, 'u2', 'a2', 'a1', 0)
  const old = loadHistory(detail({
    entries: [...first.entries, ...second.entries],
    compactTurns: [first.summary, second.summary],
    leafId: 'a2', oldestId: 'u1', hasMore: false,
  }))
  // keep=0 omits every assistant body, but each next turn's parent still
  // matches the preceding snapshot tail. That is a valid sparse connection.
  const incoming = suffix('a2', 0)
  const recovered = applyTail(old, detail({
    entries: incoming.flatMap(t => t.entries), compactTurns: incoming.map(t => t.summary),
    leafId: 'a6', oldestId: 'u3', hasMore: true,
  }))

  expect(recovered.oldestId).toBe('u1')
  expect(recovered.hasMore).toBe(false)
  expect(recovered.nodes.map(n => n.id)).toEqual(['u1', 'u2', 'u3', 'u4', 'u5', 'u6'])
  expect(recovered.compactTurns?.map(t => t.id)).toEqual(['u1', 'u2', 'u3', 'u4', 'u5', 'u6'])
})

test('a connected branch switch prunes sibling summaries from turn and session statistics', () => {
  const first = projection(1, 'u1', 'a1')
  const sibling = projection(2, 'old-u2', 'old-a2', 'a1')
  const replacement = projection(2, 'new-u2', 'new-a2', 'a1')
  first.summary.stats = { ...first.summary.stats, hasCost: true, cost: 1 }
  sibling.summary.stats = { ...sibling.summary.stats, hasCost: true, cost: 10 }
  replacement.summary.stats = { ...replacement.summary.stats, hasCost: true, cost: 20 }
  let state = loadHistory(detail({
    entries: [...first.entries, ...sibling.entries], compactTurns: [first.summary, sibling.summary],
    leafId: 'old-a2', oldestId: 'u1', hasMore: false,
  }))
  state = applyTail(state, detail({
    entries: replacement.entries, compactTurns: [replacement.summary],
    leafId: 'new-a2', oldestId: 'new-u2', hasMore: true,
  }))
  expect(state.oldestId).toBe('u1')
  expect(state.hasMore).toBe(false)
  expect(state.compactTurns?.map(t => t.id)).toEqual(['u1', 'new-u2'])
  expect(projectTurnStats(state.nodes, state.turnBase, state.compactTurns).get('new-a2')).toMatchObject({ steps: 1, cost: 20 })
  expect(sessionStats(state).cost).toBe(21)
})

test('an authoritative same-turn rewind replaces its longer snapshot even with hidden leaves', () => {
  for (const keep of [0, 1]) {
    const selected = projection(1, 'u1', 'a1', undefined, keep)
    const old = projection(1, 'u1', 'a2', undefined, keep)
    if (keep) old.entries[1].parentId = 'a1'
    old.summary = { ...old.summary, entryCount: 3, hiddenCount: keep ? 1 : 2,
      stepCount: 2, stats: { ...old.summary.stats, steps: 2 } }
    let state = loadHistory(detail({ entries: old.entries, compactTurns: [old.summary],
      leafId: 'a2', oldestId: 'u1', hasMore: false }))
    state = applyTail(state, detail({ entries: selected.entries, compactTurns: [selected.summary],
      leafId: 'a1', oldestId: 'u1', hasMore: false }))
    expect(state.nodes.map(n => n.id)).toEqual(keep ? ['u1', 'a1'] : ['u1'])
    expect(state.compactTurns?.[0]).toMatchObject({ tailId: 'a1', entryCount: 2, hiddenCount: keep ? 0 : 1 })
    expect(sessionStats(state).steps).toBe(1)
  }
})
