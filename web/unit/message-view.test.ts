import { expect, test } from 'bun:test'
import { COMPACT_VISIBLE_TEXT_CHARS, foldReplies, groupTurns, loadMessageView } from '../src/lib/messageView.ts'
import { hydrateEntries, loadHistory } from '../src/lib/model.ts'
import type { ChatNode, CompactTurn, Entry, Message, SessionDetail, ViewState } from '../src/api/types.ts'

test('message view defaults to compact and respects saved choices', () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const saved = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: { getItem: (key: string) => saved.get(key) ?? null },
  })
  try {
    expect(loadMessageView()).toEqual({ mode: 'compact' })
    saved.set('ki-message-view', 'detailed')
    expect(loadMessageView()).toEqual({ mode: 'detailed' })
    saved.set('ki-message-view', 'compact')
    expect(loadMessageView()).toEqual({ mode: 'compact' })
    saved.set('ki-message-view', 'invalid')
    expect(loadMessageView()).toEqual({ mode: 'compact' })
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get: () => { throw new Error('storage unavailable') },
    })
    expect(loadMessageView()).toEqual({ mode: 'compact' })
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  }
})

const prose = (chars = COMPACT_VISIBLE_TEXT_CHARS) => 'p'.repeat(chars)
const user = (id: string, text: string, origin?: string): ChatNode => ({ kind: 'user', id, text, content: [], origin })
const asst = (id: string, text = 'ok', streaming = false): ChatNode => ({ kind: 'assistant', id, text, streaming })
const tool = (id: string, running = false): ChatNode => ({ kind: 'tool', id, name: 'Bash', args: {}, running })

test('groupTurns includes an imported prelude in the first human turn like the server', () => {
  const turns = groupTurns([asst('a0'), user('u1', 'one'), asst('a1'), user('u2', 'two'), asst('a2')])
  expect(turns.map(t => t.id)).toEqual(['u1', 'u2'])
  expect(turns[0].user?.id).toBe('u1')
  expect(turns[0].nodes.map(n => n.id)).toEqual(['a0', 'u1', 'a1'])
  expect(turns[1].nodes.map(n => n.id)).toEqual(['u2', 'a2'])
})

test('foldReplies shows only assistant prose at or above the visible threshold', () => {
  expect(COMPACT_VISIBLE_TEXT_CHARS).toBe(50)
  const short = asst('short', prose(COMPACT_VISIBLE_TEXT_CHARS - 1))
  const exact = asst('exact', prose(COMPACT_VISIBLE_TEXT_CHARS))
  expect(foldReplies([user('u', 'human'), short, exact], {}).map(i => i.id))
    .toEqual(['u', 'fold:u', 'exact'])
  const folded = foldReplies([user('u', 'human'), short, exact], {}).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['short'])
})

test('foldReplies counts characters rather than UTF-16 units', () => {
  const emoji = '😀'.repeat(COMPACT_VISIBLE_TEXT_CHARS)
  expect(foldReplies([user('u', 'human'), asst('a', emoji)], {}).map(i => i.id)).toEqual(['u', 'a'])
  expect(foldReplies([user('u', 'human'), asst('a', '😀'.repeat(COMPACT_VISIBLE_TEXT_CHARS - 1)), asst('b', prose())], {}).map(i => i.id))
    .toEqual(['u', 'fold:u', 'b'])
  expect(foldReplies([user('u', 'human'), asst('short', '😀'.repeat(COMPACT_VISIBLE_TEXT_CHARS - 1))], {}).map(i => i.id))
    .toEqual(['u', 'short'])
})

test('foldReplies folds tools, runtime notifications and short replies', () => {
  const nodes = [
    user('u1', 'human'), asst('a1'),
    user('notice', '<task-notification>done</task-notification>', 'agent:task-1'), tool('t1'),
    asst('a2', prose()), user('u2', 'next human', 'extension:telegram-bot'), asst('a3'),
  ]
  const turns = groupTurns(nodes)
  expect(turns.map(t => t.id)).toEqual(['u1', 'u2'])
  expect(turns[0].nodes.map(n => n.id)).toEqual(['u1', 'a1', 'notice', 't1', 'a2'])

  const items = foldReplies(nodes, {})
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', 'u2', 'a3'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1', 'notice', 't1'])
  expect(folded?.kind === 'fold' ? folded.count : 0).toBe(3)
})

test('foldReplies folds an opening directive while keeping its long answer', () => {
  const nodes = [user('directive', 'subagent directive', 'agent'), asst('answer', prose())]
  expect(foldReplies(nodes, {}).map(i => i.id)).toEqual(['fold:directive', 'answer'])
  expect(foldReplies([user('directive', 'subagent directive', 'agent'), asst('answer')], {}).map(i => i.id))
    .toEqual(['fold:directive', 'answer'])
})

