import { beforeEach, expect, test } from 'bun:test'
import { durationColumns, durationText } from '../src/lib/duration'
import { COLLAPSED_TOOL_HEIGHT, forgetRowHeights, rememberRowHeight, rowBodyToken, rowHeightEstimate, UNKNOWN_WIDTH, type RowHeightRevision } from '../src/lib/rowHeight'

beforeEach(forgetRowHeights)

test('duration precision stays fixed for short runs and preserves long-run seconds', () => {
  const cases = [
    [0, '0.0ms'], [999, '999.0ms'], [1000, '1.0s'],
    [9900, '9.9s'], [10_000, '10.0s'], [10_200, '10.2s'],
    [59_900, '59.9s'], [60_000, '1m00s'], [61_000, '1m01s'],
    [1_051_000, '17m31s'], [3_599_000, '59m59s'], [3_600_000, '1h00m00s'], [3_601_000, '1h00m01s'],
    [86_399_000, '23h59m59s'], [86_400_000, '1d00h00m00s'], [86_401_000, '1d00h00m01s'],
    [864_001_000, '10d00h00m01s'], [86_400_001_000, '1000d00h00m01s'],
  ] as const
  for (const [ms, text] of cases) expect(durationText(ms)).toBe(text)
  for (const ms of [-1, NaN, Infinity]) expect(durationText(ms)).toBe('0.0ms')
})

test('duration slots stay compact and stable within each format without shrinking long days', () => {
  for (const [values, columns] of [
    [[0, 9, 99, 999], 7],
    [[1000, 9900, 10_000, 59_900], 5],
    [[60_000, 61_000, 599_000, 3_599_000], 6],
    [[3_600_000, 3_601_000, 35_999_000, 86_399_000], 9],
    [[86_400_000, 86_401_000, 863_999_000], 11],
    [[864_000_000, 864_001_000], 12],
    [[86_400_000_000, 86_400_001_000], 14],
  ] as const) {
    for (const ms of values) {
      const text = durationText(ms)
      expect(durationColumns(text)).toBe(columns)
      expect(durationColumns(text)).toBeGreaterThanOrEqual(text.length)
      expect(durationColumns(text) - text.length).toBeLessThanOrEqual(2)
    }
  }
  expect(durationColumns(durationText(999.99))).toBe(8)
})

test('height reuse requires body identity and presentation, not equal length', () => {
  const body = { text: 'same length', origin: 'agent:child' }
  const revision = { body: rowBodyToken(body), decoration: 'zh:collapsed:turn-1' }
  rememberRowHeight('entry', 390, 444, body.text.length, revision)
  expect(rowHeightEstimate('entry', 390, body.text.length, { ...revision })).toBe(444)
  expect(rowHeightEstimate('entry', 390, body.text.length, { ...revision, body: rowBodyToken({ text: 'other\nlines' }) })).not.toBe(444)
  expect(rowHeightEstimate('entry', 390, body.text.length, { ...revision, decoration: 'en:expanded:turn-1' })).not.toBe(444)
  expect(rowHeightEstimate('entry', 834, body.text.length, revision)).not.toBe(444)
  expect(rowHeightEstimate('entry', UNKNOWN_WIDTH, body.text.length, revision)).not.toBe(444)
})

test('tools, images and turn footers invalidate zero-character measurements', () => {
  const tool = { result: 'preview' }
  const revision = { body: rowBodyToken(tool), decoration: 'tool' }
  rememberRowHeight('tool', 800, 40, 0, revision)
  expect(rowHeightEstimate('tool', 800, 0, revision)).toBe(40)
  expect(rowHeightEstimate('tool', 800, 0, { body: rowBodyToken({ result: 'full' }), decoration: 'tool' })).not.toBe(40)
  expect(rowHeightEstimate('tool', 800, 0, { ...revision, decoration: 'tool:footer' })).not.toBe(40)
})

test('cached revisions contain only small tokens, never a body reference or copy', () => {
  const body = { text: 'large retained message'.repeat(10_000), images: ['large image payload'] }
  const revision: RowHeightRevision = { body: rowBodyToken(body), decoration: 'zh:tool' }
  expect(revision.body).toBe(rowBodyToken(body))
  expect(revision.body).not.toBe(rowBodyToken({ ...body }))
  expect(rowBodyToken(undefined)).toBe(0)
  expect(typeof revision.body).toBe('number')
  expect(Object.values(revision).some(value => typeof value === 'object')).toBe(false)
  expect(JSON.stringify(revision).length).toBeLessThan(100)
})

test('known collapsed tool geometry avoids spurious four-pixel scroll corrections', () => {
  const revision = { body: rowBodyToken({ name: 'Read' }), decoration: 'collapsed' }
  expect(rowHeightEstimate('tool', 390, 0, revision, COLLAPSED_TOOL_HEIGHT)).toBe(68)
  rememberRowHeight('tool', 390, 72, 0, revision)
  expect(rowHeightEstimate('tool', 390, 0, revision, COLLAPSED_TOOL_HEIGHT)).toBe(72)
})
