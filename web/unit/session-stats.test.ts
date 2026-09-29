import { expect, test } from 'bun:test'
import { applyEvent, appendOptimisticUser, loadHistory, hydrateEntries, hydrateTurn, projectTurnStats, cacheHitPercent, cacheHitRate, cacheMisses, emptyView, formatCost, formatDuration, formatTokens, formatTokensPerSecond, latestStats, turnStats } from '../src/lib/model.ts'
import { applyTail } from '../src/lib/model.ts'
import type { ChatNode, CompactTurn, Entry, SessionDetail, TrajRecord, ViewState } from '../src/api/types.ts'

function view(over: Partial<ViewState> = {}): ViewState {
  return { ...emptyView(), ...over }
}

function msg(id: string, parentId: string, role: 'user' | 'assistant', extra: Partial<NonNullable<Entry['message']>> = {}): Entry {
  return {
    type: 'message',
    id,
    parentId,
    message: { role, ...extra },
  }
}

test('latestStats counts the branch but keeps only the newest step usage', () => {
  const entries: Entry[] = [
    msg('u1', '', 'user'),
    msg('a1', 'u1', 'assistant', {
      usage: { input: 10, output: 4, cacheRead: 90, cacheWrite: 0, cost: { input: 0.01, output: 0.02, cacheRead: 0, cacheWrite: 0, total: 0.03 } },
      ttftMs: 800,
      latencyMs: 1800,
    }),
    { type: 'compaction', id: 'c1', parentId: 'a1', summary: 'sum', usage: { input: 20, output: 5 } },
    msg('u2', 'c1', 'user'),
    msg('a2', 'u2', 'assistant', { usage: { input: 3, output: 1, cacheRead: 7 }, ttftMs: 200, latencyMs: 700 }),
    msg('other', 'u1', 'assistant', { usage: { input: 999, output: 999 } }),
  ]
  const s = latestStats(view({
    leafId: 'a2',
    allEntries: entries,
    nodes: [
      { kind: 'user', id: 'u2', text: 'hi', content: [] },
      { kind: 'assistant', id: 'a2', text: 'ok', usage: { input: 3, output: 1, cacheRead: 7 }, ttftMs: 200, latencyMs: 700 },
    ],
  }))
  expect(s.turns).toBe(2)
  expect(s.steps).toBe(3)
  expect(s.input).toBe(3 + 7)
  expect(s.output).toBe(1)
  expect(s.cacheRead).toBe(7)
  expect(s.hasCost).toBe(false)
  expect(s.ttftMs).toBe(200)
  expect(s.decodeMs).toBe(500)
  expect(s.decodeTokens).toBe(1)
})

test('latestStats prefers a live step that is not persisted yet', () => {
  const s = latestStats(view({
    leafId: 'a1',
    allEntries: [msg('u1', '', 'user'), msg('a1', 'u1', 'assistant', { usage: { input: 1, output: 1 }, ttftMs: 50, latencyMs: 100 })],
    nodes: [
      { kind: 'assistant', id: 'a1', text: 'old', usage: { input: 1, output: 1 }, ttftMs: 50, latencyMs: 100 },
      { kind: 'assistant', id: 'live-asst-1', text: 'new', usage: { input: 8, output: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0.012 } }, ttftMs: 100, latencyMs: 600 },
    ],
  }))
  expect(s.turns).toBe(1)
  expect(s.steps).toBe(2)
  expect(s.input).toBe(8)
  expect(s.output).toBe(2)
  expect(s.hasCost).toBe(true)
  expect(s.cost).toBeCloseTo(0.012)
  expect(s.ttftMs).toBe(100)
  expect(s.decodeMs).toBe(500)
  expect(s.decodeTokens).toBe(2)
})

