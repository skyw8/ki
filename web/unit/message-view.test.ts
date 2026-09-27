import { expect, test } from 'bun:test'
import { clampCompactKeep, foldReplies, groupTurns } from '../src/lib/messageView.ts'
import type { ChatNode } from '../src/api/types.ts'

const user = (id: string, text: string): ChatNode => ({ kind: 'user', id, text, content: [] })
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

test('foldReplies never hides work in progress', () => {
  // A running tool and streaming text stay visible even at keep=0.
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1', true), asst('a1b')]
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1', 'a1b'])
})

test('foldReplies folds the running turn too, keeping live work visible', () => {
  // An in-flight turn is the longest one in practice: leaving it unfolded meant
  // rendering all of its reply nodes (measured on a live run: 61 nodes, where
  // folding renders 2). It folds like any other turn — only nodes that are still
  // streaming or running stay on screen.
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b'), user('u2', 'two'), asst('a2a'), asst('a2b')]
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'u2', 'fold:u2'])
  const live = [user('u1', 'one'), asst('a1a'), asst('a1b', 'ok', true)]
  expect(foldReplies(live, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'a1b'])
  const runningTool = [user('u1', 'one'), asst('a1a'), tool('t1', true), asst('a1b')]
  expect(foldReplies(runningTool, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1', 'a1b'])
})

test('clampCompactKeep bounds the configured N', () => {
  expect(clampCompactKeep(Number.NaN)).toBe(1)
  expect(clampCompactKeep(-3)).toBe(0)
  expect(clampCompactKeep(2.6)).toBe(3)
  expect(clampCompactKeep(99)).toBe(20)
})

