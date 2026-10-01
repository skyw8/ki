import type { AgentSnapshot } from '../api/types'

export type AgentBranch = { agent: AgentSnapshot; children: AgentBranch[] }

/** Canonical task paths, not delivery order, define the logical-agent tree. */
export function agentTree(agents: AgentSnapshot[]): AgentBranch[] {
  const branches = new Map(agents.map(agent => [agent.task_name, { agent, children: [] } as AgentBranch]))
  const roots: AgentBranch[] = []
  for (const [path, branch] of branches) {
    let parent = path.slice(0, path.lastIndexOf('/'))
    // Restored snapshots can omit ancestors. Keep descendants visible under
    // the nearest known ancestor instead of dropping them or inventing agents.
    while (parent && !branches.has(parent)) parent = parent.slice(0, parent.lastIndexOf('/'))
    if (parent) branches.get(parent)!.children.push(branch)
    else roots.push(branch)
  }
  return roots
}
