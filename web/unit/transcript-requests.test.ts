import { afterEach, expect, test } from 'bun:test'
import { ApiError, type Client } from '../src/api/client'
import type { CompactTurn, Entry, SessionDetail } from '../src/api/types'
import { loadHistory } from '../src/lib/model'
import { TranscriptRequests } from '../src/lib/transcript-requests'
import { TranscriptStore } from '../src/lib/transcript-store'
import fixtures from './fixtures/transcript-recovery.json'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const settle = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }
const sleep = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))
const entry = (id: string, parentId?: string, role = 'assistant', text = id): Entry => ({
  type: 'message', id, parentId, message: { role, content: [{ type: 'text', text }] },
})
const detail = (key: keyof typeof fixtures, leafId?: string): SessionDetail =>
  ({ id: 's', ...fixtures[key], leafId }) as SessionDetail
const controllers: TranscriptRequests[] = []
afterEach(() => { for (const controller of controllers.splice(0)) controller.dispose() })

class Transport {
  reads: Array<{ id: string; opts: Parameters<Client['get']>[1]; result: ReturnType<typeof deferred<SessionDetail>> }> = []
  batches: Array<{ ids: string[]; signal?: AbortSignal; result: ReturnType<typeof deferred<{ entries: Entry[] }>> }> = []
  get = (id: string, opts?: Parameters<Client['get']>[1]) => {
    const result = deferred<SessionDetail>()
    this.reads.push({ id, opts, result })
    return result.promise
  }
  getEntries = (_id: string, ids: string[], signal?: AbortSignal) => {
    const result = deferred<{ entries: Entry[] }>()
    this.batches.push({ ids, signal, result })
    return result.promise
  }
}
function setup(initial: SessionDetail, options: Partial<ConstructorParameters<typeof TranscriptRequests>[0]> = {}) {
  const store = new TranscriptStore(loadHistory(initial), 's')
  const api = new Transport()
  const history = new TranscriptRequests({
    api, id: 's', store, presentation: { mode: 'detailed', keep: 1 },
    beforeCommit: async () => {}, ...options,
  })
  controllers.push(history)
  history.start()
  return { store, api, history }
}
const compact = { mode: 'compact', keep: 0 } as const
function selectedVisible(): SessionDetail {
  const out = detail('selected', 'selected-a1')
  return { ...out, entries: [entry('u', undefined, 'user'), entry('selected-a1', 'u')],
    compactTurns: out.compactTurns!.map(turn => ({
      ...turn, entryIds: ['u', 'selected-a1'], visibleNodeIds: ['u', 'selected-a1'],
      omittedNodeIds: [], hiddenCount: 0,
    })) }
}

test('conversion rejected at the gesture boundary retries the current leaf without an exhausted cursor', async () => {
  const entries = [entry('u1', undefined, 'user'), entry('a1', 'u1'), entry('a2', 'a1'), entry('a3', 'a2')]
  const gate = deferred<void>()
  let gates = 0
  const { api, store, history } = setup({
    id: 's', entries, oldestId: 'a2', hasMore: true, leafId: 'a3', running: true,
  }, { presentation: { mode: 'compact', keep: 1 }, beforeCommit: () => ++gates === 1 ? gate.promise : Promise.resolve() })
  const summary: CompactTurn = {
    ...detail('selected').compactTurns![0], id: 'u1', tailId: 'a3', entryCount: 4,
    entryIds: ['u1', 'a3'], visibleNodeIds: ['u1', 'a3'], hiddenCount: 2, stepCount: 3,
  }
  const page: SessionDetail = { id: 's', entries: [entries[0], entries[3]],
    compactTurns: [summary], oldestId: 'u1', hasMore: false }
  const task = history.loadOlder()
  api.reads[0].result.resolve(page)
  await settle()
  expect(gates).toBe(1)
  store.applyEvents([{ type: 'message_end', entryId: 'a4', parentId: 'a3',
    message: { role: 'assistant', content: [{ type: 'text', text: 'latest' }] } }])
  await settle()
  expect(api.reads.map(call => call.opts?.turn)).toEqual(['a2', 'a2'])
  expect(api.reads[0].opts?.signal?.aborted).toBe(true)
  expect(store.current.oldestId).toBe('a2')
  expect(store.current.hasMore).toBe(true)
  expect(store.current.compactTurns ?? []).toEqual([])
  gate.resolve()
  api.reads[1].result.resolve({ ...page, entries: [entries[0], entry('a4', 'a3')],
    compactTurns: [{ ...summary, tailId: 'a4', entryIds: ['u1', 'a4'], visibleNodeIds: ['u1', 'a4'], entryCount: 5, hiddenCount: 3 }] })
  expect(await task).not.toBeNull()
  expect(store.current.leafId).toBe('a4')
  expect(store.current.oldestId).toBe('u1')
  expect(store.current.hasMore).toBe(false)
  expect(store.current.compactTurns?.[0].tailId).toBe('a4')
  expect(history.getSnapshot().loadingOlder).toBe(false)
})

