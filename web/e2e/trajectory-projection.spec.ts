import { expect, test } from '@playwright/test'
import { applyEvent, emptyView, loadHistory, reconcileUserNodes } from '../src/lib/model.ts'
import type { Entry, LoopEvent } from '../src/api/types.ts'

function request(id: string, parentId: string, system: string, tools = []): Entry {
  return { type: 'request_header', id, parentId, timestamp: '2026-08-31T00:00:00.000Z', system, tools }
}

function assistant(id: string, parentId: string, text: string): Entry {
  return {
    type: 'message',
    id,
    parentId,
    timestamp: '2026-08-31T00:00:01.000Z',
    message: { role: 'assistant', content: [{ type: 'text', text }] },
  }
}

test('reconcileUserNodes drops optimistic copies after jsonl ids exist', () => {
  const nodes = reconcileUserNodes([
    { kind: 'user', id: 'opt-user-1', text: 'queued-promote', content: [] },
    { kind: 'assistant', id: 'a', text: 'ok' },
    { kind: 'user', id: 'jsonl', text: 'queued-promote', content: [] },
  ])
  expect(nodes.filter(n => n.kind === 'user').map(n => n.id)).toEqual(['jsonl'])
  expect(nodes.map(n => n.kind)).toEqual(['user', 'assistant'])
  expect(reconcileUserNodes([
    { kind: 'user', id: 'opt-user-1', text: 'queued-promote', content: [] },
    { kind: 'user', id: 'live-user-2', text: 'queued-promote', content: [] },
  ]).map(n => n.id)).toEqual(['live-user-2'])
})

test('index keeps abandoned sibling users on the session view', () => {
  const view = loadHistory({
    id: 'session', cwd: '/tmp', provider: 'test', model: 'test', title: 'test',
    leafId: 'u2',
    entries: [
      { type: 'message', id: 'u2', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'edited' }] } },
      { type: 'message', id: 'a2', parentId: 'u2', message: { role: 'assistant', content: [{ type: 'text', text: 'ok' }] } },
    ],
    index: [
      { type: 'message', id: 'u1', parentId: '', role: 'user', preview: 'original' },
      { type: 'message', id: 'a1', parentId: 'u1', role: 'assistant', preview: 'ok' },
      { type: 'message', id: 'u2', parentId: '', role: 'user', preview: 'edited' },
      { type: 'message', id: 'a2', parentId: 'u2', role: 'assistant', preview: 'ok' },
    ],
  })
  expect(view.allEntries.filter(e => e.message?.role === 'user').map(e => e.id)).toEqual(['u1', 'u2'])
  expect(view.nodes.filter(n => n.kind === 'user').map(n => n.text)).toEqual(['edited'])
})

test('promptUnchanged request headers reuse the previous prompt body', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'hello' }] } },
    { type: 'request_header', id: 'h1', parentId: 'u1', timestamp: '2026-08-31T00:00:00.000Z', system: 'same prompt', tools: [] },
    assistant('a1', 'h1', 'one'),
    { type: 'request_header', id: 'h2', parentId: 'a1', timestamp: '2026-08-31T00:00:02.000Z', promptUnchanged: true },
    assistant('a2', 'h2', 'two'),
  ]
  const view = loadHistory({
    id: 'session', cwd: '/tmp', provider: 'test', model: 'test', title: 'test', leafId: 'a2', entries,
  })
  expect(view.requests.map(item => item.prompt?.system)).toEqual(['same prompt', 'same prompt'])
  expect(view.records.filter(item => item.kind === 'system')).toHaveLength(1)
})

