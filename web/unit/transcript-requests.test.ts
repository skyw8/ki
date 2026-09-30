import React from 'react'
import { expect, test } from 'bun:test'
import type { Dispatch, SetStateAction } from 'react'
import type { Client } from '../src/api/client'
import type { CompactTurn, Entry, SessionDetail, ViewState } from '../src/api/types'
import { useTranscriptRequests } from '../src/features/chat/useTranscriptRequests'
import { applyEvent, loadHistory, turnStats } from '../src/lib/model'

test('a conversion rejected by a deferred updater retries without publishing an exhausted cursor', async () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', message: { role: 'user', content: [{ type: 'text', text: 'input' }] } },
    ...[1, 2, 3].map(n => ({ type: 'message', id: `a${n}`, parentId: n === 1 ? 'u1' : `a${n - 1}`,
      message: { role: 'assistant', content: [{ type: 'text', text: `reply ${n}` }] } })),
  ]
  const view = { current: loadHistory({ id: 's', entries, oldestId: 'a2', hasMore: true, leafId: 'a3', running: true }) }
  const stats = [...turnStats(view.current.nodes).values()][0]
  const summary: CompactTurn = {
    id: 'u1', tailId: 'a3', entryIds: ['u1', 'a3'], visibleNodeIds: ['u1', 'a3'],
    hiddenCount: 2, entryCount: 4, stepCount: 3, stats,
  }
  const page: SessionDetail = { id: 's', entries: [entries[0], entries[3]],
    compactTurns: [summary], oldestId: 'u1', hasMore: false }
  const calls: string[] = []
  let resolveRetry!: (page: SessionDetail) => void
  const retry = new Promise<SessionDetail>(resolve => { resolveRetry = resolve })
  const api = { get: (_id: string, opts: { turn: string }) => {
    calls.push(opts.turn)
    return calls.length === 1 ? Promise.resolve(page) : retry
  } } as unknown as Client
  const updates: SetStateAction<ViewState>[] = []
  const effects: Array<() => void | (() => void)> = []
  const cleanups: Array<() => void> = []
  const setView: Dispatch<SetStateAction<ViewState>> = update => { updates.push(update) }
  // A controllable dispatcher isolates the response-to-React-commit race:
  // real timers or browser rendering cannot guarantee this interleaving.
  const internals = (React as unknown as {
    __SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED: { ReactCurrentDispatcher: { current: unknown } }
  }).__SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED
  const previousDispatcher = internals.ReactCurrentDispatcher.current
  const oldWindow = globalThis.window
  let history!: ReturnType<typeof useTranscriptRequests>
  try {
    internals.ReactCurrentDispatcher.current = {
      useMemo: (factory: () => unknown) => factory(),
      useRef: (value: unknown) => ({ current: value }),
      useCallback: (callback: unknown) => callback,
      useState: (value: unknown) => [value, () => {}],
      useEffect: (effect: () => void | (() => void)) => effects.push(effect),
      useLayoutEffect: (effect: () => void | (() => void)) => effects.push(effect),
    }
    history = useTranscriptRequests(api, 's', 1, view, setView, { mode: 'compact', keep: 1 }, async () => {})
  } finally {
    internals.ReactCurrentDispatcher.current = previousDispatcher
  }
  globalThis.window = { clearTimeout, setTimeout } as unknown as Window & typeof globalThis
  const flush = () => {
    const update = updates.shift()
    if (!update) throw new Error('Expected queued view update')
    view.current = typeof update === 'function' ? update(view.current) : update
  }
  const settle = async () => { for (let i = 0; i < 10; i++) await Promise.resolve() }
  try {
    for (const effect of effects) {
      const cleanup = effect()
      if (cleanup) cleanups.push(cleanup)
    }
    await settle()
    expect(calls).toEqual(['a2'])
    expect(updates).toHaveLength(1)
    view.current = applyEvent(view.current, { type: 'message_end', entryId: 'a4', parentId: 'a3',
      message: { role: 'assistant', content: [{ type: 'text', text: 'latest' }] } })
    flush()
    expect(view.current.oldestId).toBe('a2')
    expect(view.current.hasMore).toBe(true)
    await settle()
    expect(calls).toEqual(['a2', 'a2'])
    const latest = view.current.entries.find(entry => entry.id === 'a4')!
    resolveRetry({ ...page, entries: [entries[0], latest], compactTurns: [{
      ...summary, tailId: 'a4', entryIds: ['u1', 'a4'], visibleNodeIds: ['u1', 'a4'], entryCount: 5, hiddenCount: 3,
    }] })
    await settle()
    flush()
    await settle()
    expect(view.current.leafId).toBe('a4')
    expect(view.current.oldestId).toBe('u1')
    expect(view.current.hasMore).toBe(false)
    expect(view.current.compactTurns?.[0].tailId).toBe('a4')
  } finally {
    history.cancel()
    cleanups.forEach(cleanup => cleanup())
    globalThis.window = oldWindow
  }
})
