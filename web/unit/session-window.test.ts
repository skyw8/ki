import { expect, test } from 'bun:test'
import { applyEvent, applyIndex, applyTail, compactBoundary, evictBodies, hydrateEntries, loadHistory, turnStats } from '../src/lib/model.ts'
import type { Entry, IndexEntry } from '../src/api/types.ts'

// The WebUI loads the newest window of a session and lets the tree index arrive
// afterwards, so that opening a long session does not wait for a parse of the
// whole transcript. These tests pin the window semantics of the model layer.

function user(n: number): Entry {
  return { type: 'message', id: `u${n}`, parentId: `a${n - 1}`, message: { role: 'user', content: [{ type: 'text', text: `turn ${n}` }] } }
}

function assistant(n: number): Entry {
  return { type: 'message', id: `a${n}`, parentId: `u${n}`, message: { role: 'assistant', content: [{ type: 'text', text: `reply ${n}` }], usage: { input: 10, output: 2 } } }
}

function row(e: Entry, preview: string): IndexEntry {
  return {
    type: e.type,
    id: e.id,
    parentId: e.parentId,
    role: e.message?.role,
    preview,
  }
}

/** Twelve turns; the tail window holds the newest four. */
function fixture() {
  const all: Entry[] = []
  for (let n = 1; n <= 12; n++) all.push(user(n), assistant(n))
  all[0] = { ...all[0], parentId: '' }
  const entries = all.slice(-8)
  const index = all.map(e => row(e, `turn ${e.id}`))
  return { all, entries, index, leafId: 'a12' }
}

test('a tail window builds nodes only for the entries it holds', () => {
  const { entries, leafId } = fixture()
  const view = loadHistory({ id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries, oldestId: 'u9', hasMore: true })

  expect(view.nodes.map(n => n.id)).toEqual(entries.map(e => e.id))
  expect(view.nodes.filter(n => n.kind === 'user').map(n => n.id)).toEqual(['u9', 'u10', 'u11', 'u12'])
  // Without the index the branch numbering is relative: the caller knows the
  // window, not how much history precedes it.
  expect(view.indexLoaded).toBe(false)
  expect(view.turnBase).toBe(0)
  expect(view.records.filter(r => r.kind === 'user')).toHaveLength(4)
  expect(view.oldestId).toBe('u9')
})

test('a persisted run abort becomes a standalone history row', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'go' }] } },
    { type: 'message', id: 'a1', parentId: 'u1', message: { role: 'assistant', content: [], stopReason: 'aborted' } },
    { type: 'context_usage', id: 'c1', parentId: 'a1' },
    { type: 'run_aborted', id: 'x1', parentId: 'c1', details: { reason: 'user_request', source: 'webui' } },
  ]
  const view = loadHistory({ id: 's', entries, leafId: 'x1' })
  expect(view.nodes.at(-1)).toEqual({
    kind: 'cancellation',
    id: 'x1',
    runId: undefined,
    reason: 'user_request',
    source: 'webui',
    ts: undefined,
  })
})

test('a legacy aborted assistant synthesizes a generic standalone row', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'go' }] } },
    { type: 'message', id: 'a1', parentId: 'u1', message: { role: 'assistant', content: [], stopReason: 'aborted', errorMessage: 'context canceled' } },
  ]
  const view = loadHistory({ id: 's', entries, leafId: 'a1' })
  expect(view.nodes.at(-1)).toMatchObject({
    kind: 'cancellation',
    id: 'legacy-cancellation:a1',
    reason: undefined,
    source: undefined,
  })
})

test('persisted compaction start and end pair across distinct entry ids', () => {
  const entries: Entry[] = [
    { type: 'compaction_start', id: 'compact-start', details: { reason: 'manual' } },
    { type: 'compaction_end', id: 'compact-end', parentId: 'compact-start', details: { reason: 'manual', status: 'committed', ok: true } },
  ]
  const view = loadHistory({ id: 's', entries, leafId: 'compact-end' })
  const compact = view.records.find(record => record.kind === 'compact')
  expect(compact?.running).toBe(false)
  expect(compact?.preview).toBe('Compacted (manual)')
})

test('persisted failed compaction is not projected as success', () => {
  const entries: Entry[] = [
    { type: 'compaction_start', id: 'compact-start', details: { reason: 'threshold' } },
    { type: 'compaction_end', id: 'compact-end', parentId: 'compact-start', details: { reason: 'threshold', status: 'failed' } },
  ]
  const view = loadHistory({ id: 's', entries, leafId: 'compact-end' })
  expect(view.records.find(record => record.kind === 'compact')?.preview).toBe('Compacted (threshold) (failed)')
})