test('latestStats skips a streaming step and a sibling branch', () => {
  const s = latestStats(view({
    leafId: 'a1',
    allEntries: [
      msg('u1', '', 'user'),
      msg('a1', 'u1', 'assistant', { usage: { input: 1, output: 1 }, ttftMs: 40, latencyMs: 240 }),
      msg('u2', 'u1', 'user'),
      msg('a2', 'u2', 'assistant', { usage: { input: 50, output: 50 } }),
    ],
    nodes: [
      { kind: 'assistant', id: 'a1', text: 'ok', usage: { input: 1, output: 1 }, ttftMs: 40, latencyMs: 240 },
      { kind: 'assistant', id: 'stream', text: '…', streaming: true, usage: { input: 9, output: 9 } },
    ],
  }))
  expect(s.turns).toBe(1)
  expect(s.steps).toBe(1)
  expect(s.input).toBe(1)
  expect(s.output).toBe(1)
  expect(s.decodeMs).toBe(200)
  expect(s.decodeTokens).toBe(1)
})

test('latestStats drops decode when the step reported no TTFT', () => {
  const s = latestStats(view({
    nodes: [{ kind: 'assistant', id: 'a1', text: 'ok', usage: { input: 8, output: 2 }, latencyMs: 500 }],
  }))
  expect(s.input).toBe(8)
  expect(s.output).toBe(2)
  expect(s.ttftMs).toBe(0)
  expect(s.decodeMs).toBe(0)
  expect(s.decodeTokens).toBe(0)
})

test('cacheHitPercent needs billed input and a cache read', () => {
  const base = latestStats(view())
  expect(cacheHitPercent({ ...base, input: 0, cacheRead: 0 })).toBeNull()
  expect(cacheHitPercent({ ...base, input: 100, cacheRead: 0 })).toBeNull()
  expect(cacheHitPercent({ ...base, input: 100, cacheRead: 90 })).toBe(90)
  // Unrounded, so the strip can render it to 2 decimals.
  const hit = cacheHitPercent({ ...base, input: 74_932, cacheRead: 3_584 })
  expect(hit!.toFixed(2)).toBe('4.78')
})

function asst(id: string, usage: NonNullable<Extract<ChatNode, { kind: 'assistant' }>['usage']>, extra: Partial<Extract<ChatNode, { kind: 'assistant' }>> = {}): ChatNode {
  return { kind: 'assistant', id, text: id, usage, ...extra }
}

test('cacheMisses compares each step against the previous prompt', () => {
  // No previous request → nothing to miss, so the session's first step is never flagged.
  expect(cacheMisses([asst('a1', { input: 30000, cacheRead: 0 })]).size).toBe(0)
  // Full read-back of the previous prompt → no miss.
  expect(cacheMisses([asst('a1', { input: 100, cacheRead: 29900 }), asst('a2', { input: 100, cacheRead: 30000 })]).size).toBe(0)
  // Appended content was not in the previous prompt, so it is never a miss.
  expect(cacheMisses([asst('a1', { input: 100, cacheRead: 29900 }), asst('a2', { input: 10000, cacheRead: 29900 })]).size).toBe(0)
  // A miss above the 20K token gate is reported with its token count and ratio.
  const miss = cacheMisses([asst('a1', { input: 100, cacheRead: 29900 }), asst('a2', { input: 30000, cacheRead: 0 })])
  expect(miss.get('a2')).toEqual({ missedTokens: 30000, missRatio: 1 })
})

test('cacheMisses also surfaces misses over half the previous prompt', () => {
  // 6K missed of a 10K previous prompt: under 20K but 60% → flagged.
  const ratio = cacheMisses([asst('a1', { input: 100, cacheRead: 9900 }), asst('a2', { input: 6000, cacheRead: 4000 })])
  expect(ratio.get('a2')).toEqual({ missedTokens: 6000, missRatio: 0.6 })
  // 10K missed of a 30K previous prompt: neither gate clears → not flagged.
  const quiet = cacheMisses([asst('a1', { input: 100, cacheRead: 29900 }), asst('a2', { input: 22000, cacheRead: 20000 })])
  expect(quiet.size).toBe(0)
  // Below the noise floor is never flagged, even at 100% of a small prompt.
  expect(cacheMisses([asst('a1', { input: 100, cacheRead: 900 }), asst('a2', { input: 2000, cacheRead: 0 })]).size).toBe(0)
})

test('cacheMisses ignores a provider that never reports caching', () => {
  expect(cacheMisses([asst('a1', { input: 30000, cacheRead: 0, cacheWrite: 0 }), asst('a2', { input: 40000, cacheRead: 0, cacheWrite: 0 })]).size).toBe(0)
})

