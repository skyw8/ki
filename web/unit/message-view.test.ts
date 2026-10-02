import { expect, test } from 'bun:test'
import { clampCompactKeep, foldReplies, groupTurns } from '../src/lib/messageView.ts'
import { hydrateEntries, loadHistory } from '../src/lib/model.ts'
import type { ChatNode, CompactTurn, Entry, Message, SessionDetail, ViewState } from '../src/api/types.ts'

const user = (id: string, text: string, origin?: string): ChatNode => ({ kind: 'user', id, text, content: [], origin })
const asst = (id: string, text = 'ok', streaming = false): ChatNode => ({ kind: 'assistant', id, text, streaming })
const tool = (id: string, running = false): ChatNode => ({ kind: 'tool', id, name: 'Bash', args: {}, running })

test('groupTurns includes an imported prelude in the first human turn like the server', () => {
  // A separate prelude group formerly shifted display ordinals away from the
  // server's compact turn ranges. The prelude remains in its original order.
  const turns = groupTurns([asst('a0'), user('u1', 'one'), asst('a1'), user('u2', 'two'), asst('a2')])
  expect(turns.map(t => t.id)).toEqual(['u1', 'u2'])
  expect(turns[0].user?.id).toBe('u1')
  expect(turns[0].nodes.map(n => n.id)).toEqual(['a0', 'u1', 'a1'])
  expect(turns[1].nodes.map(n => n.id)).toEqual(['u2', 'a2'])
})

test('foldReplies folds older runtime notifications without consuming keep slots', () => {
  const nodes = [
    user('u1', 'human'), asst('a1'),
    user('notice', '<task-notification>done</task-notification>', 'agent:task-1'), asst('a2'),
    user('u2', 'next human', 'extension:telegram-bot'), asst('a3'),
  ]
  const turns = groupTurns(nodes)
  expect(turns.map(t => t.id)).toEqual(['u1', 'u2'])
  expect(turns[0].nodes.map(n => n.id)).toEqual(['u1', 'a1', 'notice', 'a2'])

  const items = foldReplies(nodes, { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', 'u2', 'a3'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1', 'notice'])
  expect(folded?.kind === 'fold' ? folded.count : 0).toBe(2)
})

test('foldReplies folds the opening directive without consuming the machine-only reply slot', () => {
  const nodes = [user('directive', 'subagent directive', 'agent'), asst('answer')]
  const items = foldReplies(nodes, { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['fold:directive', 'answer'])
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['fold:directive'])
})

test('notices follow the kept reply suffix instead of occupying keep slots', () => {
  const nodes = [
    user('u', 'human'), asst('earlier'),
    user('middle', 'progress', 'agent:child'), asst('final'),
    user('trailing', 'done', 'agent:child'),
  ]
  for (const keep of [0, 1, 2]) {
    const items = foldReplies(nodes, { keep })
    expect(items.filter(i => i.kind === 'node' && i.node.kind === 'user').map(i => i.id))
      .toEqual(keep === 0 ? ['u'] : keep === 1 ? ['u', 'trailing'] : ['u', 'middle', 'trailing'])
    expect(items.filter(i => i.kind === 'fold').map(i => i.count)).toEqual(keep === 0 ? [4] : keep === 1 ? [2] : [])
    if (keep === 1) expect(items.map(i => i.id)).toEqual(['u', 'fold:u', 'final', 'trailing'])
    const expanded = foldReplies(nodes, { keep, expanded: new Set(['u']) })
    expect(expanded.filter(i => i.kind === 'node').map(i => i.id)).toEqual(nodes.map(n => n.id))
  }
  expect(foldReplies([user('notice', 'done', 'agent:child')], { keep: 0 }).map(i => i.id))
    .toEqual(['fold:notice'])
})

