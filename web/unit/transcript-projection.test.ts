import { expect, test } from 'bun:test'
import type { Entry, Message, SessionDetail, ViewState } from '../src/api/types'
import { appendOptimisticUser, applyEvent, applyIndex, applyTail, emptyView, evictBodies, hydrateEntries, hydrateTurn, loadHistory, projectTurnStats, reconcileUserNodes, sessionStats, userRequests } from '../src/lib/model'
import { foldReplies, groupTurns } from '../src/lib/messageView'
import { bodyKind } from '../src/lib/transcriptCoverage'
import fixtures from './fixtures/transcript-recovery.json'
import fixtureInputs from './fixtures/transcript-recovery-inputs.json'

const message = (id: string, parentId: string | undefined, role: string, extra: Partial<Message> = {}): Entry => ({
  type: 'message', id, parentId, message: { role, content: [{ type: 'text', text: id }], ...extra },
})

function noticeEntries(): Entry[] {
  return fixtureInputs.histories.notice as Entry[]
}

function folds(state: ViewState, keep = 1) {
  return foldReplies(state.nodes, { keep, summaries: state.compactTurns, loadedTurnIds: state.loadedTurnIds })
    .filter(item => item.kind === 'fold').map(item => ({ id: item.id, count: item.count, remote: item.remote }))
}

function permutations<T>(items: T[]): T[][] {
  return items.length ? items.flatMap((item, i) => permutations([...items.slice(0, i), ...items.slice(i + 1)]).map(rest => [item, ...rest])) : [[]]
}

test('shared Go wire fixtures cover exactly the data-driven conformance cases', () => {
  expect(Object.keys(fixtures).sort()).toEqual(Object.keys(fixtureInputs.cases).sort())
})

test('durable compaction statuses survive sparse recovery without folds, phantom steps or duplicate checkpoints', () => {
  const cases = [
    ['compactionEmpty', ['empty-end'], [true], [false]],
    ['compactionFailed', ['empty-end', 'failed-end'], [true, false], [false, true]],
    ['compactionCommitted', ['empty-end', 'failed-end', 'checkpoint'], [true, false, undefined], [false, true, undefined]],
    ['compactionOnly', ['end'], [true], [false]],
    ['compactionInline', ['inline-checkpoint'], [undefined], [undefined]],
  ] as const
  for (const [name, ids, empty, failed] of cases) {
    const wire = fixtures[name]
    const snapshot: SessionDetail = { id: name, ...wire, leafId: wire.compactTurns.at(-1)?.tailId, running: false }
    const source = fixtureInputs.cases[name]
    const history = fixtureInputs.histories[source.history as keyof typeof fixtureInputs.histories] as Entry[]
    const sparse = loadHistory(snapshot)
    for (const state of [sparse, hydrateEntries(sparse, history), applyTail(sparse, snapshot)]) {
      const rows = state.nodes.filter(n => n.kind === 'compaction')
      expect(rows.map(n => n.id)).toEqual([...ids])
      expect(rows.map(n => n.empty)).toEqual([...empty])
      expect(rows.map(n => n.failed)).toEqual([...failed])
      expect(rows.some(n => n.running)).toBe(false)
      for (const keep of [0, 1, 20]) {
        const items = foldReplies(state.nodes, { keep, summaries: state.compactTurns, loadedTurnIds: state.loadedTurnIds })
        expect(items.filter(i => i.kind === 'node' && i.node.kind === 'compaction').map(i => i.id)).toEqual([...ids])
      }
      expect(sessionStats(state).steps).toBe(wire.compactTurns.at(-1)!.stepCount)
    }
  }
})

test('compaction lifecycle pairing crosses index/body boundaries and settles interrupted starts', () => {
  const entries = fixtureInputs.histories.compactionOnly as Entry[]
  const terminal: SessionDetail = { id: 's', entries: [entries[1]], leafId: 'end', running: false }
  let state = loadHistory(terminal)
  expect(state.nodes).toHaveLength(1)
  expect(state.nodes[0]).toMatchObject({ id: 'end', empty: true, running: false })
  state = applyIndex(state, { id: 's', index: entries.map(({ id, parentId, type }) => ({ id, parentId, type })) })
  expect(state.nodes).toHaveLength(1)
  state = hydrateEntries(state, entries)
  expect(state.nodes).toHaveLength(1)
  expect(state.nodes[0]).toMatchObject({ id: 'end', empty: true })

  const start: SessionDetail = { id: 's', entries: [entries[0]], leafId: 'start', running: true }
  const live = loadHistory(start)
  expect(live.nodes[0]).toMatchObject({ id: 'start', running: true })
  const idle = applyTail(live, { ...start, running: false })
  expect(idle.nodes[0]).toMatchObject({ running: false, failed: true })
  expect(sessionStats(idle).steps).toBe(0)
})

