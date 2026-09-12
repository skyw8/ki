import { expect, test } from '@playwright/test'
import { cacheHitPercent, cacheHitRate, cacheMisses, emptyView, formatCost, formatDuration, formatTokens, formatTokensPerSecond, latestStats } from '../src/model.ts'
import type { ChatNode, Entry, ViewState } from '../src/types.ts'

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
