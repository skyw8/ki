import { expect, test } from '@playwright/test'
import { cacheHitPercent, emptyView, formatCost, formatDuration, formatTokens, formatTokensPerSecond, sessionStats } from '../src/model.ts'
import type { Entry, ViewState } from '../src/types.ts'

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

test('sessionStats walks the leaf path so compacted assistants still count', () => {
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
  const s = sessionStats(view({
    leafId: 'a2',
    allEntries: entries,
    nodes: [
      { kind: 'user', id: 'u2', text: 'hi', content: [] },
      { kind: 'assistant', id: 'a2', text: 'ok', usage: { input: 3, output: 1, cacheRead: 7 }, ttftMs: 200, latencyMs: 700 },
    ],
  }))
  expect(s.turns).toBe(2)
  expect(s.steps).toBe(3)
  expect(s.input).toBe(10 + 90 + 20 + 3 + 7)
  expect(s.output).toBe(4 + 5 + 1)
  expect(s.cacheRead).toBe(97)
  expect(s.hasCost).toBe(true)
  expect(s.cost).toBeCloseTo(0.03)
  expect(s.ttftSteps).toBe(2)
  expect(s.ttftMs).toBe(1000)
  expect(s.decodeMs).toBe(1000 + 500)
  expect(s.decodeTokens).toBe(5)
})

test('sessionStats adds live nodes that are not yet in the jsonl path', () => {
  const s = sessionStats(view({
    nodes: [
      { kind: 'user', id: 'opt-user-1', text: 'hi', content: [] },
      { kind: 'assistant', id: 'live-asst-1', text: 'ok', usage: { input: 8, output: 2 }, ttftMs: 100, latencyMs: 600 },
    ],
  }))
  expect(s.turns).toBe(1)
  expect(s.steps).toBe(1)
  expect(s.input).toBe(8)
  expect(s.output).toBe(2)
  expect(s.ttftMs).toBe(100)
  expect(s.decodeMs).toBe(500)
  expect(s.decodeTokens).toBe(2)
})

test('sessionStats skips a streaming assistant and a sibling branch', () => {
  const s = sessionStats(view({
    leafId: 'a1',
    allEntries: [
      msg('u1', '', 'user'),
      msg('a1', 'u1', 'assistant', { usage: { input: 1, output: 1 } }),
      msg('u2', 'u1', 'user'),
      msg('a2', 'u2', 'assistant', { usage: { input: 50, output: 50 } }),
    ],
    nodes: [
      { kind: 'assistant', id: 'a1', text: 'ok', usage: { input: 1, output: 1 } },
      { kind: 'assistant', id: 'stream', text: '…', streaming: true, usage: { input: 9, output: 9 } },
    ],
  }))
  expect(s.turns).toBe(1)
  expect(s.steps).toBe(1)
  expect(s.input).toBe(1)
  expect(s.output).toBe(1)
})

test('cacheHitPercent needs billed input and a cache read', () => {
  const base = sessionStats(view())
  expect(cacheHitPercent({ ...base, input: 0, cacheRead: 0 })).toBeNull()
  expect(cacheHitPercent({ ...base, input: 100, cacheRead: 0 })).toBeNull()
  expect(cacheHitPercent({ ...base, input: 100, cacheRead: 90 })).toBe(90)
})

test('format helpers match the compact strip', () => {
  expect(formatTokens(517)).toBe('517')
  expect(formatTokens(12_200)).toBe('12.2K')
  expect(formatTokens(1_200_000)).toBe('1.2M')
  expect(formatDuration(800)).toBe('0.8s')
  expect(formatDuration(162_000)).toBe('2m42s')
  expect(formatTokensPerSecond(8.24)).toBe('8.2')
  expect(formatTokensPerSecond(20.4)).toBe('20')
  expect(formatCost(0.00321)).toBe('0.0032')
  expect(formatCost(1.2)).toBe('1.20')
})