test('canonical compaction SSE replay and history commute across unrelated hydration', () => {
  const entries = fixtureInputs.histories.compactionOnly as Entry[]
  const events = entries.map(e => ({
    type: e.type, lifecycleEntryId: e.id, parentId: e.parentId ?? '',
    timestamp: Date.parse(e.timestamp!), ...(e.details as { reason: string; status?: string; ok?: boolean }),
  }))
  const snapshot: SessionDetail = { id: 's', entries, leafId: 'end', running: false }
  for (const historyAt of [0, 1, 2]) {
    let state = emptyView()
    for (let i = 0; i <= events.length; i++) {
      if (i === historyAt) state = applyTail(state, snapshot)
      if (i < events.length) state = applyEvent(state, events[i])
      state = applyIndex(state, { id: 's', index: [] })
    }
    state = applyTail(state, snapshot)
    for (const event of events) state = applyEvent(state, event)
    expect(state.nodes).toHaveLength(1)
    expect(state.nodes[0]).toMatchObject({ id: 'end', empty: true, running: false })
    expect(state.records.filter(r => r.kind === 'compact')).toHaveLength(1)
    expect(state.busy).toBe(false)
    expect(sessionStats(state).steps).toBe(0)
  }
  let live = applyEvent(emptyView(), events[0])
  live = hydrateEntries(live, [])
  expect(live.nodes[0]).toMatchObject({ id: 'start', running: true })
  live = applyEvent(live, events[1])
  live = applyIndex(live, { id: 's', index: [] })
  expect(live.nodes[0]).toMatchObject({ id: 'end', empty: true, running: false })
})

test('later execution cannot resurrect an interrupted compaction and eviction retains terminal facts', () => {
  const lifecycle = fixtureInputs.histories.compactionOnly as Entry[]
  const later = message('later', 'start', 'user')
  let state = loadHistory({ id: 's', entries: [lifecycle[0], later], leafId: 'later', running: true })
  state = hydrateEntries(state, [])
  expect(state.nodes.find(n => n.kind === 'compaction')).toMatchObject({ running: false, failed: true })

  const next = message('next', 'end', 'user')
  state = loadHistory({ id: 's', entries: [...lifecycle, next], leafId: 'next' })
  state = evictBodies(state, new Set(), 0)
  expect(state.nodes.find(n => n.kind === 'compaction')).toMatchObject({ id: 'end', empty: true, running: false })
  const partial = applyIndex(loadHistory({ id: 's', entries: [lifecycle[0]], leafId: 'end' }), {
    id: 's', index: lifecycle.map(({ type, id, parentId }) => ({ type, id, parentId })),
  })
  expect(partial.nodes.find(n => n.kind === 'compaction')).toMatchObject({ id: 'end', unknown: true, truncated: true })
  const hydrated = hydrateEntries(partial, [lifecycle[1]])
  expect(hydrated.nodes.find(n => n.kind === 'compaction')).toMatchObject({ id: 'end', unknown: false, empty: true })
})

for (const [name, source] of Object.entries(fixtureInputs.cases)) {
  test(`Go↔TS compact conformance: ${name}, sparse and hydrated`, () => {
    const wire = fixtures[name as keyof typeof fixtures]
    const history = fixtureInputs.histories[source.history as keyof typeof fixtureInputs.histories] as Entry[]
    const snapshot: SessionDetail = { id: name, ...wire, leafId: wire.compactTurns.at(-1)?.tailId }
    const sparse = loadHistory(snapshot)
    const hydrated = hydrateEntries(sparse, history)
    for (const state of [sparse, hydrated, applyTail(hydrated, snapshot)]) {
      const stats = new Map([...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()].map(t => [t.turnId, t]))
      for (const expected of wire.compactTurns) {
        expect(stats.get(expected.id)).toMatchObject({ ...expected.stats, turnId: expected.id })
      }
      const session = sessionStats(state)
      expect(session).toMatchObject({ turns: wire.compactTurns.at(-1)?.stats.turn, steps: wire.compactTurns.at(-1)?.stepCount })
      expect(session.elapsedMs + session.turnElapsedMs).toBe(wire.compactTurns.at(-1)?.cumulativeElapsedMs ?? 0)
    }
  })
}

