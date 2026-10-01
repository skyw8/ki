import { expect, test } from 'bun:test'
import { applyEvent, applyRuntimeCatalog, loadHistory } from '../src/lib/model'
import type { AgentSnapshot, ProcessSnapshot } from '../src/api/types'
import { canonicalToolName } from '../src/lib/toolname'

const agent = (generation: number, revision: number): AgentSnapshot => ({ task_name: '/root/review', session_id: 'child', agent_id: 'a', generation, revision, status: 'running', phase: 'executing' })
const process = (revision: number, status: 'running' | 'exited'): ProcessSnapshot => ({ session_id: 71, revision, cmd: 'server', tty: false, status, total_bytes: 0, started_at: '2026-10-01T01:00:00Z' })

test('runtime progress stays independent of transcript, busy state and delayed older snapshots', () => {
 let view = loadHistory({ id: 'root', entries: [], running: false, agents: [agent(1, 1)], processes: [process(1, 'running')] })
 const leaf = view.leafId
 view = applyEvent(view, { type: 'agent_updated', agent: agent(2, 4) })
 view = applyEvent(view, { type: 'process_updated', process: process(5, 'exited') })
 view = applyEvent(view, { type: 'agent_updated', agent: agent(1, 100) })
 view = applyEvent(view, { type: 'process_updated', process: process(2, 'running') })
 view = applyRuntimeCatalog(view, { id: 'root', agents: [agent(2, 3)], processes: [process(4, 'running')] })
 expect(view.agents?.[0].revision).toBe(4)
 expect(view.agents?.[0].generation).toBe(2)
 expect(view.processes?.[0].status).toBe('exited')
 expect(view.nodes).toEqual([])
 expect(view.leafId).toBe(leaf)
 expect(view.busy).toBe(false)
 // A fresh runtime after server restart has no process handles.
 view = applyRuntimeCatalog(view, { id: 'root', agents: [], processes: [] })
 expect(view.agents).toEqual([])
 expect(view.processes).toEqual([])
})

test('tool display normalizes snake and Pascal names without changing the protocol message', () => {
 expect(canonicalToolName('WriteStdin')).toBe('write_stdin')
 expect(canonicalToolName('spawn_agent')).toBe('spawn_agent')
 expect(canonicalToolName('HTTPFetch')).toBe('http_fetch')
})