test('conversion retries when recovery wins after its gesture promise settles', async () => {
  let store!: TranscriptStore
  let gates = 0
  const result = setup({ id: 's', entries: [
    entry('u', undefined, 'user'), entry('old-a1', 'u'), entry('old-a2', 'old-a1'),
  ], oldestId: 'old-a1', hasMore: true, leafId: 'old-a2' }, {
    presentation: compact,
    beforeCommit: async () => {
      if (++gates !== 1) return
      // Resolve wait(), then change authority before gate() resumes. No React
      // dispatcher or deferred state updater participates in this race.
      queueMicrotask(() => queueMicrotask(() => store.update(state => ({ ...state, oldestId: 'u' }))))
    },
  })
  store = result.store
  const { api, history } = result
  const task = history.loadOlder()
  api.reads[0].result.resolve(detail('oldPage', 'old-a2'))
  await settle()
  expect(api.reads.map(call => call.opts?.turn)).toEqual(['old-a1', 'u'])
  expect(store.current.hasMore).toBe(true)
  api.reads[1].result.resolve(detail('oldPage', 'old-a2'))
  expect(await task).not.toBeNull()
  expect(store.current.hasMore).toBe(false)
})

const oldDetailed = (): SessionDetail => ({ id: 's', entries: [
  entry('u', undefined, 'user'), entry('old-a1', 'u'), entry('old-a2', 'old-a1'),
], oldestId: 'u', leafId: 'old-a2', hasMore: false })

test('conversion rejects a foreign sibling tail even though the captured root and leaf never changed', async () => {
  const { api, store, history } = setup(oldDetailed(), { presentation: compact })
  const task = history.loadOlder()
  api.reads[0].result.resolve(selectedVisible())
  expect(await task).toBeNull()
  expect(api.reads).toHaveLength(1)
  expect(history.getSnapshot()).toMatchObject({ loadingOlder: false, olderError: true })
  expect(store.current.leafId).toBe('old-a2')
  expect(store.current.compactTurns ?? []).toEqual([])
  expect(store.current.nodes.map(node => node.id)).toEqual(['u', 'old-a1', 'old-a2'])
  store.update(state => ({ ...state, title: 'unrelated runtime update' }))
  await settle()
  expect(api.reads).toHaveLength(1)
})

test('foreign conversion reconciles branch authority before retrying against a detailed recovery', async () => {
  let store!: TranscriptStore
  let recoveries = 0
  const result = setup(oldDetailed(), { presentation: compact, onRecoverNeeded: async () => {
    recoveries++
    store.commit(store.capture('tail'), {
      id: 's', entries: [entry('u', undefined, 'user'), entry('selected-a1', 'u')],
      oldestId: 'u', leafId: 'selected-a1', hasMore: false,
    })
  } })
  store = result.store
  const { api, history } = result
  const task = history.loadOlder()
  api.reads[0].result.resolve(selectedVisible())
  await settle()
  expect(recoveries).toBe(1)
  expect(api.reads).toHaveLength(2)
  expect(store.current.compactTurns ?? []).toEqual([])
  expect(store.current.leafId).toBe('selected-a1')
  api.reads[1].result.resolve(selectedVisible())
  expect(await task).not.toBeNull()
  expect(store.current.compactTurns?.map(turn => turn.tailId)).toEqual(['selected-a1'])
  expect(history.getSnapshot().olderError).toBe(false)
})