test('B1 golden runtime notice hydration preserves canonical turn, tools, steps and Trace ownership', () => {
  const snapshot: SessionDetail = { id: 's', ...fixtures.notice, leafId: 'final' }
  const opened = loadHistory(snapshot)
  const complete = hydrateEntries(opened, noticeEntries())
  for (const state of [opened, complete, applyIndex(complete, { id: 's', index: [] })]) {
    expect(groupTurns(state.nodes).map(t => t.id)).toEqual(['u'])
    const stats = [...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()]
    expect(stats).toHaveLength(1)
    expect(stats[0]).toMatchObject({ turnId: 'u', turn: 1, steps: 2, tools: 1 })
    expect(sessionStats(state)).toMatchObject({ turns: 1, steps: 2 })
    expect(new Set(state.records.map(r => r.turnId))).toEqual(new Set(['u']))
    expect(new Set(state.records.map(r => r.turn))).toEqual(new Set([1]))
    expect(userRequests(state.allEntries, state.leafId, state.nodes, state.compactTurns).map(r => r.id)).toEqual(['u'])
    expect(folds(state)[0].count).toBe(3)
  }
})

test('B2 golden helper snapshot is idempotent and does not prove body coverage', () => {
  const snapshot: SessionDetail = { id: 's', ...fixtures.helper, leafId: 'call', running: true }
  let state = loadHistory(snapshot)
  const original = folds(state)
  expect(original).toEqual([{ id: 'fold:u', count: 2, remote: true }])
  for (let i = 0; i < 8; i++) {
    const previous = state
    state = applyTail(state, snapshot)
    expect(state).toEqual(previous)
    expect(state.nodes.map(n => n.id)).toEqual(['u', 't2'])
    expect(state.loadedTurnIds).not.toContain('u')
    expect(bodyKind(state.entries.find(e => e.id === 'call')!)).toBe('helper')
    expect(folds(state)).toEqual(original)
  }
  // Even an explicitly completed HTTP operation cannot turn a helper body
  // into coverage. A full/slim node-bearing body must replace the helper.
  state = hydrateTurn(state, 'u', state.entries)
  expect(state.loadedTurnIds).not.toContain('u')
  expect(folds(state)).toEqual(original)
})

test('helper replay, full body and index arrival commute without downgrading coverage', () => {
  const snapshot: SessionDetail = { id: 's', ...fixtures.helper, leafId: 'call', running: true }
  const full = fixtureInputs.histories.helper[1] as Entry
  const operations: ((s: ViewState) => ViewState)[] = [
    s => applyTail(s, snapshot),
    s => hydrateEntries(s, [full]),
    s => applyIndex(s, { id: 's', index: [
      { type: 'message', id: 'u', role: 'user' },
      { type: 'message', id: 'call', parentId: 'u', role: 'assistant' },
    ] }),
  ]
  for (const order of permutations(operations)) {
    const state = order.reduce((s, operation) => operation(s), loadHistory(snapshot))
    expect(state.nodes.map(n => n.id)).toEqual(['u', 'call', 't1', 't2'])
    expect(state.loadedTurnIds).toContain('u')
    expect(bodyKind(state.entries.find(e => e.id === 'call')!)).toBe('full')
    expect(folds(state)).toEqual([{ id: 'fold:u', count: 2, remote: false }])
    expect([...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()][0])
      .toMatchObject({ turnId: 'u', steps: 1, tools: 2 })
  }
})

