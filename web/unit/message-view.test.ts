import { expect, test } from 'bun:test'
import { clampCompactKeep, foldReplies, groupTurns } from '../src/lib/messageView.ts'
import type { ChatNode } from '../src/api/types.ts'

const user = (id: string, text: string, origin?: string): ChatNode => ({ kind: 'user', id, text, content: [], origin })
const asst = (id: string, text = 'ok', streaming = false): ChatNode => ({ kind: 'assistant', id, text, streaming })
const tool = (id: string, running = false): ChatNode => ({ kind: 'tool', id, name: 'Bash', args: {}, running })

test('groupTurns splits at user nodes and keeps a leading group', () => {
  const turns = groupTurns([asst('a0'), user('u1', 'one'), asst('a1'), user('u2', 'two'), asst('a2')])
  expect(turns.map(t => t.id)).toEqual(['a0', 'u1', 'u2'])
  expect(turns[0].user).toBeUndefined()
  expect(turns[1].user?.id).toBe('u1')
  expect(turns[1].nodes.map(n => n.id)).toEqual(['u1', 'a1'])
  expect(turns[2].nodes.map(n => n.id)).toEqual(['u2', 'a2'])
})

test('foldReplies treats runtime-authored user messages as foldable replies', () => {
  const nodes = [
    user('u1', 'human'), asst('a1'),
    user('notice', '<task-notification>done</task-notification>', 'agent:task-1'), asst('a2'),
    user('u2', 'next human', 'extension:telegram-bot'), asst('a3'),
  ]
  const turns = groupTurns(nodes)
  expect(turns.map(t => t.id)).toEqual(['u1', 'u2'])
  expect(turns[0].nodes.map(n => n.id)).toEqual(['u1', 'a1', 'notice', 'a2'])

  const items = foldReplies(nodes, { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a2', 'u2', 'a3'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1', 'notice'])
})

test('foldReplies folds the opening directive of a machine-only subagent turn', () => {
  const nodes = [user('directive', 'subagent directive', 'agent'), asst('answer')]
  const items = foldReplies(nodes, { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['fold:directive', 'answer'])
  const folded = items[0]
  expect(folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['directive'])
})

test('foldReplies keeps every user bubble and the newest keep replies of each turn', () => {
  const nodes = [
    user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b'),
    user('u2', 'two'), asst('a2a'), tool('t2'), asst('a2b'),
  ]
  // One reply kept per turn: the fold row sits between the prompt and it.
  expect(foldReplies(nodes, { keep: 1 }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1b',
    'u2', 'fold:u2', 'a2b',
  ])
  const folded = foldReplies(nodes, { keep: 1 }).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a', 't1'])

  // keep 0 folds every reply, keep 2 leaves the short turns alone.
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'u2', 'fold:u2'])
  expect(foldReplies(nodes, { keep: 3 }).map(i => i.id)).toEqual(nodes.map(n => n.id))
  // A turn that is only a prompt has nothing to fold.
  expect(foldReplies([user('u1', 'one')], { keep: 0 }).map(i => i.id)).toEqual(['u1'])
})

test('foldReplies opens a fold in place without reordering the turn', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b')]
  expect(foldReplies(nodes, { keep: 1, expanded: new Set(['u1']) }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1a', 't1', 'a1b',
  ])
})

test('foldReplies keeps only the newest in-flight work visible', () => {
  // Trailing live work is what the user is waiting for.
  expect(foldReplies([user('u1', 'one'), asst('a1a'), tool('t1', true)], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1'])
  expect(foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b', 'ok', true)], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b'])
})

test('foldReplies keeps an earlier running sibling visible until lifecycle settlement', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('slow', true), tool('fast', false)]
  expect(foldReplies(nodes, { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'slow', 'fast'])
  const settled = nodes.map(n => n.kind === 'tool' ? { ...n, running: false } : n)
  expect(foldReplies(settled, { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'fast'])
})

const compact = (id: string, summary = 'sum'): ChatNode => ({ kind: 'compaction', id, summary })
const cancelled = (id: string): ChatNode => ({ kind: 'cancellation', id, reason: 'user_request', source: 'webui' })

test('foldReplies never folds a compaction row or lets it consume a keep slot', () => {
  // A checkpoint trailing the turn must not hide the newest reply.
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b'), compact('c1')], { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'c1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  // Even keep 0 leaves the compaction visible on its own row.
  expect(foldReplies([user('u1', 'one'), asst('a1a'), compact('c1')], { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'c1'])

  // A mid-turn compaction (overflow) stays visible while older replies fold.
  expect(foldReplies([user('u1', 'one'), asst('a1'), compact('c1'), asst('a2')], { keep: 1 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'c1', 'a2'])
})

test('foldReplies never folds a cancellation row or lets it consume a keep slot', () => {
  const items = foldReplies([user('u1', 'one'), asst('a1a'), asst('a1b'), cancelled('x1')], { keep: 1 })
  expect(items.map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b', 'x1'])
  const folded = items.find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a'])

  expect(foldReplies([user('u1', 'one'), asst('a1'), cancelled('x1')], { keep: 0 }).map(i => i.id))
    .toEqual(['u1', 'fold:u1', 'x1'])
})

test('foldReplies keeps a legacy aborted assistant visible for its cancellation row', () => {
  const aborted: ChatNode = { kind: 'assistant', id: 'a2', text: 'partial', stopReason: 'aborted' }
  expect(foldReplies([user('u1', 'one'), asst('a1'), aborted, cancelled('x1')], { keep: 0 }).map(i => i.id))
    .toEqual(['u1', 'fold:u1', 'a2', 'x1'])
})

test('clampCompactKeep bounds the configured N', () => {
  expect(clampCompactKeep(Number.NaN)).toBe(1)
  expect(clampCompactKeep(-3)).toBe(0)
  expect(clampCompactKeep(2.6)).toBe(3)
  expect(clampCompactKeep(99)).toBe(20)
})

