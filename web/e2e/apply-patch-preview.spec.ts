import { expect, test } from '@playwright/test'
import { applyEvent, emptyView } from '../src/lib/model.ts'

test('apply_patch preview is replaced by committed details', () => {
  const preview = { changes: [{ path: 'a.txt', kind: 'update', unified_diff: '@@\n-old\n+new\n' }] }
  const live = applyEvent(emptyView(), {
    type: 'patch_apply_updated',
    toolCallId: 'call-1',
    toolName: 'apply_patch',
    partialResult: preview,
  })
  expect(live.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({
    name: 'apply_patch',
    running: true,
    details: preview,
  })

  const committed = { status: 'completed', exact: true, changes: [{ path: 'a.txt', kind: 'update', unified_diff: '@@\n-old\n+NEW\n' }] }
  const done = applyEvent(live, {
    type: 'tool_execution_end',
    toolCallId: 'call-1',
    toolName: 'apply_patch',
    result: { content: [{ type: 'text', text: 'Success' }], details: committed },
  })
  expect(done.nodes.find(item => item.kind === 'tool' && item.id === 'call-1')).toMatchObject({
    running: false,
    result: 'Success',
    details: committed,
  })
})