test('cacheMisses resets across compaction and skips streaming steps', () => {
  const nodes: ChatNode[] = [
    asst('a1', { input: 100, cacheRead: 29900 }),
    { kind: 'compaction', id: 'c1', summary: 'sum' },
    asst('a2', { input: 30000, cacheRead: 0, cacheWrite: 100 }),
  ]
  expect(cacheMisses(nodes).size).toBe(0)
  // The live streaming step is not compared until it is complete.
  expect(cacheMisses([asst('a1', { input: 100, cacheRead: 29900 }), asst('live', { input: 30000 }, { streaming: true })]).size).toBe(0)
})

test('cacheHitRate is the cache-read share of the prompt, shown to 2 decimals', () => {
  expect(cacheHitRate(null)).toBeNull()
  expect(cacheHitRate({ input: 0, cacheRead: 0, cacheWrite: 0 })).toBeNull()
  expect(cacheHitRate({ input: 0, cacheRead: 100, cacheWrite: 0 })).toBe(100)
  const rate = cacheHitRate({ input: 71348, cacheRead: 3584, cacheWrite: 0 })
  expect(rate).toBeCloseTo(3584 / 74932 * 100)
  expect(rate!.toFixed(2)).toBe('4.78')
})

test('format helpers match the compact strip', () => {
  expect(formatTokens(517)).toBe('517')
  expect(formatTokens(12_200)).toBe('12.2K')
  expect(formatTokens(1_200_000)).toBe('1.2M')
  expect(formatDuration(900)).toBe('0.9s')
  expect(formatDuration(162_000)).toBe('2m42s')
  expect(formatTokensPerSecond(8.24)).toBe('8.2')
  expect(formatTokensPerSecond(259.4)).toBe('259')
  expect(formatCost(0.00321)).toBe('0.0032')
  expect(formatCost(1.2)).toBe('1.20')
})

test('turnStats aggregates a turn and keeps the first step TTFT', () => {
  const nodes: ChatNode[] = [
    { kind: 'user', id: 'u1', text: 'hi', content: [], ts: 1_000 },
    asst('a1', {
      input: 100,
      output: 20,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0.02 },
    }, { ttftMs: 200, latencyMs: 1_200, ts: 3_000 }),
    { kind: 'tool', id: 't1', name: 'Bash' },
    { kind: 'tool', id: 't2', name: 'Edit', isError: true },
    asst('a2', { input: 10, output: 30, cacheRead: 90 }, { ttftMs: 300, latencyMs: 800, ts: 5_000 }),
    { kind: 'user', id: 'u2', text: 'again', content: [], ts: 9_000 },
    asst('a3', { input: 5, output: 5 }, { ttftMs: 50, latencyMs: 100, ts: 9_400 }),
  ]
  const stats = turnStats(nodes)
  // Keyed by the turn's last node so the chat mounts the strip after it.
  expect([...stats.keys()]).toEqual(['a2', 'a3'])
  const first = stats.get('a2')!
  expect(first.turn).toBe(1)
  expect(first.steps).toBe(2)
  // Wall clock spans the turn, including the tool call in between.
  expect(first.elapsedMs).toBe(4_000)
  expect(first.input).toBe(100 + 10 + 90)
  expect(first.output).toBe(50)
  expect(first.cacheRead).toBe(90)
  expect(first.hasCost).toBe(true)
  expect(first.cost).toBeCloseTo(0.02)
  expect(first.ttftMs).toBe(200)
  expect(first.live).toBe(false)
  expect(first.tools).toBe(2)
  expect(first.toolFailures).toBe(1)
  expect(first.cacheMisses).toBe(0)
  // Decode spans: (1200-200) + (800-300) over 50 output tokens.
  expect(first.tps).toBeCloseTo(50 / 1.5)
  const second = stats.get('a3')!
  expect(second.turn).toBe(2)
  expect(second.steps).toBe(1)
  expect(second.elapsedMs).toBe(400)
  expect(second.tps).toBeCloseTo(5 / 0.05)
  expect(second.tools).toBe(0)
  expect(second.toolFailures).toBe(0)
})

