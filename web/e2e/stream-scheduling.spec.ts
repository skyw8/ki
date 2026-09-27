import { expect, test } from '@playwright/test'
import { streamBatch, reconnectDelay, type FrameClock } from '../src/lib/stream-batch'
import { readSSE } from '../src/api/sse'
import { messageDecoder } from '../src/api/messageStream'
import { MarkdownDocument, SettledScanner } from '../src/features/markdown/streamText'
import type { LoopEvent } from '../src/api/types'

test('continuous input keeps its first deadline, catches up and flushes terminal barriers', () => {
  let now = 0, id = 0
  const frames = new Map<number, () => void>(), timers = new Map<number, () => void>()
  const clock: FrameClock = {
    now: () => now, frame: f => { frames.set(++id, f); return id }, cancelFrame: i => { frames.delete(i) },
    delay: f => { timers.set(++id, f); return id }, cancelDelay: i => { timers.delete(i) },
  }
  const received: LoopEvent[][] = []
  const batch = streamBatch(events => received.push(events), clock)
  const update = (seq: number): LoopEvent => ({ type: 'message_update', seq, runId: 'run', messageStream: 1, display: { runId: 'run', seq, receivedAt: now, pendingSince: now } })
  for (let i = 0; i < 10; i++) { now = i * 5; batch.enqueue(update(i)) }
  expect(frames.size).toBe(1)
  expect(timers.size).toBe(1)
  frames.values().next().value!()
  expect(received[0]).toHaveLength(1)
  expect(received[0][0].seq).toBe(9)
  expect(received[0][0].display?.pendingSince).toBe(0)
  batch.enqueue(update(10))
  batch.enqueue({ type: 'tool_execution_start', seq: 11 })
  batch.enqueue(update(12))
  batch.enqueue({ type: 'message_end', seq: 13 })
  expect(received[1].map(e => e.seq)).toEqual([10, 11, 12, 13])
  batch.enqueue(update(14))
  now += 120
  batch.enqueue(update(15))
  expect(received[2][0].seq).toBe(15)
  batch.enqueue(update(16))
  batch.dispose()
  expect(frames.size + timers.size).toBe(0)
  batch.enqueue(update(17))
  expect(received).toHaveLength(3)
  expect(reconnectDelay(100, 1)).toBe(10_000)
})

test('buffered SSE yields real tasks, preserves UTF-8/CRLF and patch order', async () => {
  const count = 1500
  const events: LoopEvent[] = [{ type: 'message_update', seq: 1, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text: '中🙂' }] } }]
  for (let i = 2; i <= count; i++) events.push({ type: 'message_update', seq: i, messageStream: 1, messagePatch: { baseSeq: i - 1, changes: [{ path: ['content', '0', 'text'], op: 'append', value: '中🙂' }] } })
  const bytes = new TextEncoder().encode(events.map(ev => `: ping\r\nevent: message_update\r\ndata: ${JSON.stringify(ev)}\r\n\r\n`).join(''))
  const body = new ReadableStream<Uint8Array>({ start(controller) {
    // Deliberately split a multibyte character and CRLF across reads.
    for (let i = 0; i < 127; i++) controller.enqueue(bytes.slice(i, i + 1))
    controller.enqueue(bytes.slice(127))
    controller.close()
  } })
  let taskRan = false, beforeTask = 0
  setTimeout(() => { taskRan = true }, 0)
  const decode = messageDecoder()
  let last: LoopEvent | undefined
  for await (const event of readSSE<LoopEvent>(body)) {
    if (!taskRan) beforeTask++
    last = decode(event)
  }
  expect(beforeTask).toBeLessThanOrEqual(128)
  expect(last?.message?.content?.[0].text).toBe('中🙂'.repeat(count))
})

test('malformed SSE fails before a later cursor and releases the reader', async () => {
  let canceled = false
  const body = new ReadableStream<Uint8Array>({ start(c) { c.enqueue(new TextEncoder().encode('data: {bad}\n\ndata: {"type":"agent_end"}\n\n')) }, cancel() { canceled = true } })
  const seen: LoopEvent[] = []
  await expect((async () => { for await (const event of readSSE<LoopEvent>(body)) seen.push(event) })()).rejects.toThrow()
  expect(seen).toEqual([])
  expect(canceled).toBe(true)
})

test('canceling an idle stream releases a pending read immediately', async () => {
  let canceled = false
  const body = new ReadableStream<Uint8Array>({ cancel() { canceled = true } })
  const controller = new AbortController()
  const result = (async () => { for await (const _ of readSSE<LoopEvent>(body, controller.signal)) { /* no input */ } })()
  controller.abort()
  await expect(result).rejects.toThrow()
  expect(canceled).toBe(true)
  expect(body.locked).toBe(false)
})

test('incremental markdown boundaries match a whole scan across syntax and Unicode chunks', () => {
  const source = '中🙂\n\n1. one\n\n2. two\n\nprose\n\n> quote\n\n> more\n\ntext\n\n```ts\ncode\n\nmore\n```\n\n<pre>\n\nnot a block\n</pre>\n\nend\n\n'
  const whole = new SettledScanner(), streamed = new SettledScanner()
  whole.push(source)
  for (const ch of source) streamed.push(ch)
  expect(streamed.boundaries).toEqual(whole.boundaries)
  expect(streamed.source).toBe(source)
  expect(streamed.scannedCharacters).toBe(source.length)
  const huge = new SettledScanner()
  for (let i = 0; i < 1000; i++) huge.push('x'.repeat(1000))
  expect(huge.scannedCharacters).toBe(1_000_000)
})

test('markdown retains stable segments, normalizes split CRLF, resets rewrites and resolves document references', () => {
  const document = new MarkdownDocument()
  const prefix = ('A **paragraph**. '.repeat(16) + '\r\n\r\n').repeat(30)
  const first = document.update(prefix)
  const second = document.update(prefix + 'tail\r')
  expect(second.segments).toBe(first.segments)
  const third = document.update(prefix + 'tail\r\nrest', true)
  expect(third.segments.join('') + third.tail).toBe((prefix + 'tail\r\nrest').replaceAll('\r\n', '\n'))
  const rewritten = document.update('[label][ref]\n\n' + prefix)
  expect(rewritten.segments.length).toBeGreaterThan(0)
  const references = document.update('[label][ref]\n\n' + prefix + '[ref]: https://example.test', true)
  expect(references.references).toBe(true)
  expect(references.segments).toEqual([])
  expect(references.tail).toContain('[label][ref]')
  expect(document.update('replacement', true).tail).toBe('replacement')
})
