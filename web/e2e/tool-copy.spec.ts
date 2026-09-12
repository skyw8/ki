import { expect, test } from '@playwright/test'
import { toolCopyText } from '../src/tool-copy.ts'
import type { ChatNode } from '../src/types.ts'

// Regression: the row-level copy button used to ship only the collapsed preview
// (or the tool description) instead of the tool's input and output.

type ToolNode = Extract<ChatNode, { kind: 'tool' }>

const tool = (node: Partial<ToolNode> & { name: string }): ToolNode => ({
  kind: 'tool',
  id: 'call-1',
  ...node,
} as ToolNode)

test('Read copies the call arguments and the file body', () => {
  const text = toolCopyText(tool({
    name: 'Read',
    args: { file_path: 'a.ts' },
    result: 'export const x = 1',
  }), 'a.ts')
  expect(text).toBe(['IN', '{\n  "file_path": "a.ts"\n}', '', 'OUT', 'export const x = 1'].join('\n'))
})

test('Bash copies the command and its output', () => {
  const text = toolCopyText(tool({
    name: 'Bash',
    args: { command: 'ls -la' },
    result: 'total 0',
  }), 'ls -la')
  expect(text).toContain('IN\nls -la')
  expect(text).toContain('OUT\ntotal 0')
})

test('Edit copies the diff as output, not the preview', () => {
  const text = toolCopyText(tool({
    name: 'Edit',
    args: { file_path: 'a.ts', old_string: 'x', new_string: 'y' },
    details: { diff: '-x\n+y' },
    result: 'ok',
  }), 'a.ts')
  expect(text).toContain('OUT\n-x\n+y')
  expect(text).not.toContain('ok')
})

test('apply_patch copies the unified diff', () => {
  const text = toolCopyText(tool({
    name: 'apply_patch',
    args: { patch: '*** Begin Patch' },
    details: { changes: [{ unified_diff: '@@\n-a\n+b' }] },
    result: 'applied',
  }), 'apply_patch')
  expect(text).toContain('OUT\n@@\n-a\n+b')
})

test('a running tool with no payload falls back to its summary', () => {
  const text = toolCopyText(tool({
    name: 'Read',
    running: true,
  }), 'a.ts')
  expect(text).toBe('a.ts')
})
