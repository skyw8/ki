import type { TrajRecord } from '../../api/types'

export type ToolActivity = { name: string; calls: number; failures: number }

export function buildToolActivity(records: TrajRecord[]): ToolActivity[] {
  // Calls, results, history hydration and live overlays share a call identity.
  // Count it once; a partial duplicate must not erase a recorded failure.
  const calls = new Map<string, { name: string; failed: boolean }>()
  for (const record of records) {
    if (record.kind !== 'tool') continue
    const previous = calls.get(record.id)
    calls.set(record.id, {
      name: record.name || previous?.name || 'tool',
      failed: !!record.error || !!previous?.failed,
    })
  }
  const tools = new Map<string, ToolActivity>()
  for (const call of calls.values()) {
    const row = tools.get(call.name) ?? { name: call.name, calls: 0, failures: 0 }
    row.calls++
    if (call.failed) row.failures++
    tools.set(call.name, row)
  }
  return [...tools.values()].sort((a, b) => b.calls - a.calls || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
}