test('foreign conversion retry is bounded when reconciliation cannot prove the returned branch', async () => {
  let recoveries = 0
  const { api, store, history } = setup(oldDetailed(), { presentation: compact, onRecoverNeeded: async () => { recoveries++ } })
  const task = history.loadOlder()
  api.reads[0].result.resolve(selectedVisible())
  await settle()
  expect(api.reads).toHaveLength(2)
  api.reads[1].result.resolve(selectedVisible())
  expect(await task).toBeNull()
  expect(recoveries).toBe(1)
  expect(api.reads).toHaveLength(2)
  expect(store.current.compactTurns ?? []).toEqual([])
  expect(history.getSnapshot().olderError).toBe(true)
})

test('conversion accepts the oldest turn tail on the current path without requiring it to equal the latest leaf', async () => {
  const entries = [entry('u', undefined, 'user'), entry('a1', 'u'),
    entry('v', 'a1', 'user'), entry('a2', 'v'), entry('w', 'a2', 'user'), entry('a3', 'w')]
  const { api, store, history } = setup({ id: 's', entries, oldestId: 'v', leafId: 'a3', hasMore: true }, { presentation: compact })
  const task = history.loadOlder()
  expect(api.reads[0].opts?.turn).toBe('v')
  const summary: CompactTurn = { ...detail('selected').compactTurns![0],
    id: 'v', parentId: 'a1', tailId: 'a2', entryIds: ['v', 'a2'], visibleNodeIds: ['v', 'a2'], omittedNodeIds: [], hiddenCount: 0 }
  api.reads[0].result.resolve({ id: 's', entries: entries.slice(2, 4), oldestId: 'v', hasMore: true, compactTurns: [summary] })
  expect(await task).not.toBeNull()
  expect(store.current.leafId).toBe('a3')
  expect(store.current.compactTurns?.[0].tailId).toBe('a2')
  expect(store.current.nodes.map(node => node.id)).toContain('a3')
  expect(store.current.oldestId).toBe('v')
  expect(store.current.hasMore).toBe(true)
  expect(history.getSnapshot().olderError).toBe(false)
})

test('Go compact page cannot resurrect a branch recovered while its gesture gate waits', async () => {
  const gate = deferred<void>()
  const { api, store, history } = setup(detail('oldTail', 'y-answer'), {
    presentation: compact, beforeCommit: () => gate.promise,
  })
  const task = history.loadOlder()
  api.reads[0].result.resolve(detail('oldPage', 'y-answer'))
  await settle()
  expect(store.commit(store.capture('tail'), detail('selected', 'selected-a1'))).toBe(true)
  expect(await task).toBeNull()
  gate.resolve()
  await settle()
  expect(store.current.leafId).toBe('selected-a1')
  expect(store.current.compactTurns?.map(turn => turn.tailId)).toEqual(['selected-a1'])
  expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
  expect(store.current.hasMore).toBe(false)
  expect(history.getSnapshot().olderError).toBe(false)
})

test('frontier change aborts and releases an ordinary page even when the boundary did not change', async () => {
  const { api, store, history } = setup(detail('oldTail', 'y-answer'), { presentation: compact })
  const old = history.loadOlder()
  store.commit(store.capture('tail'), detail('oldTail', 'y-answer'))
  const next = history.loadOlder()
  expect(next).not.toBe(old)
  expect(api.reads).toHaveLength(2)
  expect(api.reads[0].opts?.signal?.aborted).toBe(true)
  expect(await old).toBeNull()
  api.reads[0].result.resolve(detail('oldPage', 'y-answer'))
  await settle()
  expect(store.current.oldestId).toBe('v')
  api.reads[1].result.resolve(detail('oldPage', 'y-answer'))
  expect(await next).not.toBeNull()
  expect(store.current.oldestId).toBe('u')
})

test('concurrent loads deduplicate and sequential loads read the synchronously committed cursor', async () => {
  const { api, store, history } = setup({ id: 's', entries: [entry('c', 'b')], oldestId: 'c', leafId: 'c', hasMore: true })
  const one = history.loadOlder(7)
  expect(history.loadOlder(9)).toBe(one)
  expect(api.reads).toHaveLength(1)
  expect(api.reads[0].opts?.limit).toBe(7)
  api.reads[0].result.resolve({ id: 's', entries: [entry('b', 'a')], oldestId: 'b', hasMore: true })
  expect(await one).not.toBeNull()
  const two = history.loadOlder()
  expect(api.reads.map(call => call.opts?.before)).toEqual(['c', 'b'])
  api.reads[1].result.resolve({ id: 's', entries: [entry('a', undefined, 'user')], oldestId: 'a', hasMore: false })
  expect(await two).not.toBeNull()
  expect(store.current.oldestId).toBe('a')
  expect(await history.loadOlder()).toBeNull()
  expect(api.reads).toHaveLength(2)
})