test('repeated request headers inherit one visible system prompt', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'hello' }] } },
    request('h1', 'u1', 'same prompt'),
    assistant('a1', 'h1', 'one'),
    request('h2', 'a1', 'same prompt'),
    assistant('a2', 'h2', 'two'),
    request('h3', 'a2', 'same prompt'),
    assistant('a3', 'h3', 'three'),
  ]

  const view = loadHistory({
    id: 'session', cwd: '/tmp', provider: 'test', model: 'test', title: 'test', leafId: 'a3', entries,
  })

  expect(view.requests.map(item => item.prompt?.system)).toEqual(['same prompt', 'same prompt', 'same prompt'])
  expect(view.requests.map(item => item.promptChange?.kind)).toEqual(['initial', undefined, undefined])
  expect(view.records.filter(item => item.kind === 'system')).toHaveLength(1)
  expect(view.records.filter(item => item.requestOnly)).toHaveLength(3)
  expect(view.nodes.map(item => item.kind)).toEqual(['user', 'assistant', 'assistant', 'assistant'])
})

test('prompt changes create a new visible system record and diff source', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'hello' }] } },
    request('h1', 'u1', 'first'),
    assistant('a1', 'h1', 'one'),
    request('h2', 'a1', 'second'),
    assistant('a2', 'h2', 'two'),
  ]
  const view = loadHistory({
    id: 'session', cwd: '/tmp', provider: 'test', model: 'test', title: 'test', leafId: 'a2', entries,
  })

  expect(view.requests.map(item => item.promptChange?.kind)).toEqual(['initial', 'system'])
  expect(view.records.filter(item => item.kind === 'system').map(item => item.system)).toEqual(['first', 'second'])
  expect(view.records.filter(item => item.kind === 'system')[1]).toMatchObject({ previousSystem: 'first' })
  expect(view.nodes.map(item => item.kind)).toEqual(['user', 'assistant', 'assistant'])
})

test('live request headers retain request metadata while rendering one prompt', () => {
  const header = (id: string): LoopEvent => ({
    type: 'request_header',
    entryId: id,
    timestamp: 1_000,
    system: 'live prompt',
    tools: [],
    provider: 'test',
    model: 'model',
  })
  let view = applyEvent(emptyView(), header('h1'))
  view = applyEvent(view, header('h2'))

  expect(view.requests).toHaveLength(2)
  expect(view.records.filter(item => item.kind === 'system')).toHaveLength(1)
  expect(view.requests[1]?.prompt?.system).toBe('live prompt')
})

test('a manual compaction streams a running chat row that settles in place', () => {
  // /compact is one synchronous request, so the chat row is driven by the
  // session notification events rather than a prompt stream.
  let view = applyEvent(emptyView(), { type: 'compaction_start', reason: 'manual' })
  expect(view.nodes).toHaveLength(1)
  expect(view.nodes[0]).toMatchObject({ kind: 'compaction', running: true })

  view = applyEvent(view, { type: 'compaction_end', reason: 'manual', ok: true })
  expect(view.nodes).toHaveLength(1)
  expect(view.nodes[0]).toMatchObject({ kind: 'compaction', running: false, failed: false, empty: false })
  expect(view.records.filter(r => r.kind === 'compact')).toHaveLength(1)

  // "Nothing to compact" is not a failure: no model call happened.
  let skipped = applyEvent(emptyView(), { type: 'compaction_start', reason: 'manual' })
  skipped = applyEvent(skipped, { type: 'compaction_end', reason: 'empty', ok: true })
  expect(skipped.nodes[0]).toMatchObject({ kind: 'compaction', running: false, empty: true, failed: false })

  let failed = applyEvent(emptyView(), { type: 'compaction_start', reason: 'manual' })
  failed = applyEvent(failed, { type: 'compaction_end', reason: 'manual', ok: false })
  expect(failed.nodes[0]).toMatchObject({ kind: 'compaction', running: false, failed: true })

  // The reloaded history replaces the live row with the persisted summary.
  const settled = loadHistory({
    id: 'session', cwd: '/tmp', provider: 'test', model: 'test', title: 'test', leafId: 'c1',
    entries: [{ type: 'compaction', id: 'c1', summary: 'SUM', tokensBefore: 156752 }],
  })
  expect(settled.nodes).toEqual([{ kind: 'compaction', id: 'c1', summary: 'SUM', tokensBefore: 156752 }])
})
