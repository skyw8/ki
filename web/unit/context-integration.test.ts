import { expect, test } from 'bun:test'
import type { Entry, IndexEntry } from '../src/api/types'
import { buildContext, buildContextTrend } from '../src/features/context/model'
import { applyEvent, hydrateEntries, loadHistory } from '../src/lib/model'

test('contextEstimate survives index projection, fuller-body hydration and late slim merges', () => {
  const index: IndexEntry[] = [
    { type: 'message', id: 'agent', role: 'user', origin: 'agent:work', preview: 'preview', contextEstimate: { message: 420 } },
    { type: 'request_header', id: 'r', parentId: 'agent', contextEstimate: { system: 100, tools: 300 } },
  ]
  const view = loadHistory({ id: 'session', leafId: 'r', index, entries: [] })
  const context = buildContext({ entries: view.allEntries, requests: view.requests, requestId: 'r' })
  expect(context.categories.find(group => group.id === 'agent')?.tokens).toBe(420)
  expect(buildContextTrend({ entries: view.allEntries, requests: view.requests })[0].categories).toEqual({ agent: 420, system: 100, tools: 300 })
  const hydrated = hydrateEntries(view, [
    { type: 'message', id: 'agent', message: { role: 'user', origin: 'agent:work', content: [{ type: 'text', text: 'full' }] } },
    { type: 'request_header', id: 'r', parentId: 'agent', system: 'full System', tools: [] },
  ])
  expect(hydrated.allEntries.map(entry => entry.contextEstimate)).toEqual([{ message: 420 }, { system: 100, tools: 300 }])
  expect(buildContextTrend({ entries: hydrated.allEntries, requests: hydrated.requests })[0].categories).toEqual({ agent: 420, system: 100, tools: 300 })
  const captured = hydrateEntries(hydrated, [
    { type: 'message', id: 'agent', message: { role: 'user', origin: 'agent:work', content: [{ type: 'text', text: 'full' }] }, contextEstimate: { message: 420 } },
  ])
  const late = hydrateEntries(captured, [{ type: 'message', id: 'agent', bodyKind: 'slim', truncated: true, message: { role: 'user', content: [{ type: 'text', text: 'late preview' }] } }])
  expect(late.allEntries.find(entry => entry.id === 'agent')?.contextEstimate?.message).toBe(420)
  expect(late.allEntries.find(entry => entry.id === 'agent')?.message?.content?.[0].text).toBe('full')
})

test('metadata-only model requests retain identity, usage and hydrate their own prompt', () => {
  const index: IndexEntry[] = [
    { type: 'message', id: 'u', role: 'user', preview: 'question' },
    { type: 'request_header', id: 'r1', parentId: 'u' },
    { type: 'message', id: 'a1', parentId: 'r1', role: 'assistant', preview: 'first answer', usage: { input: 100, output: 10 } },
    { type: 'request_header', id: 'r2', parentId: 'a1' },
    { type: 'message', id: 'a2', parentId: 'r2', role: 'assistant', preview: 'second answer', usage: { input: 150, cacheRead: 50, output: 20 } },
  ]
  const view = loadHistory({ id: 'session', leafId: 'a2', index, entries: [] })
  expect(view.requests.map(request => [request.id, request.step])).toEqual([['r1', 1], ['r2', 2]])
  expect(view.requests.map(request => request.usage?.input)).toEqual([100, 150])
  expect(view.requests.every(request => request.prompt === undefined)).toBe(true)
  const points = buildContextTrend({ entries: view.allEntries, requests: view.requests, leafId: view.leafId })
  expect(points.map(point => [point.tokens, point.basis])).toEqual([[100, 'usage'], [200, 'usage']])
  const context = buildContext({ entries: view.allEntries, requests: view.requests, requestId: 'r1' })
  expect(context.categories.find(group => group.id === 'system')?.items[0]).toMatchObject({ entryId: 'r1', truncated: true })
  expect(context.categories.find(group => group.id === 'assistant')?.items).toHaveLength(0)

  const header: Entry = { type: 'request_header', id: 'r1', parentId: 'u', system: 'recorded first System', tools: [{ name: 'read' }] }
  const hydrated = hydrateEntries(view, [header])
  expect(hydrated.requests[0].prompt?.system).toBe('recorded first System')
  // An unknown later header might have changed the prompt. Hydrating an older
  // request must not retroactively attribute it to a metadata-only successor.
  expect(hydrated.requests[1].prompt).toBeUndefined()
  expect(hydrated.requests.map(request => request.id)).toEqual(['r1', 'r2'])
})

test('unchanged headers without a loaded predecessor stay unknown until explicitly hydrated', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u', message: { role: 'user', content: [{ type: 'text', text: 'question' }] } },
    { type: 'request_header', id: 'r', parentId: 'u', promptUnchanged: true },
    { type: 'message', id: 'a', parentId: 'r', message: { role: 'assistant', content: [{ type: 'text', text: 'answer' }], usage: { input: 42, output: 4 } } },
  ]
  const view = loadHistory({ id: 'session', leafId: 'a', entries })
  expect(view.requests[0].prompt).toBeUndefined()
  const next = hydrateEntries(view, [{ type: 'request_header', id: 'r', parentId: 'u', system: 'original System', tools: [] }])
  expect(next.requests[0].prompt?.system).toBe('original System')
})

test('remote checkpoint public marker survives index-only and hydrated projections', () => {
  const view = loadHistory({
    id: 'session', leafId: 'remote', entries: [],
    index: [{ type: 'compaction', id: 'remote', remoteContext: true, preview: 'Provider remote compaction' }],
  })
  expect(view.allEntries[0].remoteContext).toBe(true)
  const next = hydrateEntries(view, [{ type: 'compaction', id: 'remote', remoteContext: true }])
  expect(next.allEntries[0].remoteContext).toBe(true)
  const context = buildContext({ entries: next.allEntries, requests: next.requests, leafId: next.leafId })
  expect(context.categories.find(group => group.id === 'remote')?.items).toHaveLength(1)
  expect(context.categories.find(group => group.id === 'compaction')?.items).toHaveLength(0)
})

test('settling a request replaces owned rows without mutating the preceding view', () => {
  const view = loadHistory({
    id: 'session', leafId: 'r', running: true,
    entries: [
      { type: 'message', id: 'u', message: { role: 'user', content: [{ type: 'text', text: 'question' }] } },
      { type: 'request_header', id: 'r', parentId: 'u', system: 'System', tools: [] },
    ],
  })
  const beforeRequest = view.requests[0]
  const beforeRecords = view.records.slice()
  const settled = applyEvent(view, {
    type: 'message_end', entryId: 'a', parentId: 'r',
    message: { role: 'assistant', content: [{ type: 'text', text: 'answer' }], usage: { input: 42, output: 4 } },
  })
  expect(view.requests[0]).toBe(beforeRequest)
  expect(view.requests[0].status).toBe('running')
  expect(view.requests[0].usage).toBeUndefined()
  expect(view.records).toEqual(beforeRecords)
  expect(settled.requests[0].status).toBe('complete')
  expect(settled.requests[0].usage?.input).toBe(42)
})