test('409 adopts a replacement boundary atomically and leaves an explicit page retry', async () => {
  const { api, store, history } = setup(detail('oldTail', 'y-answer'), { presentation: compact })
  const task = history.loadOlder()
  api.reads[0].result.reject(new ApiError(409, 'invalid history cursor'))
  await settle()
  expect(api.reads).toHaveLength(2)
  expect(api.reads[1].opts?.before).toBeUndefined()
  api.reads[1].result.resolve({ ...detail('oldTail', 'y-answer'), oldestId: 'w' })
  expect(await task).toBeNull()
  expect(store.current.oldestId).toBe('w')
  expect(store.current.hasMore).toBe(true)
  expect(history.getSnapshot()).toMatchObject({ loadingOlder: false, olderError: true })
  expect(api.reads).toHaveLength(2)
  const retry = history.loadOlder()
  expect(api.reads[2].opts?.before).toBe('w')
  api.reads[2].result.resolve(detail('oldPage', 'y-answer'))
  expect(await retry).not.toBeNull()
  expect(history.getSnapshot().olderError).toBe(false)
})

test('nonadvancing page is an error, not a silently exhausted transcript', async () => {
  const { api, store, history } = setup({ id: 's', entries: [entry('b')], oldestId: 'b', leafId: 'b', hasMore: true })
  const task = history.loadOlder()
  api.reads[0].result.resolve({ id: 's', entries: [entry('b')], oldestId: 'b', hasMore: true })
  expect(await task).toBeNull()
  expect(store.current.hasMore).toBe(true)
  expect(store.current.oldestId).toBe('b')
  expect(history.getSnapshot().olderError).toBe(true)
})

for (const recoverBeforeReply of [false, true]) {
  test(`keep reprojection reconciles then retries once (${recoverBeforeReply ? 'local frontier changed' : 'server selected a new branch'})`, async () => {
    let recoveries = 0
    let store!: TranscriptStore
    const result = setup(detail('oldPage', 'old-a2'), {
      presentation: { mode: 'compact', keep: 1 },
      onRecoverNeeded: async () => {
        recoveries++
        store.commit(store.capture('tail'), detail('selected', 'selected-a1'))
      },
    })
    store = result.store
    const { api } = result
    await settle()
    expect(api.reads).toHaveLength(1)
    if (recoverBeforeReply) store.commit(store.capture('tail'), detail('selected', 'selected-a1'))
    api.reads[0].result.resolve(recoverBeforeReply ? detail('oldPage', 'old-a2') : selectedVisible())
    await settle()
    expect(recoveries).toBe(1)
    expect(api.reads).toHaveLength(2)
    expect(store.current.compactTurns?.map(turn => turn.tailId)).toEqual(['selected-a1'])
    expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
    api.reads[1].result.resolve(selectedVisible())
    await settle()
    expect(store.current.nodes.map(node => node.id)).toEqual(['u', 'selected-a1'])
    expect(store.current.compactTurns?.map(turn => turn.tailId)).toEqual(['selected-a1'])
    expect(api.reads).toHaveLength(2)
  })
}

test('keep recovery retry is bounded when the server still returns another target', async () => {
  let recoveries = 0
  const { api, store } = setup(detail('oldPage', 'old-a2'), {
    presentation: { mode: 'compact', keep: 1 }, onRecoverNeeded: async () => { recoveries++ },
  })
  await settle()
  api.reads[0].result.resolve(selectedVisible())
  await settle()
  api.reads[1].result.resolve(selectedVisible())
  await settle()
  expect(api.reads).toHaveLength(2)
  expect(recoveries).toBe(1)
  expect(store.current.compactTurns?.[0].tailId).toBe('old-a2')
})

