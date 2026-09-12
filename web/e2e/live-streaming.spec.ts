import { expect, test } from '@playwright/test'
import { applyEvent, emptyView } from '../src/lib/model.ts'
import type { LoopEvent, ViewState } from '../src/api/types.ts'

// Regression: the assistant message after a tool call used to be mistaken for a
// replayed history message, so its message_update deltas were dropped and the
// reply only appeared whole at message_end. These specs drive applyEvent with
// the same event sequence the server emits.

const text = (value: string) => ({ type: 'text', text: value })
const toolCall = (id: string, name: string) => ({ type: 'toolCall', id, name, arguments: {} })
const userMsg = (value: string, ts = 0) => ({ role: 'user', content: [text(value)], timestamp: ts })
const asstMsg = (content: unknown[], extra: Record<string, unknown> = {}) => ({ role: 'assistant', content, ...extra })

const ev = (type: string, rest: Record<string, unknown> = {}): LoopEvent => ({ type, ...rest }) as unknown as LoopEvent

function assistantNode(s: ViewState, id: string) {
  return s.nodes.find(n => n.kind === 'assistant' && n.id === id)
}

test('assistant text after a tool call streams before message_end', () => {
  let s = emptyView()
  s = applyEvent(s, ev('agent_start'))
  s = applyEvent(s, ev('turn_start'))
  s = applyEvent(s, ev('message_start', { message: userMsg('hi') }))
  s = applyEvent(s, ev('message_end', { message: userMsg('hi') }))

  // First assistant turn only calls a tool (no visible text).
  s = applyEvent(s, ev('message_start', { message: asstMsg([], { timestamp: 1 }) }))
  s = applyEvent(s, ev('message_end', { message: asstMsg([toolCall('call-1', 'Read')], { timestamp: 1, stopReason: 'toolUse' }), entryId: 'e1' }))
  s = applyEvent(s, ev('tool_execution_start', { toolCallId: 'call-1', toolName: 'Read' }))
  s = applyEvent(s, ev('tool_execution_end', { toolCallId: 'call-1', toolName: 'Read', result: { content: [text('file body')] } }))
  s = applyEvent(s, ev('turn_end', {}))

  // Second assistant turn is the final answer; it must stream.
  s = applyEvent(s, ev('turn_start'))
  s = applyEvent(s, ev('message_start', { message: asstMsg([], { timestamp: 2 }) }))

  let streamed = s.nodes.filter(n => n.kind === 'assistant' && n.streaming)
  expect(streamed).toHaveLength(1)

  s = applyEvent(s, ev('message_update', {
    message: asstMsg([text('Hel')], { timestamp: 2 }),
    assistantMessageEvent: { type: 'text_delta', delta: 'Hel', partial: asstMsg([text('Hel')], { timestamp: 2 }) },
  }))
  s = applyEvent(s, ev('message_update', {
    message: asstMsg([text('Hello')], { timestamp: 2 }),
    assistantMessageEvent: { type: 'text_delta', delta: 'lo', partial: asstMsg([text('Hello')], { timestamp: 2 }) },
  }))

  streamed = s.nodes.filter(n => n.kind === 'assistant' && n.streaming)
  expect(streamed).toHaveLength(1)
  expect((streamed[0] as { text: string }).text).toBe('Hello')

  s = applyEvent(s, ev('message_end', { message: asstMsg([text('Hello')], { timestamp: 2, stopReason: 'stop' }), entryId: 'e2' }))
  const final = assistantNode(s, 'e2') as { text: string; streaming?: boolean } | undefined
  expect(final?.text).toBe('Hello')
  expect(final?.streaming).toBeFalsy()
  // The streamed node adopts the entry id; no duplicate whole-message node.
  expect(s.nodes.filter(n => n.kind === 'assistant' && n.text === 'Hello')).toHaveLength(1)
})

test('reconnect replay dedupes persisted messages and still streams the live one', () => {
  // The view already shows the current run's user and completed tool turn.
  let s: ViewState = {
    ...emptyView(),
    nodes: [
      { kind: 'user', id: 'e0', text: 'hi' },
      { kind: 'assistant', id: 'e1', text: '', usage: null },
      { kind: 'tool', id: 'call-1', name: 'Read', args: {} },
    ],
  }

  // The server replays the run from agent_start.
  s = applyEvent(s, ev('agent_start'))
  s = applyEvent(s, ev('message_start', { message: userMsg('hi') }))
  s = applyEvent(s, ev('message_end', { message: userMsg('hi'), entryId: 'e0' }))
  s = applyEvent(s, ev('message_start', { message: asstMsg([], { timestamp: 1 }) }))
  s = applyEvent(s, ev('message_end', { message: asstMsg([toolCall('call-1', 'Read')], { timestamp: 1, stopReason: 'toolUse' }), entryId: 'e1' }))

  expect(s.nodes.filter(n => n.id === 'e1')).toHaveLength(1)
  expect(s.nodes.filter(n => n.kind === 'assistant')).toHaveLength(1)

  // The live final answer must still stream.
  s = applyEvent(s, ev('message_start', { message: asstMsg([], { timestamp: 2 }) }))
  s = applyEvent(s, ev('message_update', {
    message: asstMsg([text('Done')], { timestamp: 2 }),
    assistantMessageEvent: { type: 'text_delta', delta: 'Done', partial: asstMsg([text('Done')], { timestamp: 2 }) },
  }))
  const streamed = s.nodes.filter(n => n.kind === 'assistant' && n.streaming)
  expect(streamed).toHaveLength(1)
  expect((streamed[0] as { text: string }).text).toBe('Done')
})

test('a sub-millisecond tool duration (0ms) is kept on the tool node', () => {
  let s = emptyView()
  s = applyEvent(s, ev('message_end', { message: asstMsg([toolCall('call-1', 'Read')], { timestamp: 1, stopReason: 'toolUse' }), entryId: 'e1' }))
  s = applyEvent(s, ev('tool_execution_end', {
    toolCallId: 'call-1',
    toolName: 'Read',
    durationMs: 0,
    result: { content: [text('file body')] },
  }))
  const node = s.nodes.find(n => n.kind === 'tool' && n.id === 'call-1')
  // 0 is a real measurement, not "absent": the row must still render a timer.
  expect(node).toMatchObject({ durationMs: 0, running: false })
})
