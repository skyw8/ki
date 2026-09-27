import { expect, test } from '@playwright/test'
import { messageDecoder } from '../src/api/messageStream'

test('wire patches preserve snapshots, handle Unicode, blocks, replacements and sequence gaps', () => {
  const decode = messageDecoder()
  const first = decode({ type: 'message_update', seq: 3, messageStream: 3, message: { role: 'assistant', content: [{ type: 'thinking', thinking: '中文🙂' }] } })
  const next = decode({ type: 'message_update', seq: 8, messageStream: 3, messagePatch: { baseSeq: 3, changes: [
    { path: ['content', '0', 'thinking'], op: 'append', value: '追加' },
    { path: ['content'], op: 'resize', value: 2 },
    { path: ['content', '1'], op: 'set', value: { type: 'toolCall', arguments: { command: 'echo' } } },
  ] } })
  expect(first.message?.content?.[0].thinking).toBe('中文🙂')
  expect(next.message?.content).toHaveLength(2)
  decode({ type: 'tool_execution_start', seq: 9 })
  const last = decode({ type: 'message_update', seq: 12, messageStream: 3, messagePatch: { baseSeq: 8, changes: [
    { path: ['content', '1', 'arguments', 'command'], op: 'append', value: ' hi' },
    { path: ['content', '0'], op: 'set', value: { type: 'text', text: 'done' } },
  ] } })
  expect(last.message?.content?.[1].arguments).toEqual({ command: 'echo hi' })
  expect(next.message?.content?.[1].arguments).toEqual({ command: 'echo' })
  expect(() => decode({ type: 'message_update', seq: 13, messageStream: 3, messagePatch: { baseSeq: 8, changes: [] } })).toThrow('base mismatch')
  decode({ type: 'message_end', seq: 14, message: last.message })
  expect(() => decode({ type: 'message_update', seq: 15, messageStream: 3, messagePatch: { baseSeq: 12, changes: [] } })).toThrow('base mismatch')
  expect(decode({ type: 'message_update', seq: 19, messageStream: 19, message: { role: 'assistant', content: [] } }).message?.content).toEqual([])
})