test('turnStats counts tool failures and notable cache misses per turn', () => {
  const nodes: ChatNode[] = [
    { kind: 'user', id: 'u1', text: 'hi', content: [] },
    // Full read-back of the previous prompt: healthy, no miss.
    asst('a1', { input: 100, cacheRead: 29_900 }),
    { kind: 'tool', id: 't1', name: 'Bash' },
    { kind: 'tool', id: 't2', name: 'Edit', isError: true },
    // The previous prompt (30K) is re-billed instead of read back → one miss.
    asst('a2', { input: 30_000, cacheRead: 0 }),
    { kind: 'user', id: 'u2', text: 'again', content: [] },
    // Reads the whole previous prompt back → no miss.
    asst('a3', { input: 30_000, cacheRead: 30_000 }),
  ]
  const stats = turnStats(nodes)
  const first = stats.get('a2')!
  expect(first.tools).toBe(2)
  expect(first.toolFailures).toBe(1)
  expect(first.cacheMisses).toBe(1)
  const second = stats.get('a3')!
  expect(second.tools).toBe(0)
  expect(second.toolFailures).toBe(0)
  expect(second.cacheMisses).toBe(0)
  // A compaction clears the previous-prompt baseline, so the rewrite is not a miss.
  const afterCompact = turnStats([
    { kind: 'user', id: 'u1', text: 'hi', content: [] },
    asst('a1', { input: 100, cacheRead: 29_900 }),
    { kind: 'compaction', id: 'c1', summary: 'sum' },
    asst('a2', { input: 30_000, cacheRead: 0 }),
  ]).get('a2')!
  expect(afterCompact.cacheMisses).toBe(0)
})

test('turnStats drops turns without a step and flags live ones', () => {
  expect(turnStats([{ kind: 'user', id: 'u1', text: 'hi', content: [] }]).size).toBe(0)
  const live = turnStats([
    { kind: 'user', id: 'u1', text: 'hi', content: [] },
    asst('a1', { input: 1, output: 1 }),
    { kind: 'assistant', id: 's1', text: '…', streaming: true },
  ])
  expect(live.get('s1')!.live).toBe(true)
  expect(live.get('s1')!.steps).toBe(1)
  const running = turnStats([
    { kind: 'user', id: 'u1', text: 'hi', content: [] },
    { kind: 'tool', id: 't1', name: 'Bash', running: true },
  ])
  expect(running.get('t1')!.live).toBe(true)
  expect(running.get('t1')!.steps).toBe(0)
})

test('turnStats falls back to summed latencies without timestamps', () => {
  const stats = turnStats([
    { kind: 'user', id: 'u1', text: 'hi', content: [] },
    asst('a1', { input: 1, output: 1 }, { latencyMs: 700 }),
    asst('a2', { input: 1, output: 1 }, { latencyMs: 300 }),
  ])
  expect(stats.get('a2')!.elapsedMs).toBe(1_000)
  // No TTFT means no TPS estimate, matching stepMetrics.
  expect(stats.get('a2')!.tps).toBeNull()
})

test('turnStats exposes the turn start and counts a tool tail', () => {
  const stats = turnStats([
    { kind: 'user', id: 'u1', text: 'hi', content: [], ts: 1_000 },
    asst('a1', { input: 1, output: 1 }, { ttftMs: 100, latencyMs: 400, ts: 1_400 }),
    { kind: 'tool', id: 't1', name: 'Bash', startedAt: 1_400, durationMs: 3_000 },
    { kind: 'assistant', id: 's1', text: '…', streaming: true },
  ])
  const live = stats.get('s1')!
  expect(live.live).toBe(true)
  // The live divider ticks `now - startedAt`; without the start it cannot.
  expect(live.startedAt).toBe(1_000)
  // A tool tail extends the span past the last assistant timestamp (1_400):
  // the tool ends at 4_400, so the turn has run 3_400ms from 1_000.
  expect(live.elapsedMs).toBe(3_400)
})

