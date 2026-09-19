import { expect, test } from '@playwright/test'
import { applyIndex, applyTail, hydrateEntries, loadHistory, turnStats } from '../src/lib/model.ts'
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
