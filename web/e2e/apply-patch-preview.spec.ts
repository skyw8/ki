import { expect, test } from '@playwright/test'
import { applyEvent, emptyView, loadHistory } from '../src/model.ts'

test('apply_patch preview is created before execution and replaced by committed details', () => {
  const preview = { changes: [{ path: 'a.txt', kind: 'update', unified_diff: '@@\n-old\n+new\n' }] }
  const live = applyEvent(emptyView(), {
    type: 'patch_apply_updated',
    toolCallId: 'call-1',
    toolName: 'apply_patch',
    partialResult: preview,
  })
  const node = live.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')
  expect(node).toMatchObject({ name: 'apply_patch', running: true, details: preview })

  const rejected = applyEvent(live, { type: 'tool_execution_end', toolCallId: 'call-1', toolName: 'apply_patch', isError: true })
  expect(rejected.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({ running: false, isError: true, details: { status: 'failed', exact: true, changes: [] } })

  const committed = { status: 'completed', exact: true, changes: [{ path: 'a.txt', kind: 'update', unified_diff: '@@\n-old\n+NEW\n' }] }
  const done = applyEvent(live, {
    type: 'tool_execution_end',
    toolCallId: 'call-1',
    toolName: 'apply_patch',
    result: { content: [{ type: 'text', text: 'Success' }], details: committed },
  })
  expect(done.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({ running: false, result: 'Success', details: committed })
})

test('tool execution timing is applied to live tool nodes and trajectory records', () => {
  const started = applyEvent(emptyView(), {
    type: 'tool_execution_start',
    timestamp: 1_000,
    toolCallId: 'call-1',
    toolName: 'Read',
    args: { file_path: '/tmp/a.txt' },
  })
  const done = applyEvent(started, {
    type: 'tool_execution_end',
    timestamp: 1_042,
    durationMs: 42,
    toolCallId: 'call-1',
    toolName: 'Read',
    result: { content: [{ type: 'text', text: 'ok' }] },
  })
  expect(done.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({ running: false, durationMs: 42 })
  expect(done.records.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({ running: false, startedAt: 1_000, durationMs: 42 })
})

test('history reconstructs tool start time from result timing', () => {
  const view = loadHistory({
    id: 'session-1',
    cwd: '/tmp',
    provider: 'test',
    model: 'model',
    title: 'test',
    leafId: 'result-1',
    entries: [
      {
        type: 'message',
        id: 'assistant-1',
        message: { role: 'assistant', timestamp: 1_000, content: [{ type: 'toolCall', id: 'call-1', name: 'Read', arguments: {} }] },
      },
      {
        type: 'message',
        id: 'result-1',
        parentId: 'assistant-1',
        message: { role: 'toolResult', toolCallId: 'call-1', toolName: 'Read', timestamp: 1_042, durationMs: 42, content: [{ type: 'text', text: 'ok' }] },
      },
    ],
  })
  expect(view.records.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({ startedAt: 1_000, durationMs: 42, running: false })
})
