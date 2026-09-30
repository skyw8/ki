import { expect, test } from 'bun:test'
import { ApiError, type Client } from '../src/api/client'
import type { LoopEvent, SessionDetail } from '../src/api/types'
import { loadHistory } from '../src/lib/model'
import { SessionSyncController } from '../src/lib/session-sync'
import { TranscriptStore } from '../src/lib/transcript-store'
import type { FrameClock } from '../src/lib/stream-batch'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
async function settle() { for (let i = 0; i < 30; i++) await Promise.resolve() }
const initial: SessionDetail = { id: 's', running: true, leafId: 'u', oldestId: 'u', hasMore: false,
  entries: [{ type: 'message', id: 'u', message: { role: 'user', content: [{ type: 'text', text: 'input' }] } }] }
const final: SessionDetail = { ...initial, running: false, leafId: 'a',
  entries: [...initial.entries!, { type: 'message', id: 'a', parentId: 'u', message: { role: 'assistant', content: [{ type: 'text', text: 'answer' }] } }] }
const completed: LoopEvent = { type: 'message_end', runId: 'run', seq: 7, entryId: 'a', parentId: 'u', message: final.entries![1].message }
const noFrames: FrameClock = { now: () => 0, frame: () => 1, cancelFrame() {}, delay: () => 2, cancelDelay() {} }
function untilAbort(signal: AbortSignal) {
  return new Promise<void>(done => {
    if (signal.aborted) done()
    else signal.addEventListener('abort', () => done(), { once: true })
  })
}

test('EOF reconciles the canonical transcript and commits before acknowledging its resume cursor', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const connections: Array<{ cursor?: string; through?: string }> = []
  let gets = 0
  const api = {
    get: async () => { gets++; return { ...final, running: true } },
    async *events(_id: string, signal: AbortSignal, cursor?: string, through?: string) {
      connections.push({ cursor, through })
      if (connections.length === 1) yield completed
      else await untilAbort(signal)
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), wait: async () => {}, clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    expect(gets).toBe(1)
    expect(connections).toEqual([{ cursor: undefined, through: undefined }, { cursor: 'run:7', through: undefined }])
    expect(store.current.nodes.filter(node => node.kind === 'assistant').map(node => node.id)).toEqual(['a'])
  } finally { sync.dispose() }
})

test('failed reconciliation retains partial work, retries without any prompt API, then adopts idle tail', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const retry = deferred<void>()
  let gets = 0
  const api = {
    get: async () => { if (++gets === 1) throw new TypeError('offline'); return final },
    async *events() {
      yield { type: 'message_update', runId: 'run', seq: 1, assistantMessageEvent: {
        type: 'text_delta', partial: { role: 'assistant', content: [{ type: 'text', text: 'partial answer' }] },
      } } satisfies LoopEvent
      throw new TypeError('link dropped')
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), wait: () => retry.promise, clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    expect(sync.getSnapshot().phase).toBe('retrying')
    expect(store.current.busy).toBe(true)
    expect(store.current.nodes.some(node => node.kind === 'assistant' && node.text === 'partial answer')).toBe(true)
    retry.resolve()
    await settle()
    expect(gets).toBe(2)
    expect(sync.getSnapshot().phase).toBe('idle')
    expect(store.current.busy).toBe(false)
    expect(store.current.nodes.filter(node => node.kind === 'assistant').map(node => node.text)).toEqual(['answer'])
  } finally { sync.dispose() }
})

test('page suspension suppresses push running hints until explicit resume and keeps the applied cursor', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const cursors: Array<string | undefined> = []
  let gets = 0
  const api = {
    get: async () => { gets++; return { ...final, running: true } },
    async *events(_id: string, signal: AbortSignal, cursor?: string) {
      cursors.push(cursor)
      if (cursors.length === 1) yield completed
      await untilAbort(signal)
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    sync.suspend()
    await sync.listen('s')
    await sync.recover(true)
    expect(cursors).toEqual([undefined])
    expect(gets).toBe(0)
    await sync.resume()
    await settle()
    expect(cursors).toEqual([undefined, 'run:7'])
    expect(gets).toBe(1)
  } finally { sync.dispose() }
})

test('an unapplied buffered event never advances the resume cursor when its reader is replaced', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const cursors: Array<string | undefined> = []
  const api = {
    get: async () => initial,
    async *events(_id: string, signal: AbortSignal, cursor?: string) {
      cursors.push(cursor)
      yield { type: 'message_start', runId: 'run', seq: 1, message: { role: 'assistant', content: [] } } satisfies LoopEvent
      await untilAbort(signal)
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    sync.suspend()
    await sync.resume()
    await settle()
    expect(cursors).toEqual([undefined, undefined])
    expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
  } finally { sync.dispose() }
})

test('a late response from an earlier opening of the same session cannot replace the new branch', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const delayed = deferred<SessionDetail>()
  const api = { get: () => delayed.promise } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  try {
    sync.select('s')
    const pending = sync.recover()
    store.reset('s', loadHistory({ ...initial, leafId: 'new', entries: [{ type: 'message', id: 'new', message: { role: 'user', content: [{ type: 'text', text: 'other branch' }] } }] }))
    sync.select('s')
    delayed.resolve(final)
    await pending
    await settle()
    expect(store.current.leafId).toBe('new')
    expect(store.current.nodes.map(node => node.id)).toEqual(['new'])
  } finally { sync.dispose() }
})