test('latestStats exposes the current turn start for the live counter', () => {
  const entry: Entry = {
    type: 'message',
    id: 'u1',
    parentId: '',
    timestamp: '2026-09-27T12:00:00.000Z',
    message: { role: 'user', content: [{ type: 'text', text: 'hi' }] },
  }
  expect(latestStats(view({ leafId: 'u1', allEntries: [entry] })).turnStartedAt)
    .toBe(Date.parse('2026-09-27T12:00:00.000Z'))
  // A just-sent prompt whose entry has not landed falls back to its live node.
  expect(latestStats(view({ nodes: [{ kind: 'user', id: 'opt', text: 'hi', content: [], ts: 42 }] })).turnStartedAt).toBe(42)
  expect(latestStats(view()).turnStartedAt).toBe(0)
  expect(latestStats(view({ leafId: 'u1', allEntries: [entry], nodes: [
    { kind: 'user', id: 'opt-user-2', text: 'next prompt', ts: 42 },
  ] })).turnStartedAt).toBe(42)
})

test('latestStats totals the session run time and keeps the newest turn separate', () => {
  const records: TrajRecord[] = [
    { id: 'u1', kind: 'user', turn: 1, preview: '', startedAt: 1_000 },
    { id: 'a1', kind: 'assistant', turn: 1, preview: '', startedAt: 1_200, durationMs: 800 },
    { id: 't1', kind: 'tool', turn: 1, preview: '', startedAt: 2_000, durationMs: 3_000 },
    { id: 'u2', kind: 'user', turn: 2, preview: '', startedAt: 10_000 },
    { id: 'a2', kind: 'assistant', turn: 2, preview: '', startedAt: 10_500, durationMs: 500 },
  ]
  const s = latestStats(view({ records }))
  // Turn 1 spans 1_000 → 5_000 (the tool tail); turn 2 spans 10_000 → 10_500.
  expect(s.elapsedMs).toBe(4_000)
  expect(s.turnElapsedMs).toBe(500)
  // A folded compact turn the browser never loaded still contributes its stats.
  const folded = latestStats(view({
    records,
    compactTurns: [{
      id: 'u0', parentId: '', tailId: 'a0', entryIds: ['u0'], visibleNodeIds: ['u0'], hiddenCount: 0,
      stats: { turn: 0, steps: 1, elapsedMs: 2_500, durationMs: 500, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false, cost: 0, ttftMs: 0, tps: null, live: false },
      stepCount: 1,
    }],
  }))
  expect(folded.elapsedMs).toBe(4_000 + 2_500)
})

test('a confirmed second prompt never switches the live timer back to the previous turn', () => {
  const first = msg('u1', '', 'user', { timestamp: 1_000, content: [{ type: 'text', text: 'first' }] })
  const answer = msg('a1', 'u1', 'assistant', { timestamp: 11_000 })
  const second = { role: 'user', timestamp: 101_000, content: [{ type: 'text', text: 'second' }] }
  let s = loadHistory({ entries: [first, answer], leafId: 'a1' } as SessionDetail)
  s = appendOptimisticUser(s, second.content)
  s = applyEvent(s, { type: 'message_start', message: second })
  s = applyEvent(s, { type: 'message_end', entryId: 'u2', message: second })
  const stats = latestStats(s)
  expect(stats.turnStartedAt).toBe(101_000)
  expect(stats.elapsedMs).toBe(10_000)
  expect(stats.elapsedMs + 106_000 - stats.turnStartedAt).toBe(15_000)
  // An unrelated body/index rebuild must keep the confirmed prompt as well.
  expect(latestStats(hydrateEntries(s, [first])).turnStartedAt).toBe(101_000)
})

