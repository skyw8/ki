import type { Entry, ViewState } from '../../api/types'

function rowId(entry: Entry): string | undefined {
  if (entry.type === 'compaction') return entry.id
  if (entry.type !== 'message') return undefined
  const role = entry.message?.role
  if (role === 'toolResult' || role === 'tool') return entry.message?.toolCallId
  if (role === 'user' || role === 'assistant') return entry.id
  return undefined
}

export function conversationTarget(view: Pick<ViewState, 'allEntries' | 'nodes' | 'leafId'>, id: string): string | null {
  if (view.nodes.some(node => node.id === id)) return id
  const entries = new Map(view.allEntries.map(entry => [entry.id, entry]))
  const entry = entries.get(id)
  if (!entry) return id
  const direct = rowId(entry)
  if (direct) return direct
  if (entry.type !== 'model_change' && entry.type !== 'request_header') return null

  const seen = new Set<string>([id])
  let parentId = entry.parentId
  while (parentId && !seen.has(parentId)) {
    seen.add(parentId)
    const parent = entries.get(parentId)
    if (!parent) return null
    const target = rowId(parent)
    if (target) return target
    parentId = parent.parentId
  }
  if (parentId) return null

  const branch: Entry[] = []
  seen.clear()
  let leaf = view.leafId
  while (leaf && !seen.has(leaf)) {
    seen.add(leaf)
    const current = entries.get(leaf)
    if (!current) return null
    branch.push(current)
    if (current.id === id) {
      return branch.reverse().slice(1).map(rowId).find((target): target is string => !!target) ?? null
    }
    leaf = current.parentId
  }
  return null
}

export function conversationTargetTurn(view: Pick<ViewState, 'allEntries' | 'compactTurns' | 'leafId'>, id: string): string | null {
  const turns = new Set(view.compactTurns?.map(turn => turn.id))
  if (!turns.size) return null
  const entries = new Map(view.allEntries.map(entry => [entry.id, entry]))
  let entry = entries.get(id) ?? view.allEntries.find(item => item.message?.toolCallId === id
    || item.message?.content?.some(block => block.type === 'toolCall' && block.id === id))
  if (entry && view.leafId) {
    const active = new Set<string>()
    let leaf = view.leafId
    while (leaf && !active.has(leaf)) {
      active.add(leaf)
      leaf = entries.get(leaf)?.parentId ?? ''
    }
    if (!active.has(entry.id)) return null
  }
  const seen = new Set<string>()
  while (entry && !seen.has(entry.id)) {
    if (turns.has(entry.id)) return entry.id
    seen.add(entry.id)
    entry = entry.parentId ? entries.get(entry.parentId) : undefined
  }
  return null
}
