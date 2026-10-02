import { expect, test } from 'bun:test'
import type { ChatNode, Entry, IndexEntry, LoopEvent, Message, ViewState } from '../src/api/types'
import { applyEvent, applyIndex, applyTail, hydrateEntries, loadHistory } from '../src/lib/model'

const text = (value: string) => [{ type: 'text', text: value }]

function message(id: string, parentId: string, value: Message): Entry {
  return { type: 'message', id, parentId, timestamp: new Date(value.timestamp!).toISOString(), message: value }
}

function audit(id: string, parentId: string, type: string, details: Omit<LoopEvent, 'type'>): Entry {
  return { id, parentId, type, timestamp: new Date(details.timestamp!).toISOString(), details }
}

const user = message('u', '', { role: 'user', timestamp: 1000, content: text('inspect files') })
const firstRequest: Entry = { type: 'request_header', id: 'r1', parentId: 'u', timestamp: new Date(1100).toISOString(), system: 'Code Mode' }
const exec = message('exec-call', 'r1', {
  role: 'assistant', timestamp: 1200,
  content: [{ type: 'toolCall', id: 'exec-1', name: 'exec', toolType: 'custom', input: 'await tools.read({file_path: "go.mod"})' }],
})
const metadata = { toolCallId: 'nested-1', parentCallId: 'exec-1', cellId: 'cell-1', toolName: 'read', requestedToolName: 'Read', args: { file_path: 'go.mod' } }
const start = audit('nested-start', 'exec-call', 'tool_execution_start', { ...metadata, timestamp: 1250 })
const update = audit('nested-update', 'nested-start', 'tool_execution_update', { ...metadata, timestamp: 1270, partialResult: { phase: 'reading' } })
const yielded = message('exec-result', 'nested-update', {
  role: 'toolResult', toolCallId: 'exec-1', toolName: 'exec', timestamp: 1300, durationMs: 100,
  content: text('Script running with cell ID cell-1'),
})
const secondRequest: Entry = { type: 'request_header', id: 'r2', parentId: 'exec-result', timestamp: new Date(1400).toISOString(), system: 'Code Mode' }
const wait = message('wait-call', 'r2', {
  role: 'assistant', timestamp: 1500,
  content: [{ type: 'toolCall', id: 'wait-1', name: 'wait', arguments: { cell_id: 'cell-1' } }],
})
const end = audit('nested-end', 'wait-call', 'tool_execution_end', {
  ...metadata, timestamp: 1550, durationMs: 300, isError: false,
  result: { content: text('module ki'), details: { bytes: 9 } },
})
const notify = audit('notify', 'nested-end', 'tool_execution_update', {
  toolCallId: 'exec-1', parentCallId: 'exec-1', cellId: 'cell-1', toolName: 'exec', timestamp: 1570,
  partialResult: { notification: 'read complete' },
})
const waited = message('wait-result', 'notify', {
  role: 'toolResult', toolCallId: 'wait-1', toolName: 'wait', timestamp: 1600, durationMs: 100,
  content: text('done'),
})
const base = [user, firstRequest, exec]
const continuation = [start, update, yielded, secondRequest, wait, end, notify, waited]
const history = [...base, ...continuation]

function event(entry: Entry): LoopEvent {
  return entry.message
    ? { type: 'message_end', entryId: entry.id, parentId: entry.parentId, message: entry.message }
    : { ...entry.details as LoopEvent, type: entry.type, entryId: entry.id, parentId: entry.parentId, timestamp: Date.parse(entry.timestamp!), system: entry.system }
}

function live(): ViewState {
  let state = loadHistory({ id: 's', entries: base, leafId: exec.id, running: true })
  for (const entry of continuation) state = applyEvent(state, event(entry))
  return applyEvent(state, { type: 'agent_end' })
}

function tool(state: ViewState, id = 'nested-1'): Extract<ChatNode, { kind: 'tool' }> {
  const node = state.nodes.find(n => n.kind === 'tool' && n.id === id)
  expect(node?.kind).toBe('tool')
  return node as Extract<ChatNode, { kind: 'tool' }>
}

