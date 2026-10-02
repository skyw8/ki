import { expect, test } from 'bun:test'
import type { TrajRecord } from '../src/api/types'
import { buildToolActivity } from '../src/features/context/tool-activity'
import { applyEvent, hydrateEntries, loadHistory } from '../src/lib/model'

const tool = (id: string, name: string, extra: Partial<TrajRecord> = {}): TrajRecord =>
  ({ id, name, kind: 'tool', turn: 1, preview: '', ...extra })

test('tool ranking counts calls, puts most-used first and sorts ties by name', () => {
  const rows = buildToolActivity([
    tool('r1', 'read'), tool('g1', 'grep'), tool('r2', 'read', { error: true }),
    tool('e1', 'exec', { error: true }), tool('g2', 'grep', { running: true }),
    tool('other', 'assistant', { kind: 'assistant' }),
  ])
  expect(rows).toEqual([
    { name: 'grep', calls: 2, failures: 0 },
    { name: 'read', calls: 2, failures: 1 },
    { name: 'exec', calls: 1, failures: 1 },
  ])
})

test('a repeated call identity counts once and partial duplicates do not erase errors', () => {
  const records = [
    tool('a', 'read', { error: true }), tool('a', '', { running: true }),
    tool('b', 'read'), tool('b', 'read', { error: true }),
  ]
  const original = structuredClone(records)
  expect(buildToolActivity(records)).toEqual([{ name: 'read', calls: 2, failures: 2 }])
  expect(records).toEqual(original)
})

test('nested Code Mode calls are ranked independently from the enclosing exec', () => {
  expect(buildToolActivity([
    tool('outer', 'exec'), tool('inner', 'read', { parentCallId: 'outer', error: true }),
    tool('next', 'read', { parentCallId: 'outer' }),
  ])).toEqual([{ name: 'read', calls: 2, failures: 1 }, { name: 'exec', calls: 1, failures: 0 }])
})

test('empty history has no ranking and unnamed calls have a readable fallback', () => {
  expect(buildToolActivity([])).toEqual([])
  expect(buildToolActivity([tool('unknown', '')])).toEqual([{ name: 'tool', calls: 1, failures: 0 }])
})

test('live failure settlement and body hydration update the same ranked call', () => {
  let view = loadHistory({ id: 'session', entries: [], running: true })
  const start = { type: 'tool_execution_start', toolCallId: 'read-1', toolName: 'read' }
  view = applyEvent(applyEvent(view, start), start)
  expect(buildToolActivity(view.records)).toEqual([{ name: 'read', calls: 1, failures: 0 }])
  view = applyEvent(view, {
    type: 'tool_execution_end', toolCallId: 'read-1', toolName: 'read', isError: true,
    result: { content: [{ type: 'text', text: 'not found' }] },
  })
  expect(buildToolActivity(view.records)).toEqual([{ name: 'read', calls: 1, failures: 1 }])
  view = hydrateEntries(view, [{
    type: 'message', id: 'result', message: {
      role: 'toolResult', toolCallId: 'read-1', toolName: 'read', isError: true,
      content: [{ type: 'text', text: 'not found' }],
    },
  }])
  expect(buildToolActivity(view.records)).toEqual([{ name: 'read', calls: 1, failures: 1 }])
})