test('slim bodies prove node coverage without claiming complete payloads', () => {
  const snapshot: SessionDetail = { id: 's', ...fixtures.helper, leafId: 'call' }
  const slim = message('call', 'u', 'assistant', { content: [
    { type: 'text', text: 'preview…' },
    { type: 'toolCall', id: 't1', name: 'Read' },
    { type: 'toolCall', id: 't2', name: 'Read' },
  ] })
  slim.truncated = true
  const state = hydrateEntries(loadHistory(snapshot), [slim])
  expect(state.loadedTurnIds).toContain('u')
  expect(bodyKind(state.entries.find(e => e.id === 'call')!)).toBe('slim')
  expect(state.nodes.find(n => n.id === 'call')?.truncated).toBe(true)
  expect(folds(state)[0].remote).toBe(false)
})

test('B4 distinct canonical same-text inputs survive history, reconciliation, events and navigation', () => {
  const first = message('first', undefined, 'user', { clientRequestId: 'first-request', content: [{ type: 'text', text: 'continue' }] })
  const second = message('second', 'first', 'user', { clientRequestId: 'second-request', content: first.message!.content })
  let state = loadHistory({ id: 's', entries: [first, second], leafId: 'second' })
  state = applyEvent(state, { type: 'message_end', entryId: 'first', message: first.message })
  state = applyEvent(state, { type: 'message_end', entryId: 'second', message: second.message })
  expect(reconcileUserNodes(state.nodes).map(n => n.id)).toEqual(['first', 'second'])
  expect(userRequests(state.allEntries, state.leafId, state.nodes).map(r => r.id)).toEqual(['first', 'second'])
  expect(sessionStats(state).turns).toBe(2)
  // Correlation is only an acknowledgement rule for transient rows, never a
  // license to remove two different durable IDs with identical metadata.
  const copies = state.nodes.map(n => n.kind === 'user' ? { ...n, clientRequestId: 'same' } : n)
  expect(reconcileUserNodes(copies).map(n => n.id)).toEqual(['first', 'second'])
})

test('optimistic same-text inputs use distinct request IDs and survive unrelated rebuilds', () => {
  const content = [{ type: 'text', text: 'continue' }]
  let state = appendOptimisticUser(emptyView(), content, 'r1')
  state = appendOptimisticUser(state, content, 'r2')
  expect(state.nodes).toHaveLength(2)
  state = applyIndex(state, { id: 's', index: [] })
  expect(state.nodes).toHaveLength(2)
  for (const [id, request] of [['first', 'r1'], ['second', 'r2']]) {
    state = applyEvent(state, { type: 'message_end', entryId: id, message: { role: 'user', clientRequestId: request, content } })
  }
  expect(state.nodes.map(n => n.id)).toEqual(['first', 'second'])
  expect(sessionStats(state).turns).toBe(2)
  expect(reconcileUserNodes(state.nodes)).toEqual(state.nodes)
})

test('B10 accepted runtime identity/origin remains runtime through every replay ordering', () => {
  const notification: Message = {
    role: 'user', origin: 'agent:child', clientRequestId: 'child:4',
    completion: { taskId: 'child', generation: 4 },
    external: { source: 'test' }, content: [{ type: 'text', text: 'result' }],
  }
  const first = message('u', undefined, 'user', { timestamp: 1_000 })
  const operations: ((s: ViewState) => ViewState)[] = [
    s => applyEvent(s, { type: 'steer_accepted', message: notification }),
    s => applyEvent(s, { type: 'message_start', message: notification }),
    s => applyEvent(s, { type: 'message_end', entryId: 'notice', parentId: 'u', message: notification }),
  ]
  for (const order of permutations(operations)) {
    let state = loadHistory({ id: 's', entries: [first], leafId: 'u' })
    for (const operation of order) {
      state = operation(state)
      expect(groupTurns(state.nodes).map(t => t.id)).toEqual(['u'])
      expect(sessionStats(state).turns).toBe(1)
      expect(userRequests(state.allEntries, state.leafId, state.nodes).map(r => r.id)).toEqual(['u'])
      const notice = state.nodes.find(n => n.id === 'notice')
      if (notice) expect(notice).toMatchObject({
        origin: 'agent:child', clientRequestId: 'child:4', completion: { taskId: 'child', generation: 4 }, external: { source: 'test' },
      })
      else expect(state.nodes.map(n => n.id)).toEqual(['u'])
    }
    expect(state.nodes.map(n => n.id)).toEqual(['u', 'notice'])
    expect(new Set(state.records.map(r => r.turnId))).toEqual(new Set(['u']))
  }
})

