import type { ChatNode, CompactTurn } from '../api/types'
import { nodeLive } from './model'
import { groupTurns, isHumanPrompt } from './transcriptIdentity'
export { groupTurns } from './transcriptIdentity'
export type { ChatTurn } from './transcriptIdentity'
import type { ChatTurn } from './transcriptIdentity'

export type MessageViewMode = 'detailed' | 'compact'

export type MessageView = { mode: MessageViewMode }

export const COMPACT_VISIBLE_TEXT_CHARS = 50

const MODE_KEY = 'ki-message-view'

export function loadMessageView(): MessageView {
  try {
    return { mode: localStorage.getItem(MODE_KEY) === 'detailed' ? 'detailed' : 'compact' }
  } catch {
    return { mode: 'compact' }
  }
}

export function saveMessageView(view: MessageView): void {
  try {
    localStorage.setItem(MODE_KEY, view.mode)
  } catch {
  }
}

export type ChatRenderItem =
  | { kind: 'node'; id: string; node: ChatNode }
  | { kind: 'fold'; id: string; turn: ChatTurn; nodes: ChatNode[]; expanded: boolean; count: number; preview?: string; firstHiddenId?: string; remote?: boolean }

export function detailedItems(nodes: ChatNode[]): ChatRenderItem[] {
  return nodes.map(n => ({ kind: 'node', id: n.id, node: n }))
}

export type FoldOptions = {
  expanded?: ReadonlySet<string>
  summaries?: CompactTurn[]
  loadedTurnIds?: string[]
}

function isLive(n: ChatNode): boolean {
  return nodeLive(n)
}

function isVisibleReply(n: ChatNode): boolean {
  return n.kind === 'assistant' && [...n.text].length >= COMPACT_VISIBLE_TEXT_CHARS
}

function isFoldable(n: ChatNode): boolean {
  return (n.kind === 'tool' || (n.kind === 'assistant' && n.stopReason !== 'aborted')) ||
    (n.kind === 'user' && !isHumanPrompt(n.origin))
}

function hiddenReplyIds(rest: ChatNode[], keep: string): Set<string> {
  const foldable = rest.filter(isFoldable)
  const live = foldable.findIndex(isLive)
  const hidden = foldable.filter((n, i) => (live < 0 || i < live) && !isVisibleReply(n) && n.id !== keep)
  return new Set(hidden.map(n => n.id))
}

export function foldReplies(nodes: ChatNode[], opts: FoldOptions): ChatRenderItem[] {
  const turns = groupTurns(nodes)
  let newest: ChatNode | undefined
  for (let i = nodes.length - 1; i >= 0; i--) {
    const n = nodes[i]
    if (n.kind !== 'assistant') continue
    if (n.text.length > 0 && nodes[i + 1]?.kind !== 'tool') newest = n
    break
  }
  const summaries = new Map(opts.summaries?.map(t => [t.id, t]))
  const loaded = new Set(opts.loadedTurnIds)
  const out: ChatRenderItem[] = []
  turns.forEach(turn => {
    const rest = turn.nodes.filter(n => n !== turn.user)
    const hiddenIds = hiddenReplyIds(rest, newest?.id ?? '')
    const hidden = hiddenIds.size
    const summary = !loaded.has(turn.id) ? summaries.get(turn.id) : undefined
    const observed = new Set(rest.map(n => n.id))
    const snapshotReplies = summary?.baselineNodes?.filter(isFoldable) ?? []
    const visible = new Set(summary?.visibleNodeIds)
    const snapshotCount = summary ? summary.hiddenCount + snapshotReplies.filter(n => visible.has(n.id)).length : 0
    const overlap = snapshotReplies.filter(n => observed.has(n.id)).length
    const missing = summary ? Math.max(0, snapshotCount - overlap) : 0
    const count = hidden + missing
    if (count === 0) {
      for (const n of turn.nodes) out.push({ kind: 'node', id: n.id, node: n })
      return
    }
    const expanded = opts.expanded?.has(turn.id) ?? false
    const fold: ChatRenderItem = { kind: 'fold', id: `fold:${turn.id}`, turn, nodes: rest.filter(n => hiddenIds.has(n.id)), expanded, count, preview: summary?.preview, firstHiddenId: summary?.firstHiddenId, remote: missing > 0 }
    let inserted = false
    for (const n of turn.nodes) {
      if (n !== turn.user && !inserted) { out.push(fold); inserted = true }
      if (n === turn.user || expanded || !hiddenIds.has(n.id)) out.push({ kind: 'node', id: n.id, node: n })
    }
    if (!inserted) out.push(fold)
  })
  return out
}
