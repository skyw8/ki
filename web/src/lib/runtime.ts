import type { AgentSnapshot, ProcessSnapshot } from '../api/types'

export function mergeAgentSnapshots(current: AgentSnapshot[], incoming: AgentSnapshot[], incremental = false): AgentSnapshot[] {
 const values = new Map((incremental ? current : []).map(agent => [agent.task_name, agent]))
 for (const agent of incoming) {
  const previous = current.find(value => value.task_name === agent.task_name)
  const stale = previous && ((previous.generation ?? 0) > (agent.generation ?? 0) ||
   (previous.generation ?? 0) === (agent.generation ?? 0) && (previous.revision ?? 0) > (agent.revision ?? 0))
  values.set(agent.task_name, stale ? previous : agent)
 }
 return [...values.values()].sort((a, b) => Number(b.status === 'running') - Number(a.status === 'running') ||
  (b.last_activity_at ?? '').localeCompare(a.last_activity_at ?? '') || a.task_name.localeCompare(b.task_name)).slice(0, 128)
}

export function mergeProcessSnapshots(current: ProcessSnapshot[], incoming: ProcessSnapshot[], incremental = false): ProcessSnapshot[] {
 const values = new Map((incremental ? current : []).map(process => [process.session_id, process]))
 for (const process of incoming) {
  const previous = current.find(value => value.session_id === process.session_id)
  values.set(process.session_id, previous && (previous.revision ?? 0) > (process.revision ?? 0) ? previous : process)
 }
 return [...values.values()].sort((a, b) => Number(b.status === 'running') - Number(a.status === 'running') ||
  b.started_at.localeCompare(a.started_at)).slice(0, 256)
}