test('the lazy index renumbers turns without adding nodes', () => {
  const { entries, index, leafId } = fixture()
  let view = loadHistory({ id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries })
  view = applyIndex(view, { id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries, index })

  expect(view.indexLoaded).toBe(true)
  expect(view.turnBase).toBe(8)
  expect(view.nodes.map(n => n.id)).toEqual(entries.map(e => e.id))
  expect([...turnStats(view.nodes, view.turnBase).values()].map(s => s.turn)).toEqual([9, 10, 11, 12])
  // The trajectory table and branch navigation see the whole tree.
  expect(view.records.filter(r => r.kind === 'user')).toHaveLength(12)
  expect(view.allEntries).toHaveLength(24)
})

test('paging older history prepends nodes and keeps the offset exact', () => {
  const { all, entries, index, leafId } = fixture()
  let view = loadHistory({ id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries, index })
  const page = all.slice(0, 16)
  view = hydrateEntries(view, page, { hasMore: false, oldestId: 'u1' })

  expect(view.nodes.map(n => n.id)).toEqual(all.map(e => e.id))
  expect(view.turnBase).toBe(0)
  expect([...turnStats(view.nodes, view.turnBase).values()].map(s => s.turn)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12])
  expect(view.hasMore).toBe(false)
  expect(view.oldestId).toBe('u1')
})

test('a finished run refreshes the tail and keeps the index warm', () => {
  const { entries, index, leafId } = fixture()
  let view = loadHistory({ id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries, index })
  const next: Entry[] = [user(13), assistant(13)]
  const window = [...entries.slice(2), ...next]
  view = applyTail(view, {
    id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId: 'a13',
    entries: window,
  })

  expect(view.indexLoaded).toBe(true)
  expect(view.index.map(r => r.id)).toContain('u13')
  // The window slides forward and keeps the entries already loaded around it
  // (u9/a9 here), which is what makes the refresh cheap: no page is refetched.
  expect(view.nodes.map(n => n.id)).toEqual(['u9', 'a9', ...window.map(e => e.id)])
  // Records still cover the whole branch (the trajectory table, the request
  // navigator), including entries whose bodies are only in the index.
  const users = view.records.filter(r => r.kind === 'user')
  expect(users).toHaveLength(13)
  expect(users.map(r => r.turn)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13])
})

test('a rebuild during a run keeps the streaming bubble', () => {
  const { entries, index, leafId } = fixture()
  let view = loadHistory({ id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId, entries, index })
  view = { ...view, nodes: [...view.nodes, { kind: 'assistant', id: 'live-asst', text: 'partial', thinking: '', streaming: true }] }

  const rebuilt = hydrateEntries(view, [])
  expect(rebuilt).toBe(view)
  const paged = hydrateEntries(view, [user(8)])
  expect(paged.nodes.filter(n => n.id === 'live-asst')).toHaveLength(1)
  expect(paged.nodes[paged.nodes.length - 1].id).toBe('live-asst')
})

test('an idle recovery snapshot retires missed terminal events without losing loaded history', () => {
  const { entries, index, leafId } = fixture()
  let view = loadHistory({ id: 's', entries, index, leafId, running: true })
  view = applyEvent(view, { type: 'message_update', message: { role: 'assistant', content: [{ type: 'text', text: 'unfinished partial' }] } })
  view = applyEvent(view, { type: 'tool_execution_start', toolCallId: 'lost-tool', toolName: 'Bash' })
  view = applyEvent(view, { type: 'run_aborted' })
  const recovered = applyTail(view, { id: 's', running: false, leafId: 'a13', entries: [user(13), assistant(13)] })
  expect(recovered.busy).toBe(false)
  expect(recovered.stopping).toBe(false)
  expect(recovered.nodes.map(n => n.id)).toEqual([...entries.map(e => e.id), 'u13', 'a13'])
  expect(recovered.records.some(r => r.running)).toBe(false)
  expect(recovered.requests.some(r => r.status === 'running')).toBe(false)
  expect(recovered.indexLoaded).toBe(true)
})

test('a late index over a long branch stays cheap', () => {
  // 2000 entries on the branch: the index rebuild walks them to number turns
  // and fill the trajectory table, but it must never build chat nodes for them.
  const all: Entry[] = []
  for (let n = 1; n <= 1000; n++) all.push(user(n), assistant(n))
  all[0] = { ...all[0], parentId: '' }
  const entries = all.slice(-200)
  const index = all.map(e => row(e, `turn ${e.id}`))
  const detail = { id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId: 'a1000', entries }

  const t0 = Date.now()
  let view = loadHistory(detail)
  view = applyIndex(view, { ...detail, index })
  const elapsed = Date.now() - t0

  expect(view.indexLoaded).toBe(true)
  expect(view.turnBase).toBe(900)
  expect(view.nodes).toHaveLength(200)
  expect(view.records.filter(r => r.kind === 'user')).toHaveLength(1000)
  expect(elapsed).toBeLessThan(1_000)
})

