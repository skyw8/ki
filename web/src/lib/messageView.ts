import type { ChatNode, CompactTurn } from '../api/types'
import { nodeLive } from './model'
import { groupTurns, isHumanPrompt } from './transcriptIdentity'
export { groupTurns } from './transcriptIdentity'
export type { ChatTurn } from './transcriptIdentity'
import type { ChatTurn } from './transcriptIdentity'

export type MessageViewMode = 'detailed' | 'compact'

export type MessageView = { mode: MessageViewMode; keep: number }

export const DEFAULT_COMPACT_KEEP = 1
export const MAX_COMPACT_KEEP = 20

const MODE_KEY = 'ki-message-view'
const KEEP_KEY = 'ki-message-view-keep'

export function clampCompactKeep(n: number): number {
  if (!Number.isFinite(n)) return DEFAULT_COMPACT_KEEP
  return Math.min(MAX_COMPACT_KEEP, Math.max(0, Math.round(n)))
}

export function loadMessageView(): MessageView {
  try {
    const mode = localStorage.getItem(MODE_KEY) === 'compact' ? 'compact' : 'detailed'
    const raw = localStorage.getItem(KEEP_KEY)
    return { mode, keep: raw == null ? DEFAULT_COMPACT_KEEP : clampCompactKeep(Number(raw)) }
  } catch {
    return { mode: 'detailed', keep: DEFAULT_COMPACT_KEEP }
  }
}

export function saveMessageView(view: MessageView): void {
  try {
    localStorage.setItem(MODE_KEY, view.mode)
    localStorage.setItem(KEEP_KEY, String(clampCompactKeep(view.keep)))
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
  keep: number
  expanded?: ReadonlySet<string>
  summaries?: CompactTurn[]
  loadedTurnIds?: string[]
}

function isLive(n: ChatNode): boolean {
  return nodeLive(n)
}

function isKeepReply(n: ChatNode): boolean {
  return n.kind === 'tool' ||
    (n.kind === 'assistant' && n.stopReason !== 'aborted')
}

function isFoldable(n: ChatNode): boolean {
  return isKeepReply(n) || (n.kind === 'user' && !isHumanPrompt(n.origin))
}

function hiddenReplyIds(rest: ChatNode[], keep: number): Set<string> {
  const foldable = rest.filter(isFoldable)
  const replies = foldable.filter(isKeepReply)
  const firstKept = keep > 0 ? replies[Math.max(0, replies.length - keep)] : undefined
  const cut = firstKept ? foldable.indexOf(firstKept) : foldable.length
  const live = foldable.findIndex(isLive)
  const hidden = live < 0 ? cut : Math.min(cut, live)
  return new Set(foldable.slice(0, hidden).map(n => n.id))
}

export function foldReplies(nodes: ChatNode[], opts: FoldOptions): ChatRenderItem[] {
  const turns = groupTurns(nodes)
  const keep = clampCompactKeep(opts.keep)
  const summaries = new Map(opts.summaries?.map(t => [t.id, t]))
  const loaded = new Set(opts.loadedTurnIds)
  const out: ChatRenderItem[] = []
  turns.forEach(turn => {
    const rest = turn.nodes.filter(n => n !== turn.user)
    const hiddenIds = hiddenReplyIds(rest, keep)
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
