import { expect, test } from '@playwright/test'
import { cacheHitPercent, emptyView, formatDuration, formatTokensPerSecond, latestStats } from '../src/model.ts'
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

test('latestStats keeps only the newest assistant on the leaf path', () => {
  const entries: Entry[] = [
    msg('u1', '', 'user'),
    msg('a1', 'u1', 'assistant', {
      usage: { input: 10, output: 4, cacheRead: 90, cacheWrite: 0 },
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
  expect(s.input).toBe(3 + 7)
  expect(s.cacheRead).toBe(7)
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
      { kind: 'assistant', id: 'live-asst-1', text: 'new', usage: { input: 8, output: 2 }, ttftMs: 100, latencyMs: 600 },
    ],
  }))
  expect(s.input).toBe(8)
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
  expect(s.input).toBe(1)
  expect(s.decodeMs).toBe(200)
  expect(s.decodeTokens).toBe(1)
})

test('latestStats drops decode when the step reported no TTFT', () => {
  const s = latestStats(view({
    nodes: [{ kind: 'assistant', id: 'a1', text: 'ok', usage: { input: 8, output: 2 }, latencyMs: 500 }],
  }))
  expect(s.input).toBe(8)
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

test('format helpers match the compact strip', () => {
  expect(formatDuration(900)).toBe('0.9s')
  expect(formatDuration(800)).toBe('0.8s')
  expect(formatDuration(162_000)).toBe('2m42s')
  expect(formatTokensPerSecond(8.24)).toBe('8.2')
  expect(formatTokensPerSecond(259.4)).toBe('259')
})