test('concurrent stale keep responses share one recovery before their bounded retries', async () => {
  const recovered = deferred<void>()
  let recoveries = 0
  const { api, store } = setup(detail('oldTail', 'y-answer'), {
    presentation: { mode: 'compact', keep: 1 }, onRecoverNeeded: () => { recoveries++; return recovered.promise },
  })
  await settle()
  expect(api.reads).toHaveLength(4)
  for (const call of api.reads) {
    const turn = store.current.compactTurns!.find(turn => turn.id === call.opts?.turn)!
    call.result.resolve({ id: 's', compactTurns: [{ ...turn, tailId: `new-${turn.id}` }], entries: [] })
  }
  await settle()
  expect(recoveries).toBe(1)
  expect(api.reads).toHaveLength(4)
  store.commit(store.capture('tail'), detail('oldTail', 'y-answer'))
  recovered.resolve()
  await settle()
  expect(api.reads).toHaveLength(8)
  for (const call of api.reads.slice(4)) call.result.resolve(detail('oldTail', 'y-answer'))
  await settle()
  expect(api.reads).toHaveLength(8)
  expect(recoveries).toBe(1)
})

test('detailed mode automatically retries expansion canceled by a same-tail recovery', async () => {
  const { api, store, history } = setup(detail('oldPage', 'old-a2'))
  await settle()
  expect(api.reads).toHaveLength(1)
  store.commit(store.capture('tail'), detail('oldPage', 'old-a2'))
  await settle()
  expect(api.reads[0].opts?.signal?.aborted).toBe(true)
  expect(api.reads).toHaveLength(2)
  const task = history.requestTurn('u')
  api.reads[1].result.resolve({ id: 's', entries: [
    entry('u', undefined, 'user'), entry('old-a1', 'u'), entry('old-a2', 'old-a1'),
  ] })
  expect(await task).toBe(true)
  expect(store.current.loadedTurnIds).toContain('u')
  expect(api.reads).toHaveLength(2)
})

test('full turn pagination deduplicates and commits all bodies only after the final gesture gate', async () => {
  const gate = deferred<void>()
  const { api, store, history } = setup(detail('oldPage', 'old-a2'), {
    presentation: compact, beforeCommit: () => gate.promise,
  })
  const before = store.current
  const task = history.requestTurn('u')
  expect(history.requestTurn('u')).toBe(task)
  api.reads[0].result.resolve({ id: 's', entries: [entry('old-a2', 'old-a1')], oldestId: 'old-a2', hasMore: true })
  await settle()
  expect(api.reads).toHaveLength(2)
  expect(api.reads[1].opts?.before).toBe('old-a2')
  expect(api.reads[0].opts?.limit).toBe(500)
  expect(store.current).toBe(before)
  api.reads[1].result.resolve({ id: 's', entries: [entry('u', undefined, 'user'), entry('old-a1', 'u')], hasMore: false })
  await settle()
  expect(store.current).toBe(before)
  gate.resolve()
  expect(await task).toBe(true)
  expect(store.current.loadedTurnIds).toContain('u')
  expect(store.current.nodes.map(node => node.id)).toEqual(['u', 'old-a1', 'old-a2'])
  expect(await history.requestTurn('u')).toBe(true)
  expect(api.reads).toHaveLength(2)
})

test('turn expansion arriving after branch recovery cannot mark the new target loaded', async () => {
  const { api, store, history } = setup(detail('oldPage', 'old-a2'), { presentation: compact })
  const task = history.requestTurn('u')
  store.commit(store.capture('tail'), detail('selected', 'selected-a1'))
  expect(await task).toBe(false)
  expect(api.reads[0].opts?.signal?.aborted).toBe(true)
  api.reads[0].result.resolve({ id: 's', entries: [entry('u', undefined, 'user'), entry('old-a1', 'u'), entry('old-a2', 'old-a1')] })
  await settle()
  expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
  expect(store.current.loadedTurnIds ?? []).not.toContain('u')
  expect(store.current.compactTurns?.[0].tailId).toBe('selected-a1')
})

test('turn expansion must include the target summary tail', async () => {
  const { api, store, history } = setup(detail('oldPage', 'old-a2'), { presentation: compact })
  const task = history.requestTurn('u')
  api.reads[0].result.resolve({ id: 's', entries: [entry('u', undefined, 'user'), entry('selected-a1', 'u')], hasMore: false })
  expect(await task).toBe(false)
  expect(store.current.compactTurns?.[0].tailId).toBe('old-a2')
  expect(store.current.loadedTurnIds ?? []).not.toContain('u')
})