test('forced lifecycle recovery supersedes a stuck snapshot and rejects its later completion', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const delayed = deferred<SessionDetail>()
  let gets = 0
  const api = { get: () => ++gets === 1 ? delayed.promise : Promise.resolve(final) } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  try {
    sync.select('s')
    const old = sync.recover()
    await sync.recover(true)
    delayed.resolve({ ...initial, running: true })
    await old
    await settle()
    expect(gets).toBe(2)
    expect(store.current.leafId).toBe('a')
    expect(store.current.busy).toBe(false)
  } finally { sync.dispose() }
})

test('HTTP errors expose retry state without clearing history and recovery can be retried', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const errors: unknown[] = []
  let gets = 0
  const api = { get: async () => { if (++gets === 1) throw new ApiError(503, 'temporarily unavailable'); return final } } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), onError: error => errors.push(error) })
  try {
    sync.select('s')
    await sync.recover()
    expect(sync.getSnapshot().phase).toBe('error')
    expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
    expect(errors).toHaveLength(1)
    await sync.recover(true)
    expect(store.current.leafId).toBe('a')
    expect(sync.getSnapshot().phase).toBe('idle')
  } finally { sync.dispose() }
})

test('repeated recovery hints coalesce while one snapshot is already in flight', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const delayed = deferred<SessionDetail>()
  let gets = 0
  const api = { get: () => { gets++; return delayed.promise } } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  try {
    sync.select('s')
    const first = sync.recover()
    expect(sync.recover()).toBe(first)
    expect(gets).toBe(1)
    delayed.resolve(final)
    await first
    expect(store.current.busy).toBe(false)
  } finally { sync.dispose() }
})

test('a sideband racing an idle snapshot cannot leave a busy replica without a reader', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const delayed = deferred<SessionDetail>()
  let gets = 0
  const api = { get: () => ++gets === 1 ? delayed.promise : Promise.resolve(final) } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  try {
    sync.select('s')
    const pending = sync.recover()
    store.applyEvents([{ type: 'run_aborted', runId: 'run', reason: 'user_request' }])
    delayed.resolve(final)
    await pending
    await settle()
    expect(gets).toBe(2)
    expect(store.current.busy).toBe(false)
    expect(store.current.stopping).toBe(false)
    expect(sync.getSnapshot().phase).toBe('idle')
  } finally { sync.dispose() }
})

test('late selection cannot resume a suspended or disposed controller', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  let gets = 0
  let connects = 0
  const api = { get: async () => { gets++; return final }, async *events() { connects++ } } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  sync.select('s')
  sync.suspend()
  sync.select('s')
  await sync.listen('s')
  expect(connects).toBe(0)
  expect(sync.getSnapshot().phase).toBe('paused')
  sync.dispose()
  sync.select('s')
  await sync.resume()
  expect(gets).toBe(0)
  sync.activate()
  await sync.resume()
  expect(gets).toBe(1)
  expect(sync.getSnapshot().phase).toBe('idle')
  sync.dispose()
})

test('reopening a session revokes the cursor whose transient state was discarded', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const cursors: Array<string | undefined> = []
  const api = {
    async *events(_id: string, signal: AbortSignal, cursor?: string) {
      cursors.push(cursor)
      yield completed
      await untilAbort(signal)
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    store.reset('s', loadHistory(initial))
    sync.select('s')
    await sync.listen('s')
    await settle()
    expect(cursors).toEqual([undefined, undefined])
    expect(store.current.nodes.some(node => node.id === 'a')).toBe(true)
  } finally { sync.dispose() }
})

test('a new running hint supersedes an old idle snapshot without canceling recovery coverage', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const delayed = deferred<SessionDetail>()
  let gets = 0
  let connects = 0
  const api = {
    get: () => ++gets === 1 ? delayed.promise : Promise.resolve({ ...final, running: true }),
    async *events(_id: string, signal: AbortSignal) { connects++; await untilAbort(signal) },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}) })
  try {
    sync.select('s')
    const pending = sync.recover()
    void sync.listen('s')
    delayed.resolve(final)
    await pending
    await settle()
    expect(gets).toBe(2)
    expect(connects).toBe(1)
    expect(store.current.busy).toBe(true)
    expect(store.current.nodes.some(node => node.id === 'a')).toBe(true)
  } finally { sync.dispose() }
})

test('a failed tail read does not prevent a known live reader from resuming its applied ACK', async () => {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const cursors: Array<string | undefined> = []
  let gets = 0
  const api = {
    get: async () => { gets++; throw new TypeError('history temporarily unavailable') },
    async *events(_id: string, signal: AbortSignal, cursor?: string) {
      cursors.push(cursor)
      if (cursors.length === 1) yield completed
      else await untilAbort(signal)
    },
  } as unknown as Client
  const sync = new SessionSyncController(api, store, { transcriptOptions: () => ({}), wait: async () => {}, clock: noFrames })
  try {
    sync.select('s')
    await sync.listen('s')
    await settle()
    expect(cursors).toEqual([undefined, 'run:7'])
    expect(gets).toBe(1)
    expect(store.current.busy).toBe(true)
    expect(store.current.nodes.some(node => node.id === 'a')).toBe(true)
  } finally { sync.dispose() }
})
