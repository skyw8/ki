import { expect, test } from 'bun:test'
import { coverageMismatches, reportSpecs } from '../scripts/e2e-coverage'

test('coverage rejects a duplicate replacing a missing test even when counts match', () => {
  expect(coverageMismatches(['a', 'b'], ['a', 'a'])).toEqual(['duplicate test: a', 'missing test: b'])
  expect(coverageMismatches(['a'], ['a', 'unknown'])).toEqual(['unexpected test: unknown'])
  expect(coverageMismatches(['a', 'b'], ['b', 'a'])).toEqual([])
})

test('report discovery keeps nested titles and execution excludes skipped or unstarted tests', () => {
  const spec = (id: string, status?: string) => ({
    id, title: id, file: 'view.spec.ts', ok: true,
    tests: [{ results: status ? [{ status }] : [] }],
  })
  const report = { suites: [{ title: 'view.spec.ts', line: 0, suites: [{
    title: 'phone', line: 12, specs: [spec('passed', 'passed'), spec('skipped', 'skipped'), spec('unstarted')],
  }] }] }
  const specs = reportSpecs(report)
  expect(specs.map(spec => spec.path)).toEqual(['phone › passed', 'phone › skipped', 'phone › unstarted'])
  expect(coverageMismatches(specs.map(spec => spec.id), specs.filter(spec => spec.executed).map(spec => spec.id)))
    .toEqual(['missing test: skipped', 'missing test: unstarted'])
})
