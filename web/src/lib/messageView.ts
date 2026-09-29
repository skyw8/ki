import type { ChatNode, CompactTurn } from '../api/types'
import { isHumanPrompt, nodeLive } from './model'

/**
 * Message view mode controls how much of the transcript the chat renders.
 *
 * detailed is the full transcript. compact keeps every user prompt — it is what
 * a turn is, and the anchor the request navigator scrolls to — and the newest
 * `keep` reply nodes of each turn; the reply nodes in between (the tool chatter
 * of a long turn) fold into one row per turn. This is a per-browser display
 * preference, stored next to the theme and language rather than in the server's
 * toggles.
 */
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
    // Private mode / blocked storage: fall back to the default view.
    return { mode: 'detailed', keep: DEFAULT_COMPACT_KEEP }
  }
}

export function saveMessageView(view: MessageView): void {
  try {
    localStorage.setItem(MODE_KEY, view.mode)
    localStorage.setItem(KEEP_KEY, String(clampCompactKeep(view.keep)))
  } catch {
    // Private mode / quota should not block the setting.
  }
}

/**
 * ChatTurn is one human turn: the human user node that opens it plus every
 * node up to the next human input. Runtime-authored user messages (for example
 * subagent directives and completion notifications) stay inside that turn so
 * compact mode can fold them like assistant and tool replies.
 *
 * Nodes before the first human user (a window or child session that starts
 * with a machine-authored message) form a leading turn with no user.
 */
export type ChatTurn = {
  /** Stable key and anchor id: the turn's first node id. */
  id: string
  user?: Extract<ChatNode, { kind: 'user' }>
  nodes: ChatNode[]
}

/** groupTurns splits the branch at human inputs, matching the request
 * navigator. Machine-authored user-role messages are foldable transcript
 * replies, not permanent turn anchors. */
export function groupTurns(nodes: ChatNode[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  for (const n of nodes) {
    const opensTurn = n.kind === 'user' && isHumanPrompt(n.origin)
    if (opensTurn || turns.length === 0) {
      turns.push({ id: n.id, user: opensTurn ? n : undefined, nodes: [n] })
      continue
    }
    turns[turns.length - 1].nodes.push(n)
  }
  return turns
}

/**
 * ChatRenderItem is one row of the (virtualized) chat list.
 *
 * compact mode folds some nodes away, so the render list is no longer the node
 * list. A fold row's synthetic id is prefixed so it can never collide with the
 * node id it was derived from; the nodes it hides and the ones that stay are
 * emitted as ordinary node items, so virtualization and the turn dividers keep
 * working per node.
 */
export type ChatRenderItem =
  | { kind: 'node'; id: string; node: ChatNode }
  | { kind: 'fold'; id: string; turn: ChatTurn; nodes: ChatNode[]; expanded: boolean; count: number; preview?: string; firstHiddenId?: string; remote?: boolean }

export function detailedItems(nodes: ChatNode[]): ChatRenderItem[] {
  return nodes.map(n => ({ kind: 'node', id: n.id, node: n }))
}

export type FoldOptions = {
  /** Newest reply nodes kept visible in each turn. */
  keep: number
  /** Turn ids the user expanded by hand (they stay open). */
  expanded?: ReadonlySet<string>
  summaries?: CompactTurn[]
  loadedTurnIds?: string[]
}

/** isLive reports nodes compact mode must never hide: work in progress. */
function isLive(n: ChatNode): boolean {
  return nodeLive(n)
}

/**
 * hiddenReplyIds returns the ids of reply nodes compact mode folds: every
 * reply but the newest `keep`, and never a node that is still live — folding
 * the streaming text or the running tool would hide exactly what the user is
 * waiting for.
 *
 * Compaction rows are metadata, not replies: they are always shown on their own
 * row and never counted toward `keep`. Counting them was why a compaction that
 * trails a turn folded away that turn's final answer.
 *
 * Liveness comes from lifecycle reconciliation, not node position: parallel
 * tools finish out of order and an earlier sibling can still be running.
 * Stale state is retired by immutable results or the next model request.
 *
 * The running turn folds like any other (measured: one in-flight turn held 61
 * reply nodes and rendered all of them, where folding renders two). Keeping a
 * whole live turn unfolded was the earlier behaviour and it is what made
 * opening a running session expensive: the newest turn is usually the longest
 * one, and its tool chatter is exactly what compact mode hides.
 */
function hiddenReplyIds(rest: ChatNode[], keep: number): Set<string> {
  const replies = rest.filter(n => n.kind !== 'compaction')
  const cut = Math.max(0, replies.length - keep)
  const live = replies.findIndex(isLive)
  const hidden = live < 0 ? cut : Math.min(cut, live)
  return new Set(replies.slice(0, hidden).map(n => n.id))
}

/**
 * foldReplies builds the compact render list.
 *
 * Per turn: the user bubble always stays, then one fold row for the reply nodes
 * before the newest `keep`, then those newest nodes. A folded turn opens in
 * place — the row stays where it is and the hidden nodes appear right after it,
 * so expanding never reorders the transcript. Compaction rows never fold and
 * never count toward `keep`.
 */
export function foldReplies(nodes: ChatNode[], opts: FoldOptions): ChatRenderItem[] {
  const turns = groupTurns(nodes)
  const keep = clampCompactKeep(opts.keep)
  const summaries = new Map(opts.summaries?.map(t => [t.id, t]))
  const loaded = new Set(opts.loadedTurnIds)
  const out: ChatRenderItem[] = []
  turns.forEach(turn => {
    if (turn.user) out.push({ kind: 'node', id: turn.user.id, node: turn.user })
    const rest = turn.user ? turn.nodes.slice(1) : turn.nodes
    const hiddenIds = hiddenReplyIds(rest, keep)
    const hidden = hiddenIds.size
    const summary = !loaded.has(turn.id) ? summaries.get(turn.id) : undefined
    const observed = new Set(rest.map(n => n.id))
    const snapshotReplies = summary?.baselineNodes?.filter(n => n.kind !== 'user') ?? []
    const snapshotCount = summary ? summary.hiddenCount + summary.visibleNodeIds.filter(id => id !== summary.id).length : 0
    const overlap = snapshotReplies.filter(n => observed.has(n.id)).length
    const missing = summary ? Math.max(0, snapshotCount - overlap) : 0
    const count = hidden + missing
    if (count === 0) {
      for (const n of rest) out.push({ kind: 'node', id: n.id, node: n })
      return
    }
    const expanded = opts.expanded?.has(turn.id) ?? false
    out.push({ kind: 'fold', id: `fold:${turn.id}`, turn, nodes: rest.filter(n => hiddenIds.has(n.id)), expanded, count, preview: summary?.preview, firstHiddenId: summary?.firstHiddenId, remote: missing > 0 })
    for (const n of rest) {
      if (expanded || !hiddenIds.has(n.id)) out.push({ kind: 'node', id: n.id, node: n })
    }
  })
  return out
}
