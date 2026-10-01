import { describe, expect, test } from 'bun:test'
import type { AgentSnapshot } from '../src/api/types'
import { agentTree } from '../src/lib/runtimeTree'

const agent = (task_name: string): AgentSnapshot => ({ task_name, session_id: task_name, status: 'running' })

describe('logical agent tree', () => {
  test('nests canonical paths regardless of snapshot delivery order', () => {
    const tree = agentTree(['/root/review/check', '/root/build', '/root', '/root/review'].map(agent))
    expect(tree.map(b => b.agent.task_name)).toEqual(['/root'])
    expect(tree[0].children.map(b => b.agent.task_name)).toEqual(['/root/build', '/root/review'])
    expect(tree[0].children[1].children[0].agent.task_name).toBe('/root/review/check')
  })
  test('missing parents never hide descendants or match path prefixes', () => {
    const tree = agentTree(['/root', '/root/reviewer', '/root/review/check', '/elsewhere/task'].map(agent))
    expect(tree.map(b => b.agent.task_name)).toEqual(['/root', '/elsewhere/task'])
    expect(tree[0].children.map(b => b.agent.task_name)).toEqual(['/root/reviewer', '/root/review/check'])
  })
  test('empty snapshots have no synthetic agents and inputs stay unchanged', () => {
    expect(agentTree([])).toEqual([])
    const snapshots = [agent('/root/child'), agent('/root')]
    agentTree(snapshots)
    expect(snapshots.map(a => a.task_name)).toEqual(['/root/child', '/root'])
  })
})
