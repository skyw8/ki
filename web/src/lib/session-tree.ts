import type { SessionInfo, WorkspaceInfo } from '../api/types'

// SessionForest is the sidebar's parent/child view over the full session list.
// It is intentionally independent of the "current" session: subagent sessions
// (forkMode=tree) nest under their parent everywhere, and the sidebar decides
// which branches are expanded.
export type SessionForest = {
  parentOf: Map<string, string>
  childrenOf: Map<string, SessionInfo[]>
  // unresolved holds tree children whose parent edge cannot be trusted (missing
  // parent, cross-workspace, self/cycle). They render as top-level rows so a
  // broken edge never hides a session.
  unresolved: Set<string>
  order: Map<string, number>
}

function sameWorkspace(a: SessionInfo, b: SessionInfo): boolean {
  return !a.workspaceId || !b.workspaceId || a.workspaceId === b.workspaceId
}

function sessionOrder(sessions: SessionInfo[], workspaces: WorkspaceInfo[]): Map<string, number> {
  const order = new Map<string, number>()
  let next = 0
  for (const workspace of workspaces) {
    for (const id of workspace.sessionIds ?? []) {
      if (!order.has(id)) order.set(id, next++)
    }
  }
  for (const session of sessions) {
    if (!order.has(session.id)) order.set(session.id, next++)
  }
  return order
}

function compareSessions(a: SessionInfo, b: SessionInfo, order: Map<string, number>): number {
  const byWorkspace = (order.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (order.get(b.id) ?? Number.MAX_SAFE_INTEGER)
  if (byWorkspace !== 0) return byWorkspace
  const byTime = (b.timestamp ?? '').localeCompare(a.timestamp ?? '')
  if (byTime !== 0) return byTime
  return a.id.localeCompare(b.id)
}

function cycleNodes(parentCandidates: Map<string, string>): Set<string> {
  const cycles = new Set<string>()
  for (const start of parentCandidates.keys()) {
    const seen = new Map<string, number>()
    const chain: string[] = []
    let cursor = start
    while (parentCandidates.has(cursor)) {
      const at = seen.get(cursor)
      if (at !== undefined) {
        for (const id of chain.slice(at)) cycles.add(id)
        break
      }
      seen.set(cursor, chain.length)
      chain.push(cursor)
      cursor = parentCandidates.get(cursor)!
    }
  }
  return cycles
}

function reachesCycle(id: string, parentCandidates: Map<string, string>, cycles: Set<string>): boolean {
  const seen = new Set<string>()
  let cursor = id
  while (parentCandidates.has(cursor)) {
    if (cycles.has(cursor) || seen.has(cursor)) return true
    seen.add(cursor)
    cursor = parentCandidates.get(cursor)!
  }
  return cycles.has(cursor)
}

export function buildSessionForest(sessions: SessionInfo[], workspaces: WorkspaceInfo[]): SessionForest {
  const byId = new Map(sessions.map(session => [session.id, session]))
  const order = sessionOrder(sessions, workspaces)
  const parentCandidates = new Map<string, string>()
  const unresolved = new Set<string>()

  for (const session of sessions) {
    if (session.forkMode !== 'tree' || !session.parentSessionId) continue
    const parent = byId.get(session.parentSessionId)
    if (!parent || parent.id === session.id || !sameWorkspace(parent, session)) {
      unresolved.add(session.id)
      continue
    }
    parentCandidates.set(session.id, parent.id)
  }

  const cycles = cycleNodes(parentCandidates)
  const parentOf = new Map<string, string>()
  for (const [childId, parentId] of parentCandidates) {
    if (cycles.has(childId) || reachesCycle(parentId, parentCandidates, cycles)) {
      unresolved.add(childId)
      continue
    }
    parentOf.set(childId, parentId)
  }

  const childrenOf = new Map<string, SessionInfo[]>()
  for (const [childId, parentId] of parentOf) {
    const child = byId.get(childId)
    if (!child) continue
    childrenOf.set(parentId, [...(childrenOf.get(parentId) ?? []), child])
  }
  for (const [parentId, children] of childrenOf) {
    children.sort((a, b) => compareSessions(a, b, order))
    childrenOf.set(parentId, children)
  }

  return { parentOf, childrenOf, unresolved, order }
}


export function orderedChildren(forest: SessionForest, parentId: string): SessionInfo[] {
  return forest.childrenOf.get(parentId) ?? []
}

// topLevelRoot walks the trusted parent edges to the row that owns the branch.
// The sidebar uses it to keep a collapsed-away child's root visible.
export function topLevelRoot(forest: SessionForest, id: string): string {
  const seen = new Set<string>()
  let cursor = id
  while (!seen.has(cursor)) {
    const parent = forest.parentOf.get(cursor)
    if (!parent) return cursor
    seen.add(cursor)
    cursor = parent
  }
  return cursor
}

export function ancestorsOf(forest: SessionForest, id: string): string[] {
  const chain: string[] = []
  const seen = new Set<string>()
  let cursor = forest.parentOf.get(id)
  while (cursor && !seen.has(cursor)) {
    seen.add(cursor)
    chain.push(cursor)
    cursor = forest.parentOf.get(cursor)
  }
  return chain
}