function compactFixture() {
  const entries: Entry[] = [
    msg('u1', '', 'user', { timestamp: 1_000 }),
    msg('a1', 'u1', 'assistant', { timestamp: 2_000, content: [{ type: 'toolCall', id: 't1', name: 'Read' }] }),
    { type: 'message', id: 'r1', parentId: 'a1', message: { role: 'toolResult', toolCallId: 't1', timestamp: 3_000, durationMs: 1_000 } },
    msg('a2', 'r1', 'assistant', { timestamp: 4_000, content: [{ type: 'toolCall', id: 't2', name: 'Read' }] }),
    { type: 'message', id: 'r2', parentId: 'a2', message: { role: 'toolResult', toolCallId: 't2', timestamp: 5_000, durationMs: 1_000 } },
  ]
  const full = loadHistory({ entries, leafId: 'r2' } as SessionDetail)
  const stats = [...turnStats(full.nodes).values()][0]
  const summary: CompactTurn = { id: 'u1', tailId: 'r2', parentId: '', entryIds: ['u1', 'a2', 'r2'], visibleNodeIds: ['u1', 't2'], omittedNodeIds: ['a2'], hiddenCount: 3, stepCount: 2, stats }
  return { entries, summary }
}

test('compact snapshot plus hydrated overlap plus live delta counts every node once', () => {
  const { entries, summary } = compactFixture()
  let s = loadHistory({ entries: entries.filter(e => summary.entryIds.includes(e.id)), compactTurns: [summary], leafId: 'r2', running: true } as SessionDetail)
  const stats = () => [...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)!
  expect(stats().tools).toBe(2)
  expect(stats().steps).toBe(2)
  s = hydrateTurn(s, 'u1', entries)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't3', toolName: 'Read', timestamp: 6_000 })
  expect(stats().steps).toBe(2)
  expect(stats().tools).toBe(3)
  expect(stats().live).toBe(true)
  s = applyEvent(s, { type: 'tool_execution_end', toolCallId: 't3', toolName: 'Read', timestamp: 7_000, durationMs: 1_000, isError: true, result: 'failed' })
  expect(stats().tools).toBe(3)
  expect(stats().toolFailures).toBe(1)
  expect(stats().elapsedMs).toBe(6_000)
  // A body load is not a new run snapshot and cannot erase observed results.
  s = hydrateEntries(s, [entries[0]])
  expect(stats().tools).toBe(3)
  expect(stats().toolFailures).toBe(1)
})

test('a sparse compact snapshot adds only new tools before expansion', () => {
  const { entries, summary } = compactFixture()
  let s = loadHistory({ entries: entries.filter(e => summary.entryIds.includes(e.id)), compactTurns: [summary], leafId: 'r2', running: true } as SessionDetail)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't3', toolName: 'Read', timestamp: 6_000 })
  const stats = [...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)!
  expect(stats.tools).toBe(3)
  expect(stats.steps).toBe(2)
  expect(stats.live).toBe(true)
})

test('compact elapsed includes its unseen prefix and does not shrink before index load', () => {
  const { entries, summary } = compactFixture()
  const s = loadHistory({ entries: entries.filter(e => summary.entryIds.includes(e.id)), compactTurns: [{ ...summary, stats: { ...summary.stats, turn: 10 }, cumulativeElapsedMs: 94_000 }], leafId: 'r2' } as SessionDetail)
  const stats = latestStats(s)
  expect(stats.elapsedMs + stats.turnElapsedMs).toBe(94_000)
  expect(s.turnBase).toBe(9)
  expect(s.turn).toBe(10)
  let live = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't3', timestamp: 6_000 })
  live = applyEvent(live, { type: 'tool_execution_end', toolCallId: 't3', durationMs: 2_000, result: 'done' })
  const extended = latestStats(live)
  expect(extended.elapsedMs).toBe(90_000)
  expect(extended.turnElapsedMs).toBe(7_000)
  expect(extended.elapsedMs + extended.turnElapsedMs).toBe(97_000)
})