test('replayed runtime acceptance/start cannot duplicate a durable notification after the final reply', () => {
  const snapshot: SessionDetail = { id: 's', ...fixtures.notice, leafId: 'final', running: false }
  const notice = noticeEntries().find(e => e.id === 'notice')!.message!
  for (const running of [false, true]) {
    let state = loadHistory({ ...snapshot, running })
    const ids = state.nodes.map(n => n.id)
    const folded = folds(state)
    for (const type of ['steer_accepted', 'message_start'] as const) {
      state = applyEvent(state, { type, message: notice })
      expect(state.nodes.map(n => n.id)).toEqual(ids)
      expect(state.nodes.at(-1)?.id).toBe('final')
      expect(state.busy).toBe(running)
      expect(folds(state)).toEqual(folded)
    }
    state = applyEvent(state, { type: 'agent_end' })
    state = applyTail(state, snapshot)
    expect(state.nodes.map(n => n.id)).toEqual(ids)
    expect(state.nodes.at(-1)?.id).toBe('final')
    state = hydrateEntries(state, noticeEntries())
    expect(state.nodes.map(n => n.id)).toEqual(['u', 'call', 'tool', 'notice', 'final'])
    expect(foldReplies(state.nodes, { keep: 1 }).at(-1)?.id).toBe('final')
  }
})

test('undrained runtime mail never displaces the terminal assistant but persisted mail still renders in order', () => {
  const first = message('u', undefined, 'user')
  const notification: Message = { role: 'user', origin: 'agent:child', clientRequestId: 'mail',
    content: [{ type: 'text', text: 'runtime mail' }] }
  let state = loadHistory({ id: 's', entries: [first], leafId: 'u', running: true })
  state = applyEvent(state, { type: 'message_start', message: { role: 'assistant', content: [] } })
  state = applyEvent(state, { type: 'steer_accepted', message: notification })
  state = applyEvent(state, { type: 'message_end', entryId: 'a', parentId: 'u',
    message: { role: 'assistant', content: [{ type: 'text', text: 'answer' }] } })
  state = applyEvent(state, { type: 'agent_end' })
  expect(state.nodes.map(n => n.id)).toEqual(['u', 'a'])
  expect(foldReplies(state.nodes, { keep: 1 }).at(-1)?.id).toBe('a')
  expect(state.busy).toBe(false)

  state = applyEvent(state, { type: 'agent_start' })
  state = applyEvent(state, { type: 'message_end', entryId: 'notice', parentId: 'a', message: notification })
  state = applyEvent(state, { type: 'message_end', entryId: 'final', parentId: 'notice',
    message: { role: 'assistant', content: [{ type: 'text', text: 'handled mail' }] } })
  expect(state.nodes.map(n => n.id)).toEqual(['u', 'a', 'notice', 'final'])
  expect(hydrateEntries(state, [first]).nodes.map(n => n.id)).toEqual(['u', 'a', 'notice', 'final'])
})

test('runtime-only history has one stable first group and no invented human navigation request', () => {
  const entries = [
    message('directive', undefined, 'user', { origin: 'agent', timestamp: 1_000 }),
    message('a1', 'directive', 'assistant', { timestamp: 2_000 }),
    message('notice', 'a1', 'user', { origin: 'agent:child', timestamp: 3_000 }),
    message('a2', 'notice', 'assistant', { timestamp: 4_000 }),
  ]
  const state = loadHistory({ id: 's', entries, leafId: 'a2' })
  expect(groupTurns(state.nodes).map(t => t.id)).toEqual(['directive'])
  expect([...projectTurnStats(state.nodes).values()][0]).toMatchObject({
    turnId: 'directive', turn: 1, steps: 2, elapsedMs: 3_000, startedAt: 1_000,
  })
  expect(sessionStats(state)).toMatchObject({ turns: 1, steps: 2, turnStartedAt: 1_000, turnElapsedMs: 3_000 })
  expect(userRequests(state.allEntries, state.leafId, state.nodes)).toEqual([])
  expect(new Set(state.records.map(r => r.turnId))).toEqual(new Set(['directive']))
})