test('many middle notifications fold without hiding running work or creating extra keep slots', () => {
  const notices = Array.from({ length: 40 }, (_, i) => user(`notice-${i}`, 'report', 'agent:child'))
  const nodes = [user('u', 'human'), asst('older'), ...notices, tool('slow', true), tool('fast')]
  const items = foldReplies(nodes, { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u', 'fold:u', 'slow', 'fast'])
  expect(items.find(i => i.kind === 'fold')?.count).toBe(41)
  const settled = nodes.map(n => n.kind === 'tool' ? { ...n, running: false } : n)
  const final = foldReplies(settled, { keep: 1 })
  expect(final.map(i => i.id)).toEqual(['u', 'fold:u', 'fast'])
  expect(final.find(i => i.kind === 'fold')?.count).toBe(42)
})

test('foldReplies keeps every user bubble and the newest keep replies of each turn', () => {
  const nodes = [
    user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b'),
    user('u2', 'two'), asst('a2a'), tool('t2'), asst('a2b'),
  ]
  // One reply kept per turn: the fold row sits between the prompt and it.
  expect(foldReplies(nodes, { keep: 1 }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1b',
    'u2', 'fold:u2', 'a2b',
  ])
  const folded = foldReplies(nodes, { keep: 1 }).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a', 't1'])

  // keep 0 folds every reply, keep 2 leaves the short turns alone.
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'u2', 'fold:u2'])
  expect(foldReplies(nodes, { keep: 3 }).map(i => i.id)).toEqual(nodes.map(n => n.id))
  // A turn that is only a prompt has nothing to fold.
  expect(foldReplies([user('u1', 'one')], { keep: 0 }).map(i => i.id)).toEqual(['u1'])
})

test('foldReplies opens a fold in place without reordering the turn', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b')]
  expect(foldReplies(nodes, { keep: 1, expanded: new Set(['u1']) }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1a', 't1', 'a1b',
  ])
})

