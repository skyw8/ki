import type { ChatNode, Entry, Message } from '../api/types'
import { compactionAnchors } from './compactionLifecycle'

export type TurnId = string

export function isHumanPrompt(origin?: string): boolean {
  return !origin || origin.startsWith('extension:')
}

export function isHumanEntry(entry: Entry): boolean {
  return entry.type === 'message' && entry.message?.role === 'user' && isHumanPrompt(entry.message.origin)
}

export type TurnGroup<T> = { id: TurnId; items: T[]; human?: T }

function partitionTurns<T extends { id: string }>(items: T[], human: (item: T) => boolean): TurnGroup<T>[] {
  const groups: TurnGroup<T>[] = []
  let leading: T[] = []
  for (const item of items) {
    if (human(item)) {
      groups.push({ id: item.id, human: item, items: [...leading, item] })
      leading = []
    } else if (groups.length) groups[groups.length - 1].items.push(item)
    else leading.push(item)
  }
  if (leading.length) groups.push({ id: leading[0].id, items: leading })
  return groups
}

export function entryTurns(entries: Entry[]): TurnGroup<Entry>[] {
  return partitionTurns(entries, isHumanEntry).map(turn => {
    if (turn.human) return turn
    const lifecycle = compactionAnchors(turn.items)
    const first = turn.items.find(e => e.type === 'message' || e.type === 'compaction' || e.type === 'run_aborted' || lifecycle.has(e.id))
    return first ? { ...turn, id: first.message?.role === 'toolResult' ? first.message.toolCallId || first.id : first.id } : turn
  })
}

export type ChatTurn = {
  id: TurnId
  user?: Extract<ChatNode, { kind: 'user' }>
  nodes: ChatNode[]
}

export function groupTurns(nodes: ChatNode[]): ChatTurn[] {
  return partitionTurns(nodes, n => n.kind === 'user' && isHumanPrompt(n.origin))
    .map(t => ({ id: t.human?.id ?? t.items[0].turnId ?? t.id, user: t.human as ChatTurn['user'], nodes: t.items }))
}

export function userMessageIdentity(message: Pick<Message, 'clientRequestId'>): string | undefined {
  return message.clientRequestId ? `request:${message.clientRequestId}` : undefined
}

export function isTransientUserId(id: string): boolean {
  return id.startsWith('opt-user-') || id.startsWith('live-user-')
}

export function reconcileUserNodes(nodes: ChatNode[]): ChatNode[] {
  const canonical = new Map<string, ChatNode>()
  const pending = new Map<string, ChatNode>()
  for (const n of nodes) {
    if (n.kind !== 'user') continue
    const key = userMessageIdentity(n)
    if (key) (isTransientUserId(n.id) ? pending : canonical).set(key, n)
  }
  const used = new Set<string>()
  const transient = new Set<string>()
  const out: ChatNode[] = []
  for (const node of nodes) {
    if (node.kind !== 'user') { out.push(node); continue }
    const key = userMessageIdentity(node)
    const n = isTransientUserId(node.id) && key ? canonical.get(key) ?? pending.get(key) ?? node : node
    if (used.has(n.id)) continue
    if (isTransientUserId(n.id) && key) {
      if (transient.has(key)) continue
      transient.add(key)
    }
    used.add(n.id)
    out.push(n)
  }
  return out
}