test('a tail refresh preserves a 500-entry reading window and its cursor', () => {
  const all = Array.from({ length: 300 }, (_, i) => [user(i + 1), assistant(i + 1)]).flat()
  let view = loadHistory({ id: 's', entries: all.slice(-500), leafId: 'a300', oldestId: 'u51', hasMore: true })
  view = applyTail(view, { id: 's', entries: all.slice(-100), leafId: 'a300', oldestId: 'u251', hasMore: true })
  expect(view.entries.map(e => e.id)).toEqual(all.slice(-500).map(e => e.id))
  expect(view.oldestId).toBe('u51')
  view = hydrateEntries(view, all.slice(0, 100), { oldestId: 'u1', hasMore: false })
  view = applyTail(view, { id: 's', entries: all.slice(-100), leafId: 'a300', oldestId: 'u251', hasMore: true })
  expect(view.hasMore).toBe(false)
  expect(view.oldestId).toBe('u1')
  expect(view.nodes.map(n => n.id)).toEqual(all.map(e => e.id))
})

test('late slim pages and index never downgrade bodies or the live leaf', () => {
  const full = assistant(1)
  let view = loadHistory({ id: 's', entries: [user(1), full], leafId: 'a1', oldestId: 'u1', hasMore: false })
  const unchanged = view.nodes[0]
  view = hydrateEntries(view, [{ ...full, truncated: true, message: { role: 'assistant', content: [{ type: 'text', text: 'preview' }] } }])
  view = applyIndex(view, { id: 's', index: [row(user(1), 'turn 1')], leafId: 'u1', oldestId: 'stale', hasMore: true })
  expect(view.entries.find(e => e.id === 'a1')).toBe(full)
  expect(view.nodes[0]).toBe(unchanged)
  expect(view.leafId).toBe('a1')
  expect(view.oldestId).toBe('u1')
  expect(view.hasMore).toBe(false)
})

test('body eviction preserves all ids, the visible row and the active tail', () => {
  const all = Array.from({ length: 6 }, (_, i) => [user(i + 1), {
    ...assistant(i + 1), message: { role: 'assistant', content: [{ type: 'text', text: `${i} ` + 'body '.repeat(4000) }] },
  }]).flat()
  const view = loadHistory({ id: 's', entries: all, leafId: 'a6', oldestId: 'u1', hasMore: false })
  const trimmed = evictBodies(view, new Set(['a2']), 60_000)
  expect(trimmed.entries.map(e => e.id)).toEqual(view.entries.map(e => e.id))
  expect(trimmed.entries.find(e => e.id === 'a2')?.truncated).toBeFalsy()
  expect(trimmed.entries.find(e => e.id === 'a6')?.truncated).toBeFalsy()
  expect(trimmed.entries.find(e => e.id === 'a1')?.truncated).toBe(true)
  expect(trimmed.oldestId).toBe(view.oldestId)
  expect(trimmed.hasMore).toBe(false)
  const restored = hydrateEntries(trimmed, [all[1]])
  expect(restored.entries[1]).toBe(all[1])
})


test('a disconnected recovery tail reopens paging instead of preserving a false root', () => {
  const { all, index } = fixture()
  for (const loadedIndex of [undefined, index.slice(0, 4), index]) {
    let s = loadHistory({ entries: all.slice(0, 4), index: loadedIndex, leafId: 'a2', oldestId: 'u1', hasMore: false } as any)
    s = applyTail(s, { entries: all.slice(-4), leafId: 'a12', oldestId: 'u11', hasMore: true } as any)
    expect(s.oldestId).toBe('u11')
    expect(s.hasMore).toBe(true)
    expect(s.nodes.map(n => n.id)).toEqual(['u11', 'a11', 'u12', 'a12'])
    // Lost ranges remain in the identity/body cache, not stranded behind an
    // old root cursor. Paging back through the gap reconnects them.
    s = hydrateEntries(s, all.slice(4, -4), { oldestId: 'u3', hasMore: true })
    s = hydrateEntries(s, all.slice(0, 4), { oldestId: 'u1', hasMore: false })
    expect(s.nodes.map(n => n.id)).toEqual(all.map(e => e.id))
  }
})