function cards(state: ViewState) {
  return state.nodes.filter(n => n.kind === 'tool').map(n => ({
    id: n.id, name: n.name, args: n.args, result: n.result, details: n.details,
    parentCallId: n.parentCallId, cellId: n.cellId, requestedToolName: n.requestedToolName,
    running: n.running, durationMs: n.durationMs, startedAt: n.startedAt, isError: n.isError, turnId: n.turnId,
  }))
}

test('nested Code Mode audit has identical live and reloaded cards without provider toolResults', () => {
  const state = live()
  const reloaded = loadHistory({ id: 's', entries: history, leafId: waited.id, running: false })
  expect(cards(state)).toEqual(cards(reloaded))
  expect(tool(reloaded)).toMatchObject({
    id: 'nested-1', parentCallId: 'exec-1', cellId: 'cell-1', requestedToolName: 'Read',
    args: { file_path: 'go.mod' }, result: 'module ki', details: { bytes: 9 },
    running: false, durationMs: 300, startedAt: 1250, isError: false, turnId: 'u',
  })
  expect(state.entries.filter(e => e.message?.role === 'toolResult').map(e => e.message?.toolCallId)).toEqual(['exec-1', 'wait-1'])
  expect(state.entries.filter(e => e.type.startsWith('tool_execution_')).map(e => e.id)).toEqual(['nested-start', 'nested-update', 'nested-end', 'notify'])
  expect(reloaded.records.filter(r => r.id === 'nested-1')).toHaveLength(1)
  expect(reloaded.records.find(r => r.id === 'nested-1')).toMatchObject({
    parentCallId: 'exec-1', cellId: 'cell-1', requestId: 'r1', step: 1, output: 'module ki', outputBlocks: text('module ki'),
  })
})

test('metadata audits deduplicate start/update/end and exclude sibling branches without fabricating bodies', () => {
  const nested = { toolCallId: 'nested-1', name: 'read', parentCallId: 'exec-1', cellId: 'cell-1', requestedToolName: 'Read' }
  const index: IndexEntry[] = [
    { type: 'message', id: 'u', role: 'user' },
    { type: 'tool_execution_start', id: 'start', parentId: 'u', ...nested },
    { type: 'tool_execution_update', id: 'update', parentId: 'start', ...nested },
    { type: 'tool_execution_end', id: 'end', parentId: 'update', ...nested, isError: true, durationMs: 9 },
    { type: 'tool_execution_end', id: 'sibling', parentId: 'u', ...nested, toolCallId: 'sibling-call', name: 'write' },
    { type: 'message', id: 'tail', parentId: 'end', role: 'assistant' },
  ]
  let state = loadHistory({ id: 's', leafId: 'tail', entries: [{ type: 'message', id: 'tail', parentId: 'end', message: { role: 'assistant', content: text('done') } }] })
  state = applyIndex(state, { id: 's', leafId: 'tail', index })
  const records = state.records.filter(record => record.kind === 'tool')
  expect(records).toHaveLength(1)
  expect(records[0]).toMatchObject({ id: 'nested-1', name: 'read', parentCallId: 'exec-1', cellId: 'cell-1', requestedToolName: 'Read', error: true, durationMs: 9 })
  expect(records[0]?.input).toBeUndefined()
  expect(records[0]?.outputBlocks).toBeUndefined()
  expect(records[0]?.output).toBe('')
  expect(state.nodes.filter(node => node.kind === 'tool')).toHaveLength(0)
})

test('yielded callbacks stay running across a later request and keep their original request ownership', () => {
  let state = loadHistory({ id: 's', entries: base, leafId: exec.id, running: true })
  for (const entry of [start, update, yielded, secondRequest, wait]) state = applyEvent(state, event(entry))
  expect(tool(state)).toMatchObject({ running: true, details: { phase: 'reading' } })
  expect(state.currentRequestId).toBe('r2')
  expect(state.records.find(r => r.id === 'nested-1')).toMatchObject({ requestId: 'r1', step: 1 })
  state = applyEvent(state, event(end))
  expect(tool(state).running).toBe(false)
  expect(state.records.find(r => r.id === 'nested-1')).toMatchObject({ requestId: 'r1', step: 1 })
})

