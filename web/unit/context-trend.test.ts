import { expect, test } from 'bun:test'
import { trendRows } from '../src/features/context/trend'
import type { ContextTrendPoint } from '../src/features/context/model'

const point = (id: string, tokens?: number, basis: ContextTrendPoint['basis'] = 'usage', categories = {}): ContextTrendPoint => ({
  requestId: id, turn: 1, step: 1, tokens, basis, categories, approximate: true, partial: true,
})

test('metadata-only history renders reported input, not zero-size estimated bars', () => {
  const { rows, maximum, reportedMaximum } = trendRows([point('a', 123_456)], 0, 1, 'total')
  expect(rows[0].reference).toBe(123_456)
  expect(rows[0].positive).toBe(0)
  expect(maximum).toBe(1)
  expect(reportedMaximum).toBe(200_000)
})

test('reported deltas remain signed and use the predecessor outside the visible page', () => {
  const { rows, reportedMaximum } = trendRows([point('a', 250_000), point('b', 80_000)], 1, 2, 'delta')
  expect(rows[0].reference).toBe(-170_000)
  expect(reportedMaximum).toBe(200_000)
})

test('unavailable input, changed measurement basis and first deltas are not invented zero baselines', () => {
  const points = [point('a', 100), point('b', 200, 'meter'), point('c')]
  expect(trendRows(points, 0, 3, 'delta').rows.map(row => row.reference)).toEqual([undefined, undefined, undefined])
})

test('loaded-category deltas are separate from reported-input deltas', () => {
  const points = [point('a', 100, 'usage', { agent: 10 }), point('b', 150, 'usage', { agent: 15 })]
  const row = trendRows(points, 1, 2, 'delta').rows[0]
  expect(row.reference).toBe(50)
  expect(row.positive).toBe(5)
  expect(row.values.find(value => value.id === 'agent')?.value).toBe(5)
})

test('large remote input never shrinks known colored categories into a hairline', () => {
  const { maximum, reportedMaximum } = trendRows([point('a', 200_000, 'usage', { agent: 100, assistant: 400 })], 0, 1, 'total')
  expect(maximum).toBe(500)
  expect(reportedMaximum).toBe(200_000)
})