test('cancellation remains a standalone durable row across body recovery and compact rendering', () => {
  let state = loadHistory({ id: 's', entries: [message('u', undefined, 'user')], leafId: 'u' })
  state = applyEvent(state, { type: 'run_aborted', entryId: 'abort', parentId: 'u', runId: 'run', reason: 'user_request', cancelSource: 'webui' })
  state = hydrateEntries(state, [message('u', undefined, 'user')])
  expect(state.nodes.map(n => n.kind)).toEqual(['user', 'cancellation'])
  expect(state.entries.at(-1)?.type).toBe('run_aborted')
  expect(foldReplies(state.nodes, { keep: 0 }).map(n => n.id)).toEqual(['u', 'abort'])
})

test('same-frontier stale recovery may fill sparse gaps without regressing live execution', () => {
  const old: SessionDetail = { id: 's', ...fixtures.helper, leafId: 'call', running: true }
  let state = loadHistory(old)
  const revision = state.liveRevision
  state = applyEvent(state, { type: 'message_end', entryId: 'final', parentId: 'missing', message: { role: 'assistant', content: [{ type: 'text', text: 'final' }] } })
  state = applyEvent(state, { type: 'agent_start' })
  const summary = { ...fixtures.notice.compactTurns[0], entryCount: 7, hiddenCount: 5, stats: { ...fixtures.notice.compactTurns[0].stats, steps: 3, tools: 2 } }
  state = applyTail(state, { id: 's', ...fixtures.notice, compactTurns: [summary], leafId: 'final', running: false }, revision)
  expect(state.busy).toBe(true)
  expect(state.leafId).toBe('final')
  expect(state.compactTurns?.[0].tailId).toBe('final')
  expect(state.nodes.map(n => n.id)).toContain('u')
  expect(state.nodes.map(n => n.id)).toContain('final')
})

test('root prelude belongs to its first human identity without duplicate snapshot usage', () => {
  const entries = [
    message('prelude', undefined, 'assistant', { timestamp: 500 }),
    message('u', 'prelude', 'user', { timestamp: 1_000 }),
    message('final', 'u', 'assistant', { timestamp: 2_000 }),
  ]
  const summary = {
    ...fixtures.notice.compactTurns[0], entryCount: 3, entryIds: ['u', 'final'], visibleNodeIds: ['u', 'final'], hiddenCount: 1,
    stats: { ...fixtures.notice.compactTurns[0].stats, tools: 0, startedAt: 1_000, elapsedMs: 1_000 },
  }
  const state = hydrateEntries(loadHistory({ id: 's', entries: entries.slice(1), compactTurns: [summary], leafId: 'final' }), [entries[0]])
  expect(groupTurns(state.nodes).map(t => t.id)).toEqual(['u'])
  expect(state.loadedTurnIds).toContain('u')
  expect([...projectTurnStats(state.nodes, 0, state.compactTurns).values()][0]).toMatchObject({
    turnId: 'u', steps: 2, elapsedMs: 1_000,
  })
  expect(sessionStats(state)).toMatchObject({ turns: 1, turnElapsedMs: 1_000 })
  expect(foldReplies(state.nodes, { keep: 20 }).map(n => n.id)).toEqual(['prelude', 'u', 'final'])
})

test('authoritative recovery refreshes present metadata but projected or stale tails cannot erase it', () => {
  let state = applyTail(emptyView(), { id: 's', title: 'Recovered', cwd: '/workspace', model: 'model', provider: 'provider', thinkingEffort: 'high' })
  state = applyTail(state, { id: 's', entries: [] })
  expect(state).toMatchObject({ title: 'Recovered', cwd: '/workspace', model: 'model', provider: 'provider', thinkingEffort: 'high' })
  state = applyEvent(state, { type: 'agent_start' })
  state = applyTail(state, { id: 's', title: 'Old', model: 'old' }, state.liveRevision - 1)
  expect(state).toMatchObject({ title: 'Recovered', model: 'model', busy: true })
})

