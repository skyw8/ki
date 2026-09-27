import { basename } from 'node:path'

interface ReportSuite {
  title: string
  file?: string
  line?: number
  specs?: Array<{
    id: string
    title: string
    file: string
    ok: boolean
    tests: Array<{ results: Array<{ status: string }> }>
  }>
  suites?: ReportSuite[]
}

export interface ReportSpec {
  id: string
  file: string
  path: string
  executed: boolean
  ok: boolean
}

// Use the same JSON identities for discovery and execution. Counts alone can
// accept a duplicate test in place of a missing one, or count a skipped spec.
export function reportSpecs(report: { suites?: ReportSuite[] }): ReportSpec[] {
  const specs: ReportSpec[] = []
  const visit = (suites: ReportSuite[], parents: string[]): void => {
    for (const suite of suites) {
      const path = suite.line === 0 ? [] : [...parents, suite.title]
      for (const spec of suite.specs ?? []) {
        if (!spec.id) throw new Error('Playwright report contains a test without an id')
        const results = spec.tests.flatMap(test => test.results)
        specs.push({
          id: spec.id,
          file: basename(spec.file),
          path: [...path, spec.title].join(' › '),
          executed: results.some(result => result.status !== 'skipped'),
          ok: spec.ok,
        })
      }
      visit(suite.suites ?? [], path)
    }
  }
  visit(report.suites ?? [], [])
  return specs
}

export function coverageMismatches(expected: string[], executed: string[]): string[] {
  const remaining = new Set(expected)
  const seen = new Set<string>()
  const errors: string[] = []
  for (const id of executed) {
    if (seen.has(id)) errors.push(`duplicate test: ${id}`)
    else if (!remaining.delete(id)) errors.push(`unexpected test: ${id}`)
    seen.add(id)
  }
  for (const id of remaining) errors.push(`missing test: ${id}`)
  return errors
}
