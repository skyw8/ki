import { expect, test } from '@playwright/test'
import { applyEvent, emptyView } from '../src/model.ts'

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
