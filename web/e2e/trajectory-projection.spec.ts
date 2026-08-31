import { expect, test } from '@playwright/test'
import { applyEvent, emptyView, loadHistory } from '../src/model.ts'
import type { Entry, LoopEvent } from '../src/types.ts'

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
  expect(view.nodes.filter(item => item.kind === 'system')).toHaveLength(1)
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