test('parallel tool completion does not settle a still-running sibling', () => {
  const entries = [msg('u1', '', 'user', { timestamp: 1_000 }), msg('a1', 'u1', 'assistant', {
    timestamp: 2_000, content: [{ type: 'toolCall', id: 'slow', name: 'Read' }, { type: 'toolCall', id: 'fast', name: 'Read' }],
  })]
  let s = loadHistory({ entries, leafId: 'a1', running: true } as SessionDetail)
  for (const toolCallId of ['slow', 'fast']) s = applyEvent(s, { type: 'tool_execution_start', toolCallId, timestamp: 2_000 })
  s = applyEvent(s, { type: 'tool_execution_end', toolCallId: 'fast', durationMs: 10, result: 'ok' })
  expect([...turnStats(s.nodes).values()][0].live).toBe(true)
  s = hydrateEntries(s, [entries[0]])
  expect(s.nodes.find(n => n.id === 'slow')).toMatchObject({ running: true })
  expect(s.nodes.find(n => n.id === 'fast')).toMatchObject({ running: false, result: 'ok' })
  expect([...turnStats(s.nodes).values()][0].live).toBe(true)
  // A later model request, unlike a sibling tool result, proves that the
  // preceding batch has finished even if this browser lost its end event.
  s = applyEvent(s, { type: 'message_start', message: { role: 'assistant', timestamp: 4_000 } })
  expect(s.nodes.find(n => n.id === 'slow')).toMatchObject({ running: false })
})

test('applyTail settles a running tool the transcript now holds a result for', () => {
  const s = view({
    busy: true,
    nodes: [
      { kind: 'user', id: 'u1', text: 'hi', content: [] },
      { kind: 'tool', id: 'tc1', name: 'Bash', running: true },
    ],
  })
  const detail = {
    id: 's', running: true, leafId: 'tr1',
    entries: [
      msg('u1', '', 'user'),
      msg('a1', 'u1', 'assistant', { content: [{ type: 'toolCall', id: 'tc1', name: 'Bash', arguments: { command: 'x' } }] }),
      { type: 'message', id: 'tr1', parentId: 'a1', message: { role: 'toolResult', toolCallId: 'tc1', toolName: 'Bash', content: [{ type: 'text', text: 'done' }] } },
    ],
  } as SessionDetail
  const tool = applyTail(s, detail).nodes.find(n => n.kind === 'tool' && n.id === 'tc1')
  expect(tool && tool.kind === 'tool' ? tool.running : true).toBeFalsy()
  expect(tool && tool.kind === 'tool' ? tool.result : undefined).toBe('done')
})

test('applyTail drops a stale live node a newer on-screen node continued past', () => {
  const s = view({
    busy: true,
    nodes: [
      { kind: 'user', id: 'u1', text: 'hi', content: [] },
      { kind: 'tool', id: 'tc1', name: 'Bash', running: true },
      { kind: 'assistant', id: 'a2', text: 'next' },
    ],
  })
  const detail = {
    id: 's', running: true, leafId: 'a2',
    entries: [msg('u1', '', 'user'), msg('a2', 'u1', 'assistant', { content: [{ type: 'text', text: 'next' }] })],
  } as SessionDetail
  expect(applyTail(s, detail).nodes.some(n => n.kind === 'tool' && n.id === 'tc1')).toBe(false)
})


test('keep=0 recognizes live starts already counted by the hidden pending batch', () => {
  const { entries, summary } = compactFixture()
  const hidden: CompactTurn = { ...summary, tailId: 'a2', entryIds: ['u1'], visibleNodeIds: ['u1'], omittedNodeIds: [], hiddenCount: 4, toolStates: [{ id: 't2', finished: false }] }
  let s = loadHistory({ entries: [entries[0]], compactTurns: [hidden], leafId: 'a2', running: true } as SessionDetail)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't2', toolName: 'Read', timestamp: 4_000 })
  expect([...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)?.tools).toBe(2)
})


test('keep=0 recovery retires a hidden old batch using its server assistant boundary', () => {
  const { entries, summary } = compactFixture()
  let s = loadHistory({ entries: entries.slice(0, 2), leafId: 'a1', running: true } as SessionDetail)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't1', toolName: 'Read', timestamp: 2_100 })
  const hidden: CompactTurn = { ...summary, entryIds: ['u1'], visibleNodeIds: ['u1'], omittedNodeIds: [], hiddenCount: 4, assistantAt: 4_000 }
  s = applyTail(s, { entries: [entries[0]], compactTurns: [hidden], leafId: 'r2', running: true } as SessionDetail)
  expect(s.nodes.some(n => n.kind === 'tool' && n.running)).toBe(false)
  expect([...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)?.tools).toBe(2)
})