test('foldReplies keeps only the newest in-flight work visible', () => {
  // Trailing live work is what the user is waiting for.
  expect(foldReplies([user('u1', 'one'), asst('a1a'), tool('t1', true)], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1'])
  expect(foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b', 'ok', true)], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b'])
})

test('foldReplies keeps an earlier running sibling visible until lifecycle settlement', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('slow', true), tool('fast', false)]
  expect(foldReplies(nodes, { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'slow', 'fast'])
  const settled = nodes.map(n => n.kind === 'tool' ? { ...n, running: false } : n)
  expect(foldReplies(settled, { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'fast'])
})

const compact = (id: string, summary = 'sum'): ChatNode => ({ kind: 'compaction', id, summary })
const cancelled = (id: string): ChatNode => ({ kind: 'cancellation', id, reason: 'user_request', source: 'webui' })

test('foldReplies never folds a compaction row or lets it consume a keep slot', () => {
  // A checkpoint trailing the turn must not hide the newest reply.
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b'), compact('c1')], { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'c1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  // Even keep 0 leaves the compaction visible on its own row.
  expect(foldReplies([user('u1', 'one'), asst('a1a'), compact('c1')], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'c1'])

  // A mid-turn compaction (overflow) stays visible while older replies fold.
  expect(foldReplies([user('u1', 'one'), asst('a1'), compact('c1'), asst('a2')], { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'c1', 'a2'])
})

test('foldReplies never folds a cancellation row or lets it consume a keep slot', () => {
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b'), cancelled('x1')], { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'x1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  expect(foldReplies([user('u1', 'one'), asst('a1'), cancelled('x1')], { keep: 0 }).map(i => i.id))
    .toEqual(['u1', 'fold:u1', 'x1'])
})

test('foldReplies keeps a legacy aborted assistant visible for its cancellation row', () => {
  const aborted: ChatNode = { kind: 'assistant', id: 'a2', text: 'partial', stopReason: 'aborted' }
  expect(foldReplies([user('u1', 'one'), asst('a1'), aborted, cancelled('x1')], { keep: 0 }).map(i => i.id))
    .toEqual(['u1', 'fold:u1', 'a2', 'x1'])
})

const message = (id: string, parentId: string, role: string, extra: Partial<Message> = {}): Entry => ({
  type: 'message', id, parentId, message: { role, content: [{ type: 'text', text: id }], ...extra },
})

function sparseHistory(entries: Entry[], entryIds: string[], visibleNodeIds: string[], hiddenCount: number, extra: Partial<CompactTurn> = {}) {
  const summary: CompactTurn = {
    id: entries[0].id, tailId: entries.at(-1)!.id, entryIds, visibleNodeIds, hiddenCount,
    entryCount: entries.length, stepCount: 2,
    stats: {
      turn: 1, steps: 2, elapsedMs: 0, durationMs: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
      tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false, cost: 0, ttftMs: 0, tps: null, live: false,
    },
    ...extra,
  }
  const state = loadHistory({
    entries: entries.filter(e => entryIds.includes(e.id)), compactTurns: [summary],
    leafId: summary.tailId, oldestId: summary.id,
  } as SessionDetail)
  return { state, summary }
}

function compactItems(s: ViewState, keep = 1) {
  return foldReplies(s.nodes, { keep, summaries: s.compactTurns, loadedTurnIds: s.loadedTurnIds })
}

function foldedCount(s: ViewState, keep = 1) {
  return compactItems(s, keep).reduce((count, item) => count + (item.kind === 'fold' ? item.count : 0), 0)
}

test('compact reload excludes visible compaction metadata from the folded reply count', () => {
  // Matches BuildCompact's keep=1 projection: hiddenCount excludes c1, but
  // visibleNodeIds includes it because lifecycle metadata always renders.
  const entries = [
    message('u1', '', 'user'), message('a1', 'u1', 'assistant'), message('a2', 'a1', 'assistant'),
    { type: 'compaction', id: 'c1', parentId: 'a2', summary: 'sum' },
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a2', 'c1'], ['u1', 'a2', 'c1'], 1)
  expect(state.compactTurns?.[0].baselineNodes?.map(n => n.kind)).toEqual(['user', 'assistant', 'compaction'])
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', 'c1'])
  expect(foldedCount(state)).toBe(1)
  expect(foldedCount(state, 0)).toBe(2)

  const zeroKeep = sparseHistory(entries, ['u1', 'c1'], ['u1', 'c1'], 2).state
  expect(compactItems(zeroKeep, 0).map(i => i.id)).toEqual(['u1', 'fold:u1', 'c1'])
  expect(foldedCount(zeroKeep, 0)).toBe(2)

  const hydrated = hydrateEntries(state, entries, { compactTurns: [summary] })
  expect(hydrated.loadedTurnIds).toContain('u1')
  expect(foldedCount(hydrated)).toBe(1)
  expect(compactItems(hydrated).find(i => i.kind === 'fold')?.remote).toBe(false)
})

test('compact metadata alone never creates a phantom remote fold', () => {
  const entries = [
    message('u1', '', 'user'), message('a1', 'u1', 'assistant'),
    { type: 'compaction', id: 'c1', parentId: 'a1', summary: 'sum' },
  ]
  const { state } = sparseHistory(entries, ['u1', 'a1', 'c1'], ['u1', 'a1', 'c1'], 0)
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'a1', 'c1'])
  expect(foldedCount(state)).toBe(0)
})

for (const variant of ['event', 'aborted-event', 'legacy-aborted']) {
  test(`compact reload excludes cancellation metadata: ${variant}`, () => {
    const entries: Entry[] = [
      message('u1', '', 'user'), message('a1', 'u1', 'assistant'), message('a2', 'a1', 'assistant'),
    ]
    const visible = ['u1', 'a2']
    if (variant !== 'event') {
      entries.push(message('a3', 'a2', 'assistant', { stopReason: 'aborted' }))
      visible.push('a3')
    }
    if (variant !== 'legacy-aborted') {
      entries.push({ type: 'run_aborted', id: 'x1', parentId: entries.at(-1)!.id, details: { reason: 'user_request', source: 'webui' } })
      visible.push('x1')
    }
    const { state } = sparseHistory(entries, visible, visible, 1)
    const renderedMetadata = variant === 'legacy-aborted' ? ['a3', 'legacy-cancellation:a3'] : visible.slice(2)
    expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', ...renderedMetadata])
    expect(foldedCount(state)).toBe(1)
    expect(foldedCount(state, 0)).toBe(2)
  })
}

test('compact reload retains the final reply plus trailing notification without consuming a slot', () => {
  const entries = [
    message('u1', '', 'user'), message('a1', 'u1', 'assistant'),
    message('notice', 'a1', 'user', { origin: 'agent:task-1' }),
  ]
  const { state } = sparseHistory(entries, ['u1', 'a1', 'notice'], ['u1', 'a1', 'notice'], 0)
  expect(state.compactTurns?.[0].baselineNodes?.find(n => n.id === 'notice')?.kind).toBe('user')
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'a1', 'notice'])
  expect(foldedCount(state)).toBe(0)
  expect(foldedCount(state, 0)).toBe(2)
  expect(compactItems(state, 0).map(i => i.id)).toEqual(['u1', 'fold:u1'])
})