test('foldReplies keeps every user bubble and folds short replies per turn', () => {
  const nodes = [
    user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b', prose()),
    user('u2', 'two'), asst('a2a'), tool('t2'), asst('a2b'),
  ]
  expect(foldReplies(nodes, {}).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1b',
    'u2', 'fold:u2', 'a2b',
  ])
  const folded = foldReplies(nodes, {}).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a', 't1'])
  expect(foldReplies([user('u1', 'one')], {}).map(i => i.id)).toEqual(['u1'])
})

test('foldReplies folds nodes interleaved after visible prose into the same row', () => {
  const nodes = [user('u1', 'one'), asst('long', prose()), tool('t1'), asst('short')]
  expect(foldReplies(nodes, {}).map(i => i.id)).toEqual(['u1', 'fold:u1', 'long', 'short'])
  const folded = foldReplies(nodes, {}).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['t1'])
})

test('foldReplies opens a fold in place without reordering the turn', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b', prose())]
  expect(foldReplies(nodes, { expanded: new Set(['u1']) }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1a', 't1', 'a1b',
  ])
})

test('foldReplies keeps in-flight work visible', () => {
  expect(foldReplies([user('u1', 'one'), asst('a1a'), tool('t1', true)], {}).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1'])
  expect(foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b', 'ok', true)], {}).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b'])
})

test('foldReplies keeps an earlier running sibling visible until lifecycle settlement', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('slow', true), tool('fast', false)]
  expect(foldReplies(nodes, {}).map(i => i.id)).toEqual(['u1', 'fold:u1', 'slow', 'fast'])
  const settled = nodes.map(n => n.kind === 'tool' ? { ...n, running: false } : n)
  expect(foldReplies(settled, {}).map(i => i.id)).toEqual(['u1', 'fold:u1'])
})

const compact = (id: string, summary = 'sum'): ChatNode => ({ kind: 'compaction', id, summary })
const cancelled = (id: string): ChatNode => ({ kind: 'cancellation', id, reason: 'user_request', source: 'webui' })

test('foldReplies never folds a compaction row', () => {
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b', prose()), compact('c1')], {})
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'c1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  expect(foldReplies([user('u1', 'one'), asst('a1a'), compact('c1')], {}).map(i => i.id))
    .toEqual(['u1', 'a1a', 'c1'])
  expect(foldReplies([user('u1', 'one'), asst('a1', prose()), compact('c1'), asst('a2')], {}).map(i => i.id))
    .toEqual(['u1', 'a1', 'c1', 'a2'])
})

test('foldReplies never folds a cancellation row', () => {
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b', prose()), cancelled('x1')], {})
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'x1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  expect(foldReplies([user('u1', 'one'), asst('a1'), cancelled('x1')], {}).map(i => i.id))
    .toEqual(['u1', 'a1', 'x1'])
})

test('foldReplies keeps a legacy aborted assistant visible for its cancellation row', () => {
  const aborted: ChatNode = { kind: 'assistant', id: 'a2', text: 'partial', stopReason: 'aborted' }
  expect(foldReplies([user('u1', 'one'), asst('a1'), aborted, cancelled('x1')], {}).map(i => i.id))
    .toEqual(['u1', 'fold:u1', 'a2', 'x1'])
})

const message = (id: string, parentId: string, role: string, extra: Partial<Message> = {}): Entry => ({
  type: 'message', id, parentId, message: { role, content: [{ type: 'text', text: id }], ...extra },
})

const proseMessage = (id: string, parentId: string, extra: Partial<Message> = {}): Entry =>
  message(id, parentId, 'assistant', { content: [{ type: 'text', text: prose() }], ...extra })

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

function compactItems(s: ViewState) {
  return foldReplies(s.nodes, { summaries: s.compactTurns, loadedTurnIds: s.loadedTurnIds })
}

function foldedCount(s: ViewState) {
  return compactItems(s).reduce((count, item) => count + (item.kind === 'fold' ? item.count : 0), 0)
}

test('compact reload keeps visible long prose and excludes compaction metadata from the fold count', () => {
  const entries = [
    message('u1', '', 'user'), message('a1', 'u1', 'assistant'), proseMessage('a2', 'a1'),
    { type: 'compaction', id: 'c1', parentId: 'a2', summary: 'sum' },
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a2', 'c1'], ['u1', 'a2', 'c1'], 1)
  expect(state.compactTurns?.[0].baselineNodes?.map(n => n.kind)).toEqual(['user', 'assistant', 'compaction'])
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', 'c1'])
  expect(foldedCount(state)).toBe(1)

  const hydrated = hydrateEntries(state, entries, { compactTurns: [summary] })
  expect(hydrated.loadedTurnIds).toContain('u1')
  expect(foldedCount(hydrated)).toBe(1)
  expect(compactItems(hydrated).find(i => i.kind === 'fold')?.remote).toBe(false)
})

test('compact metadata alone never creates a phantom remote fold', () => {
  const entries = [
    message('u1', '', 'user'), proseMessage('a1', 'u1'),
    { type: 'compaction', id: 'c1', parentId: 'a1', summary: 'sum' },
  ]
  const { state } = sparseHistory(entries, ['u1', 'a1', 'c1'], ['u1', 'a1', 'c1'], 0)
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'a1', 'c1'])
  expect(foldedCount(state)).toBe(0)
})