test('an SSE completion at the recovered leaf does not prove the older window is connected', () => {
  const { all, index } = fixture()
  for (const loadedIndex of [undefined, index]) for (const inFlight of [false, true]) {
    let s = loadHistory({ id: 's', entries: all.slice(0, 4), index: loadedIndex, leafId: 'a2', oldestId: 'u1', hasMore: false, running: true })
    const requestedRevision = s.liveRevision
    // A resumed stream can deliver the final message before the tail GET.
    // Its local reading bridge keeps the old content visible, but proves
    // nothing about the persisted entries missed while this tab was away.
    s = applyEvent(s, { type: 'message_end', entryId: 'a12', parentId: 'u12', message: assistant(12).message })
    expect(s.leafId).toBe('a12')
    s = applyTail(s, { id: 's', entries: all.slice(-4), leafId: 'a12', oldestId: 'u11', hasMore: true },
      inFlight ? requestedRevision : s.liveRevision)
    expect(s.oldestId).toBe('u11')
    expect(s.hasMore).toBe(true)
    expect(s.nodes.map(n => n.id)).toEqual(['u11', 'a11', 'u12', 'a12'])
    s = hydrateEntries(s, all.slice(0, -4), { oldestId: 'u1', hasMore: false })
    expect(s.nodes.map(n => n.id)).toEqual(all.map(e => e.id))
  }
})

test('a delayed compact conversion cannot restore an exhausted boundary after recovery', () => {
  const { all } = fixture()
  const source = loadHistory({ id: 's', entries: all.slice(0, 4), leafId: 'a2', oldestId: 'u1', hasMore: false })
  const recovered = applyTail(source, { id: 's', entries: all.slice(-4), leafId: 'a12', oldestId: 'u11', hasMore: true })
  const stats = [...turnStats(source.nodes).values()][0]
  const projected = compactBoundary(recovered, {
    id: 's', entries: all.slice(0, 2), oldestId: 'u1', hasMore: false,
    compactTurns: [{ id: 'u1', tailId: 'a1', entryIds: ['u1', 'a1'], visibleNodeIds: ['u1', 'a1'], hiddenCount: 0, stepCount: 1, stats }],
  }, source)
  expect(projected).toBe(recovered)
  expect(projected.oldestId).toBe('u11')
  expect(projected.hasMore).toBe(true)
})

test('a delayed idle snapshot cannot roll back a newer SSE completion or running state', () => {
  const first = user(1)
  let s = loadHistory({ entries: [first], leafId: first.id, running: true } as any)
  const revision = s.liveRevision
  s = applyEvent(s, { type: 'message_end', entryId: 'a1', message: { role: 'assistant', timestamp: 1_000, content: [{ type: 'text', text: 'new result' }] } })
  s = applyTail(s, { entries: [first], leafId: first.id, running: false } as any, revision)
  expect(s.leafId).toBe('a1')
  expect(s.busy).toBe(true)
  expect(s.nodes.map(n => n.id)).toEqual(['u1', 'a1'])
})


test('SSE advances to a descendant already known by a racing index response', () => {
  let s = loadHistory({ entries: [user(1)], leafId: 'u1', running: true } as any)
  s = applyIndex(s, { index: [row(user(1), 'prompt'), row(assistant(1), 'answer')] } as any)
  s = applyEvent(s, { type: 'message_end', entryId: 'a1', message: assistant(1).message })
  s = hydrateEntries(s, [user(1)])
  expect(s.leafId).toBe('a1')
  expect(s.nodes.map(n => n.id)).toEqual(['u1', 'a1'])
  // Replaying an ancestor must not rewind the newly advanced frontier.
  s = applyEvent(s, { type: 'message_end', entryId: 'u1', message: user(1).message })
  expect(s.leafId).toBe('a1')
})

test('a newer server model boundary retires missed tools even across a recovery gap', () => {
  let s = loadHistory({ entries: [user(1)], leafId: 'u1', running: true } as any)
  s = applyEvent(s, { type: 'tool_execution_start', toolCallId: 'lost', toolName: 'Read', timestamp: 1_000 })
  s = applyTail(s, { entries: [{ ...assistant(12), message: { ...assistant(12).message!, timestamp: 2_000 } }], leafId: 'a12', oldestId: 'a12', hasMore: true, running: true } as any)
  expect(s.nodes.some(n => n.kind === 'tool' && n.running)).toBe(false)
  expect(s.busy).toBe(true)
})


test('a terminal GET can advance past a queued run despite a delayed React event commit', () => {
  let s = loadHistory({ entries: [user(1), assistant(1)], leafId: 'a1', running: true } as any)
  const requestedRevision = s.liveRevision
  // The reader flushes its final event batch after it has issued the GET;
  // meanwhile the server can drain a queued prompt and finish its short run.
  s = applyEvent(s, { type: 'agent_end' })
  s = applyTail(s, { entries: [user(1), assistant(1), user(2), assistant(2)], leafId: 'a2', running: false } as any, requestedRevision)
  expect(s.leafId).toBe('a2')
  expect(s.nodes.map(n => n.id)).toEqual(['u1', 'a1', 'u2', 'a2'])
  expect(s.busy).toBe(false)
})