test('partial notification hydration preserves fold counts and sparse parent links', () => {
  const entries = [
    message('u1', '', 'user'), message('a1', 'u1', 'assistant'),
    message('notice', 'a1', 'user', { origin: 'agent:task-1' }), message('a2', 'notice', 'assistant'),
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a2'], ['u1', 'a2'], 2)
  expect(foldedCount(state)).toBe(2)
  // Loading a single hidden body must not break the existing sparse edge
  // when its own parent is still omitted from the immutable snapshot.
  const bodyOnly = hydrateEntries(state, [entries[2]])
  expect(bodyOnly.nodes.map(n => n.id)).toEqual(['u1', 'notice', 'a2'])
  expect(bodyOnly.compactTurns?.[0].baselineNodes?.map(n => n.id)).toEqual(['u1', 'notice', 'a2'])
  expect(foldedCount(bodyOnly)).toBe(2)
  // A keep=2 reprojection exposes the older a1 as well as the notice.
  // The browser's keep=1 fold counts a1 plus the older notice.
  const reprojected = { ...summary, entryIds: entries.map(e => e.id), visibleNodeIds: entries.map(e => e.id), hiddenCount: 0 }
  const partial = hydrateEntries(bodyOnly, [entries[1]], { compactTurns: [reprojected] })
  expect(partial.loadedTurnIds).toContain('u1')
  expect(partial.compactTurns?.[0].baselineNodes?.map(n => n.id)).toEqual(['u1', 'a1', 'notice', 'a2'])
  expect(foldedCount(partial)).toBe(2)
  expect(compactItems(partial).find(i => i.kind === 'fold')?.remote).toBe(false)

  const complete = hydrateEntries(partial, [entries[1]], { compactTurns: [summary] })
  expect(complete.loadedTurnIds).toContain('u1')
  expect(foldedCount(complete)).toBe(2)
  expect(compactItems(complete).find(i => i.kind === 'fold')?.remote).toBe(false)
})

test('a sparse machine-only fold counts its hidden directive anchor without consuming keep', () => {
  const entries = [
    message('directive', '', 'user', { origin: 'agent' }),
    message('a1', 'directive', 'assistant'), message('a2', 'a1', 'assistant'),
  ]
  const { state } = sparseHistory(entries, ['directive', 'a2'], ['a2'], 2, { stats: {
    turn: 0, steps: 2, elapsedMs: 0, durationMs: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
    tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false, cost: 0, ttftMs: 0, tps: null, live: false,
  } })
  expect(compactItems(state).map(i => i.id)).toEqual(['fold:directive', 'a2'])
  expect(foldedCount(state)).toBe(2)
  expect(foldedCount(state, 0)).toBe(3)
})

test('compact tool projection counts distinct replies rather than retained entry bodies', () => {
  const entries = [
    message('u1', '', 'user'),
    message('a1', 'u1', 'assistant', { content: [
      { type: 'toolCall', id: 't1', name: 'Bash', arguments: {} },
      { type: 'toolCall', id: 't2', name: 'Read', arguments: {} },
    ] }),
    message('r1', 'a1', 'toolResult', { toolCallId: 't1', toolName: 'Bash' }),
    message('r2', 'r1', 'toolResult', { toolCallId: 't2', toolName: 'Read' }),
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a1', 'r2'], ['u1', 't2'], 2, {
    omittedNodeIds: ['a1', 't1'], toolStates: [{ id: 't1', finished: true }, { id: 't2', finished: true }],
  })
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 't2'])
  expect(foldedCount(state)).toBe(2)
  const hydrated = hydrateEntries(state, entries, { compactTurns: [summary] })
  expect(hydrated.nodes.map(n => n.id)).toEqual(['u1', 'a1', 't1', 't2'])
  expect(foldedCount(hydrated)).toBe(2)
})

test('clampCompactKeep bounds the configured N', () => {
  expect(clampCompactKeep(Number.NaN)).toBe(1)
  expect(clampCompactKeep(-3)).toBe(0)
  expect(clampCompactKeep(2.6)).toBe(3)
  expect(clampCompactKeep(99)).toBe(20)
})