test('same-ID same-object reopen cancels all old waiters and cannot commit late bodies or metadata', async () => {
  const { api, store, history } = setup(detail('oldTail', 'y-answer'), { presentation: compact })
  const page = history.loadOlder()
  const index = history.requestIndex('s')
  const turn = history.requestTurn('v')
  const body = history.requestHydrate('missing')
  await sleep(40)
  expect(api.batches).toHaveLength(1)
  const unchanged = store.current
  store.reset('s', unchanged)
  expect(await page).toBeNull()
  expect(await index).toBe(false)
  expect(await turn).toBe(false)
  expect(await body).toBe(false)
  expect(api.reads.every(call => call.opts?.signal?.aborted)).toBe(true)
  expect(api.batches[0].signal?.aborted).toBe(true)
  for (const call of api.reads) call.result.resolve(detail('oldPage', 'old-a2'))
  api.batches[0].result.resolve({ entries: [entry('missing')] })
  await settle()
  expect(store.current).toBe(unchanged)
  history.start()
  const reopened = history.loadOlder()
  expect(api.reads).toHaveLength(4)
  expect(api.reads[3].opts?.signal?.aborted).toBe(false)
  api.reads[3].result.resolve(detail('oldPage', 'y-answer'))
  expect(await reopened).not.toBeNull()
})

test('dispose releases ignored gesture gates and can restart with a clean scope', async () => {
  const gate = deferred<void>()
  const { api, store, history } = setup(detail('oldTail', 'y-answer'), {
    presentation: compact, beforeCommit: () => gate.promise,
  })
  const task = history.loadOlder()
  const body = history.requestHydrate('not-yet-batched')
  api.reads[0].result.resolve(detail('oldPage', 'y-answer'))
  await settle()
  const before = store.current
  history.dispose()
  expect(await task).toBeNull()
  expect(await body).toBe(false)
  gate.resolve()
  await sleep(40)
  expect(api.batches).toHaveLength(0)
  expect(store.current).toBe(before)
  history.start()
  const next = history.loadOlder()
  api.reads[1].result.resolve(detail('oldPage', 'y-answer'))
  expect(await next).not.toBeNull()
})

test('index refresh coalesces a trailing frontier and preserves immutable status snapshots', async () => {
  const { api, store, history } = setup({ id: 's', entries: [], leafId: 'a' })
  const idle = history.getSnapshot()
  expect(history.getSnapshot()).toBe(idle)
  expect(Object.isFrozen(idle)).toBe(true)
  let notifications = 0
  const unsubscribe = history.subscribe(() => { notifications++ })
  const task = history.requestIndex('s')
  expect(history.requestIndex('s')).toBe(task)
  expect(history.getSnapshot()).not.toBe(idle)
  expect(idle.indexLoading).toBe(false)
  history.refreshIndex('s', 'b')
  history.refreshIndex('s', 'b')
  api.reads[0].result.resolve({ id: 's', index: [] })
  expect(await task).toBe(true)
  await settle()
  expect(api.reads).toHaveLength(2)
  expect(api.reads.every(call => call.opts?.fields === 'index')).toBe(true)
  expect(history.getSnapshot().indexLoading).toBe(true)
  api.reads[1].result.resolve({ id: 's', index: [] })
  await settle()
  history.refreshIndex('s', 'b')
  expect(api.reads).toHaveLength(2)
  expect(store.current.indexLoaded).toBe(true)
  expect(history.getSnapshot()).toMatchObject({ indexLoading: false, indexError: false })
  expect(notifications).toBeGreaterThan(1)
  unsubscribe()
})

test('missing index metadata fails explicitly and a later request retries', async () => {
  const { api, history } = setup({ id: 's', entries: [] })
  const first = history.requestIndex('s')
  api.reads[0].result.resolve({ id: 's' })
  expect(await first).toBe(false)
  expect(history.getSnapshot().indexError).toBe(true)
  const retry = history.requestIndex('s')
  api.reads[1].result.resolve({ id: 's', index: [] })
  expect(await retry).toBe(true)
  expect(history.getSnapshot().indexError).toBe(false)
})