for (const variant of ['event', 'aborted-event', 'legacy-aborted']) {
  test(`compact reload excludes cancellation metadata: ${variant}`, () => {
    const entries: Entry[] = [
      message('u1', '', 'user'), message('a1', 'u1', 'assistant'), proseMessage('a2', 'a1'),
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
  })
}

test('trailing runtime notifications fold while long prose stays visible', () => {
  const entries = [
    message('u1', '', 'user'), proseMessage('a1', 'u1'),
    message('notice', 'a1', 'user', { origin: 'agent:task-1' }),
  ]
  const { state } = sparseHistory(entries, ['u1', 'a1'], ['u1', 'a1'], 1, { omittedNodeIds: ['notice'] })
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1'])
  expect(foldedCount(state)).toBe(1)
})

test('partial notification hydration preserves fold counts and sparse parent links', () => {
  const entries = [
    message('u1', '', 'user'), proseMessage('a1', 'u1'),
    message('notice', 'a1', 'user', { origin: 'agent:task-1' }), proseMessage('a2', 'notice'),
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a2'], ['u1', 'a2'], 1)
  expect(foldedCount(state)).toBe(1)
  const bodyOnly = hydrateEntries(state, [entries[2]])
  expect(bodyOnly.nodes.map(n => n.id)).toEqual(['u1', 'notice', 'a2'])
  expect(bodyOnly.compactTurns?.[0].baselineNodes?.map(n => n.id)).toEqual(['u1', 'notice', 'a2'])
  expect(foldedCount(bodyOnly)).toBe(1)
  const reprojected = { ...summary, entryIds: entries.map(e => e.id), visibleNodeIds: entries.map(e => e.id), hiddenCount: 0 }
  const partial = hydrateEntries(bodyOnly, [entries[1]], { compactTurns: [reprojected] })
  expect(partial.loadedTurnIds).toContain('u1')
  expect(partial.compactTurns?.[0].baselineNodes?.map(n => n.id)).toEqual(['u1', 'a1', 'notice', 'a2'])
  expect(foldedCount(partial)).toBe(1)
  expect(compactItems(partial).find(i => i.kind === 'fold')?.remote).toBe(false)

  const complete = hydrateEntries(partial, [entries[1]], { compactTurns: [summary] })
  expect(complete.loadedTurnIds).toContain('u1')
  expect(foldedCount(complete)).toBe(1)
  expect(compactItems(complete).find(i => i.kind === 'fold')?.remote).toBe(false)
})

test('a sparse machine-only fold counts its hidden directive anchor', () => {
  const entries = [
    message('directive', '', 'user', { origin: 'agent' }),
    message('a1', 'directive', 'assistant'), message('a2', 'a1', 'assistant'),
  ]
  const { state } = sparseHistory(entries, ['directive'], [], 3, { stats: {
    turn: 0, steps: 2, elapsedMs: 0, durationMs: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
    tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false, cost: 0, ttftMs: 0, tps: null, live: false,
  } })
  expect(compactItems(state).map(i => i.id)).toEqual(['fold:directive'])
  expect(foldedCount(state)).toBe(3)
})

test('compact tool projection folds tool calls and counts them as distinct nodes', () => {
  const entries = [
    message('u1', '', 'user'),
    message('a1', 'u1', 'assistant', { content: [
      { type: 'text', text: prose() },
      { type: 'toolCall', id: 't1', name: 'Bash', arguments: {} },
      { type: 'toolCall', id: 't2', name: 'Read', arguments: {} },
    ] }),
    message('r1', 'a1', 'toolResult', { toolCallId: 't1', toolName: 'Bash' }),
    message('r2', 'r1', 'toolResult', { toolCallId: 't2', toolName: 'Read' }),
  ]
  const { state, summary } = sparseHistory(entries, ['u1', 'a1'], ['u1', 'a1'], 2, {
    toolStates: [{ id: 't1', finished: true }, { id: 't2', finished: true }],
  })
  expect(compactItems(state).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1'])
  expect(foldedCount(state)).toBe(2)
  const hydrated = hydrateEntries(state, entries, { compactTurns: [summary] })
  expect(hydrated.nodes.map(n => n.id)).toEqual(['u1', 'a1', 't1', 't2'])
  expect(foldedCount(hydrated)).toBe(2)
})