test('new accepted work invalidates an older idle GET while duplicate acknowledgements are idempotent', () => {
  const initial = emptyView()
  const accepted = appendOptimisticUser(initial, [{ type: 'text', text: 'next' }], 'new-work')
  expect(accepted.liveRevision).toBe(initial.liveRevision + 1)
  const duplicate = appendOptimisticUser(accepted, [{ type: 'text', text: 'next' }], 'new-work')
  expect(duplicate).toBe(accepted)
  const recovered = applyTail(accepted, { id: 's', entries: [], running: false }, initial.liveRevision)
  expect(recovered.busy).toBe(true)
  expect(recovered.nodes).toHaveLength(1)
  const settled = applyEvent(recovered, { type: 'agent_end' })
  const replay = applyEvent(settled, { type: 'steer_accepted', message: { role: 'user', clientRequestId: 'new-work', content: [{ type: 'text', text: 'next' }] } })
  expect(replay).toBe(settled)
  expect(replay.busy).toBe(false)
})

test('first human request adopts a runtime prelude identity immediately, including hidden snapshot work', () => {
  const history = fixtureInputs.histories.preludeClock as Entry[]
  const prefix: SessionDetail = { id: 's', ...fixtures.preludeRuntimePrefix, leafId: 'prelude-answer' }
  let state = loadHistory(prefix)
  const revision = state.liveRevision
  const human = { ...history[2].message!, clientRequestId: 'first-human' }
  state = appendOptimisticUser(state, human)
  const optimistic = state.nodes.find(n => n.kind === 'user' && n.clientRequestId === 'first-human')!
  expect(state.turn).toBe(1)
  expect(groupTurns(state.nodes).map(t => t.id)).toEqual([optimistic.id])
  expect(sessionStats(state)).toMatchObject({ turns: 1, turnElapsedMs: 100 })
  state = applyEvent(state, { type: 'message_end', entryId: 'human', parentId: 'prelude-answer', message: human })
  const check = () => {
    expect(state.turn).toBe(1)
    expect(groupTurns(state.nodes).map(t => t.id)).toEqual(['human'])
    expect([...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()]).toHaveLength(1)
    expect(sessionStats(state)).toMatchObject({ turns: 1, turnStartedAt: 2000 })
    expect(new Set(state.records.map(r => r.turnId))).toEqual(new Set(['human']))
  }
  check()
  state = applyTail(state, prefix, revision)
  check()
  for (const entry of history.slice(3)) {
    state = applyEvent(state, { type: 'message_end', entryId: entry.id, parentId: entry.parentId, message: entry.message })
    check()
  }
  expect([...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()][0])
    .toMatchObject({ ...fixtures.preludeClock.compactTurns[0].stats, turnId: 'human' })
  expect(sessionStats(state)).toMatchObject({ turnElapsedMs: 3000 })
})

test('keep=0 composer counts only steps after the immutable snapshot tail across hydration orders', () => {
  const entries: Entry[] = [message('u', undefined, 'user')]
  for (let i = 1; i <= 40; i++) entries.push(message(`a${i}`, entries.at(-1)!.id, 'assistant'))
  const summary = {
    ...fixtures.selected.compactTurns[0],
    id: 'u', tailId: 'a40', entryCount: 41, entryIds: ['u'], visibleNodeIds: ['u'],
    hiddenCount: 40, stepCount: 40, stats: { ...fixtures.selected.compactTurns[0].stats, steps: 40 },
  }
  const snapshot: SessionDetail = { id: 's', entries: [entries[0]], compactTurns: [summary], oldestId: 'u', leafId: 'a40' }
  for (const parentId of ['a40', 'missing-request-header']) {
    const operations: ((s: ViewState) => ViewState)[] = [
      s => hydrateEntries(s, entries.slice(1, 21)),
      s => hydrateEntries(s, entries.slice(21)),
      s => applyEvent(s, { type: 'message_end', entryId: 'a41', parentId, message: { role: 'assistant', content: [{ type: 'text', text: 'new reply' }] } }),
    ]
    for (const order of permutations(operations)) {
      let state = loadHistory(snapshot)
      let completed = false
      for (const operation of order) {
        state = operation(state)
        completed ||= state.entries.some(e => e.id === 'a41')
        expect(sessionStats(state)).toMatchObject({ turns: 1, steps: completed ? 41 : 40 })
      }
      expect([...projectTurnStats(state.nodes, state.turnBase, state.compactTurns).values()][0].steps).toBe(41)
      expect(sessionStats(hydrateEntries(state, entries)).steps).toBe(41)
    }
  }
})