test('body requests batch at most forty after 32ms, deduplicate, and keep concurrency at one', async () => {
  const { api, history, store } = setup({ id: 's', entries: [] })
  const tasks = Array.from({ length: 81 }, (_, i) => history.requestHydrate(`body-${i}`))
  const duplicate = history.requestHydrate('body-0')
  expect(api.batches).toHaveLength(0)
  await sleep(40)
  expect(api.batches).toHaveLength(1)
  expect(api.batches[0].ids).toHaveLength(40)
  await sleep(40)
  expect(api.batches).toHaveLength(1)
  for (let i = 0; i < 3; i++) {
    const batch = api.batches[i]
    expect(batch.ids.length).toBe(i === 2 ? 1 : 40)
    batch.result.resolve({ entries: batch.ids.map(id => entry(id)) })
    await settle()
  }
  expect((await Promise.all(tasks)).every(Boolean)).toBe(true)
  expect(await duplicate).toBe(true)
  expect(store.current.entries).toHaveLength(81)
  expect(api.batches).toHaveLength(3)
})

test('tool-row hydration requests both persisted call and result bodies', async () => {
  const call: Entry = { ...entry('call', 'u'), truncated: true,
    message: { role: 'assistant', content: [{ type: 'toolCall', id: 'tool', name: 'Read' }] } }
  const result: Entry = { ...entry('result', 'call'), truncated: true,
    message: { role: 'toolResult', toolCallId: 'tool', content: [{ type: 'text', text: 'preview' }] } }
  const { api, history } = setup({ id: 's', entries: [entry('u', undefined, 'user'), call, result], leafId: 'result' })
  const task = history.requestHydrate('tool')
  await sleep(40)
  expect(api.batches[0].ids).toEqual(['call', 'result'])
  api.batches[0].result.resolve({ entries: [{ ...call, truncated: false }, { ...result, truncated: false }] })
  expect(await task).toBe(true)
})

test('late body caching does not grant old compact metadata authority over the recovered branch', async () => {
  const { api, store, history } = setup(detail('oldPage', 'old-a2'), { presentation: compact })
  const task = history.requestHydrate('old-a2')
  await sleep(40)
  store.commit(store.capture('tail'), detail('selected', 'selected-a1'))
  const payload = { ...detail('oldPage', 'old-a2'), entries: [entry('old-a1', 'u'), entry('old-a2', 'old-a1')] }
  api.batches[0].result.resolve(payload)
  expect(await task).toBe(true)
  expect(store.current.entries.some(body => body.id === 'old-a2')).toBe(true)
  expect(store.current.leafId).toBe('selected-a1')
  expect(store.current.compactTurns?.map(turn => turn.tailId)).toEqual(['selected-a1'])
  expect(store.current.nodes.map(node => node.id)).toEqual(['u'])
  expect(store.current.hasMore).toBe(false)
})

test('explicit incomplete body provenance cannot bypass hydration', async () => {
  const { api, history } = setup({ id: 's', entries: [{ ...entry('helper'), bodyKind: 'helper' }], leafId: 'helper' })
  const task = history.requestHydrate('helper')
  await sleep(40)
  expect(api.batches[0].ids).toEqual(['helper'])
  api.batches[0].result.resolve({ entries: [entry('helper')] })
  expect(await task).toBe(true)
})

test('delayed body eviction preserves protected IDs and the latest turn', async () => {
  const large = 'x'.repeat(2_200_000)
  const { history, store } = setup({ id: 's', entries: [
    entry('u', undefined, 'user'), entry('protected', 'u', 'assistant', large),
    entry('cold', 'protected', 'assistant', large), entry('latest', 'cold', 'user'),
  ], leafId: 'latest', oldestId: 'u', hasMore: false })
  history.protect(['cold'])
  history.protect(['protected'])
  await sleep(550)
  expect(store.current.entries.find(body => body.id === 'protected')?.truncated).not.toBe(true)
  expect(store.current.entries.find(body => body.id === 'cold')?.truncated).toBe(true)
  expect(store.current.entries.find(body => body.id === 'latest')?.truncated).not.toBe(true)
  expect(store.current.oldestId).toBe('u')
})
