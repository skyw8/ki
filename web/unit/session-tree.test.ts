import { describe, expect, test } from 'bun:test'
import type { SessionInfo, WorkspaceInfo } from '../src/api/types'
import { ancestorsOf, buildSessionForest, groupedSessionChildren, orderedChildren, topLevelRoot } from '../src/lib/session-tree'

function session(id: string, fields: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id,
    cwd: '/project',
    provider: 'test',
    model: 'test',
    title: id,
    workspaceId: 'workspace',
    ...fields,
  }
}

function side(id: string, sourceSessionId?: string, fields: Partial<SessionInfo> = {}): SessionInfo {
  return session(id, { purpose: 'question_side', sourceSessionId, sourceQuestionItemId: `question-${id}`, ...fields })
}

const workspace: WorkspaceInfo = {
  id: 'workspace',
  path: '/project',
  title: 'Project',
  sessionIds: ['root', 'side-b', 'agent-b', 'side-a', 'agent-a'],
}

describe('session purpose tree', () => {
  test('side conversations nest independently of fork mode and ordinary flat forks remain roots', () => {
    const rows = [
      session('root'),
      side('side-a', 'root'),
      side('side-b', undefined, { parentSessionId: 'root', forkMode: 'flat' }),
      session('flat', { parentSessionId: 'root', forkMode: 'flat' }),
      session('agent-a', { parentSessionId: 'root', forkMode: 'tree', isAgent: true }),
    ]
    const forest = buildSessionForest(rows, [workspace])

    expect(orderedChildren(forest, 'root').map(row => row.id)).toEqual(['side-b', 'side-a', 'agent-a'])
    expect(forest.parentOf.has('flat')).toBe(false)
    expect(topLevelRoot(forest, 'side-a')).toBe('root')
    expect(ancestorsOf(forest, 'side-a')).toEqual(['root'])
    expect(forest.unresolved.size).toBe(0)
  })

  test('groups preserve workspace order without mixing side purpose into agents', () => {
    const forest = buildSessionForest([
      session('root'),
      side('side-a', 'root', { forkMode: 'tree' }),
      session('agent-a', { parentSessionId: 'root', forkMode: 'tree', isAgent: true }),
      side('side-b', 'root', { running: true }),
      session('agent-b', { parentSessionId: 'root', forkMode: 'tree', isAgent: true }),
    ], [workspace])
    const groups = groupedSessionChildren(forest, 'root')

    expect(groups.agents.map(row => row.id)).toEqual(['agent-b', 'agent-a'])
    expect(groups.sideChats.map(row => row.id)).toEqual(['side-b', 'side-a'])
    groups.agents.reverse()
    groups.sideChats.pop()
    expect(orderedChildren(forest, 'root').map(row => row.id)).toEqual(['side-b', 'agent-b', 'side-a', 'agent-a'])
    expect(groupedSessionChildren(forest, 'missing')).toEqual({ agents: [], sideChats: [], forks: [] })
  })

  test('missing, self and cross-workspace side parents remain recoverable unresolved roots', () => {
    const rows = [
      session('root'),
      side('missing', 'unknown'),
      side('no-source'),
      side('self', 'self'),
      side('cross', 'root', { workspaceId: 'other' }),
    ]
    const forest = buildSessionForest(rows, [workspace])

    expect([...forest.unresolved]).toEqual(['missing', 'no-source', 'self', 'cross'])
    for (const row of rows.slice(1)) {
      expect(topLevelRoot(forest, row.id)).toBe(row.id)
      expect(forest.parentOf.has(row.id)).toBe(false)
    }
    expect(orderedChildren(forest, 'root')).toEqual([])
  })

  test('side conversations cannot have nested side conversations or agents', () => {
    const forest = buildSessionForest([
      session('root'),
      side('side', 'root'),
      side('nested', 'side'),
      session('agent', { parentSessionId: 'side', forkMode: 'tree' }),
      side('cycle-a', 'cycle-b'),
      side('cycle-b', 'cycle-a'),
    ], [workspace])

    expect(orderedChildren(forest, 'root').map(row => row.id)).toEqual(['side'])
    expect(orderedChildren(forest, 'side')).toEqual([])
    expect([...forest.unresolved]).toEqual(['nested', 'agent', 'cycle-a', 'cycle-b'])
    expect(ancestorsOf(forest, 'cycle-a')).toEqual([])
  })

  test('a side referencing a cyclic agent chain remains an unresolved root', () => {
    const forest = buildSessionForest([
      session('agent-a', { parentSessionId: 'agent-b', forkMode: 'tree' }),
      session('agent-b', { parentSessionId: 'agent-a', forkMode: 'tree' }),
      side('side', 'agent-a'),
    ], [workspace])

    expect(forest.parentOf.size).toBe(0)
    expect([...forest.unresolved]).toEqual(['agent-a', 'agent-b', 'side'])
    expect(topLevelRoot(forest, 'side')).toBe('side')
    expect(groupedSessionChildren(forest, 'agent-a')).toEqual({ agents: [], sideChats: [], forks: [] })
  })

  test('deep agent ancestry and its workspace boundaries stay intact', () => {
    const forest = buildSessionForest([
      session('root'),
      session('agent-a', { parentSessionId: 'root', forkMode: 'tree' }),
      session('agent-b', { parentSessionId: 'agent-a', forkMode: 'tree' }),
      session('cross', { parentSessionId: 'agent-a', forkMode: 'tree', workspaceId: 'other' }),
    ], [workspace])

    expect(ancestorsOf(forest, 'agent-b')).toEqual(['agent-a', 'root'])
    expect(topLevelRoot(forest, 'agent-b')).toBe('root')
    expect(forest.unresolved.has('cross')).toBe(true)
  })
})