test('a delayed compact projection cannot regress the frontier, prompt or statistics', () => {
  const { entries, summary } = compactFixture()
  const newer: CompactTurn = { ...summary, tailId: 'a5', entryIds: ['u1', 'a5'], visibleNodeIds: ['u1', 'a5'], entryCount: 10, stepCount: 5, stats: { ...summary.stats, steps: 5 } }
  const older: CompactTurn = { ...summary, tailId: 'a3', entryIds: ['u1', 'a3'], visibleNodeIds: ['u1', 'a3'], entryCount: 6, stepCount: 3, stats: { ...summary.stats, steps: 3 } }
  let s = loadHistory({ entries: [entries[0], msg('a5', 'a4', 'assistant')], compactTurns: [newer], leafId: 'a5' } as SessionDetail)
  s = hydrateEntries(s, [entries[0], msg('a3', 'a2', 'assistant')], { compactTurns: [older] })
  expect(s.compactTurns?.[0].tailId).toBe('a5')
  expect(s.nodes.some(n => n.id === 'u1')).toBe(true)
  expect(latestStats(s).steps).toBe(5)
  expect(latestStats(s).turnStartedAt).toBe(1_000)
})


test('hidden completed batch overlap and delayed canonical result preserve prompt and counts', () => {
  const user = msg('u1', '', 'user', { timestamp: 1_000 })
  const baseStats = [...turnStats([ { kind: 'user', id: 'u1', text: '', content: [], ts: 1_000 }, { kind: 'assistant', id: 'a1', text: '', ts: 2_000 }, { kind: 'tool', id: 't1', name: 'Read' } ]).values()][0]
  const summary: CompactTurn = { id: 'u1', tailId: 'a1', entryIds: ['u1'], visibleNodeIds: ['u1'], hiddenCount: 2, entryCount: 2, stepCount: 1, stats: baseStats, toolStates: [{ id: 't1', finished: false }] }
  let s = loadHistory({ entries: [user], compactTurns: [summary], leafId: 'a1', running: true } as SessionDetail)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't1', toolName: 'Read', timestamp: 2_100 })
  s = applyEvent(s, { type: 'tool_execution_end', toolCallId: 't1', durationMs: 900, isError: true, result: 'failed' })
  const completed: CompactTurn = { ...summary, tailId: 'r1', entryCount: 3, stats: { ...baseStats, elapsedMs: 2_000, toolFailures: 1 }, toolStates: [{ id: 't1', finished: true, isError: true }] }
  s = applyTail(s, { entries: [user], compactTurns: [completed], leafId: 'r1', running: true } as SessionDetail)
  let stats = [...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)!
  expect(stats.tools).toBe(1)
  expect(stats.toolFailures).toBe(1)
  s = applyEvent(s, { type: 'message_end', entryId: 'r1', parentId: 'a1', message: { role: 'toolResult', toolCallId: 't1', timestamp: 3_000, durationMs: 900, isError: true, content: [{ type: 'text', text: 'failed' }] } })
  s = hydrateEntries(s, [user])
  expect(s.entries.find(e => e.id === 'r1')?.parentId).toBe('a1')
  expect(s.nodes.some(n => n.id === 'u1')).toBe(true)
  stats = [...projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()].at(-1)!
  expect(stats.tools).toBe(1)
  expect(stats.toolFailures).toBe(1)
})

test('tool overlay elapsed and trajectory survive hydration until toolResult arrives', () => {
  const entries = [msg('u1', '', 'user', { timestamp: 1_000 }), msg('a1', 'u1', 'assistant', { timestamp: 2_000, content: [{ type: 'toolCall', id: 't1', name: 'Read' }] })]
  let s = loadHistory({ entries, leafId: 'a1', running: true } as SessionDetail)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 't1', timestamp: 2_000 })
  s = applyEvent(s, { type: 'tool_execution_end', toolCallId: 't1', durationMs: 5_000, isError: true, result: 'failed' })
  expect(latestStats(s).turnElapsedMs).toBe(6_000)
  s = hydrateEntries(s, [entries[0]])
  expect(latestStats(s).turnElapsedMs).toBe(6_000)
  expect(s.records.find(r => r.id === 't1')).toMatchObject({ durationMs: 5_000, output: 'failed', error: true, running: false })
})