test('durable audit does not erase a live request whose persisted header is not loaded yet', () => {
  let state = loadHistory({ id: 's', entries: [user], leafId: user.id, running: true })
  state = applyEvent(state, { type: 'request_header', system: 'Code Mode', timestamp: 1100 })
  state = applyEvent(state, event(exec))
  const requestId = state.currentRequestId
  state = applyEvent(state, event(start))
  expect(state.currentRequestId).toBe(requestId)
  expect(state.records.find(r => r.id === 'nested-1')).toMatchObject({ requestId, step: 1 })
  state = applyEvent(state, { type: 'request_header', system: 'Code Mode', timestamp: 1400 })
  expect(state.currentRequestId).not.toBe(requestId)
  state = applyEvent(state, event(end))
  expect(state.records.find(r => r.id === 'nested-1')).toMatchObject({ requestId, step: 1, running: false })
})

test('notify only updates the enclosing exec and never creates a pending nested card', () => {
  const state = live()
  expect(state.nodes.filter(n => n.kind === 'tool').map(n => n.id)).toEqual(['exec-1', 'nested-1', 'wait-1'])
  expect(tool(state, 'exec-1')).toMatchObject({
    result: 'Script running with cell ID cell-1', running: false, details: { notification: 'read complete' },
  })
  const orphan = loadHistory({ id: 's', entries: [user, { ...notify, parentId: user.id }], leafId: notify.id })
  expect(orphan.nodes.filter(n => n.kind === 'tool')).toHaveLength(0)
  expect(orphan.records.filter(r => r.kind === 'tool')).toHaveLength(0)
})

test('replayed audit frames cannot duplicate entries or resurrect completed callbacks', () => {
  const expected = loadHistory({ id: 's', entries: history, leafId: waited.id })
  let state = expected
  for (let i = 0; i < 3; i++) {
    for (const entry of [start, update, end, notify]) state = applyEvent(state, event(entry))
    state = applyIndex(state, { id: 's', index: [] })
    state = hydrateEntries(state, history)
  }
  expect(cards(state)).toEqual(cards(expected))
  expect(state.entries).toHaveLength(history.length)
  expect(state.records.filter(r => r.id === 'nested-1')).toHaveLength(1)
  expect(state.nodes.some(n => n.kind === 'tool' && n.running)).toBe(false)
})

test('audit recovery at each frontier commutes with subsequent SSE and authoritative tail', () => {
  const expected = loadHistory({ id: 's', entries: history, leafId: waited.id })
  for (let frontier = 0; frontier <= continuation.length; frontier++) {
    let state = loadHistory({ id: 's', entries: base, leafId: exec.id, running: true })
    for (let i = 0; i < continuation.length; i++) {
      if (i === frontier) state = applyTail(state, { id: 's', entries: history, leafId: waited.id, running: false })
      state = applyEvent(state, event(continuation[i]))
    }
    state = applyTail(state, { id: 's', entries: history, leafId: waited.id, running: false })
    expect(cards(state)).toEqual(cards(expected))
    expect(state.records.filter(r => r.id === 'nested-1')).toHaveLength(1)
    expect(state.entries.some(e => e.message?.toolCallId === 'nested-1')).toBe(false)
  }
})

test('an end-only audit page reconstructs a completed error card without a synthetic call message', () => {
  const failure = {
    ...end, parentId: exec.id,
    details: { ...end.details as LoopEvent, isError: true, result: { Content: text('denied'), Details: { reason: 'policy' } } },
  }
  const state = loadHistory({ id: 's', entries: [...base, failure], leafId: failure.id })
  expect(tool(state)).toMatchObject({ running: false, isError: true, result: 'denied', startedAt: 1250, details: { reason: 'policy' } })
  expect(state.records.filter(r => r.id === 'nested-1')).toHaveLength(1)
  expect(state.entries).toHaveLength(base.length + 1)
})

test('unpersisted nested events still share the projection and settle at agent end', () => {
  let state = loadHistory({ id: 's', entries: base, leafId: exec.id, running: true })
  for (const entry of [start, update]) {
    const frame = event(entry)
    delete frame.entryId
    state = applyEvent(state, frame)
  }
  expect(tool(state)).toMatchObject({ running: true, parentCallId: 'exec-1', details: { phase: 'reading' } })
  state = applyIndex(state, { id: 's', index: [] })
  expect(tool(state).running).toBe(true)
  state = applyEvent(state, { type: 'agent_end' })
  expect(tool(state).running).toBe(false)
  expect(state.entries).toHaveLength(base.length)
})
