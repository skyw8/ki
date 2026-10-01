import type { Entry, ViewState } from '../../api/types'

function rowId(entry: Entry): string | undefined {
  if (entry.type === 'compaction') return entry.id
  if (entry.type !== 'message') return undefined
  const role = entry.message?.role
  if (role === 'toolResult' || role === 'tool') return entry.message?.toolCallId
  if (role === 'user' || role === 'assistant') return entry.id
  return undefined
}

/** Resolve persisted entry identity to a real chat row on the selected branch. */
export function conversationTarget(view: Pick<ViewState, 'allEntries' | 'nodes' | 'leafId'>, id: string): string | null {
  if (view.nodes.some(node => node.id === id)) return id
  const entries = new Map(view.allEntries.map(entry => [entry.id, entry]))
  const entry = entries.get(id)
  // A navigator can name a sparse or not-yet-loaded user/tool row. Paging
  // verifies its reachability later; unknown identity is not a tail fallback.
  if (!entry) return id
  const direct = rowId(entry)
  if (direct) return direct
  if (entry.type !== 'model_change' && entry.type !== 'request_header') return null

  // Header/model events intentionally have no transcript row. Locate the
  // boundary beside them, never a raw metadata ID that cannot be rendered.
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

  // Initial model selection can precede the first message. Follow only the
  // active leaf's ancestry; a sibling's convenient row is not this event.
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

/** A hidden row in an already projected compact turn needs that turn's body. */
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
