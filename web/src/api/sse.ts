import { yieldToMain } from '../lib/stream-batch'

/** Incremental line decoding, with a task budget even when a whole replay is
 * already buffered. Async-generator yields alone can starve frames in a chain
 * of resolved promises. UTF-8 and CRLF may both straddle network chunks.
 */
export async function* readSSE<T extends { type: string }>(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<T> {
  const reader = body.getReader()
  const abort = () => { void reader.cancel(signal?.reason).catch(() => {}) }
  signal?.addEventListener('abort', abort, { once: true })
  const decoder = new TextDecoder()
  let buffer = ''
  let searched = 0
  let event = ''
  let data: string[] = []
  let sliceAt = performance.now()
  let count = 0
  const flush = (): T | null => {
    const name = event
    event = ''
    if (!data.length) return null
    const raw = data.join('\n')
    data = []
    // A malformed message must fail the connection: silently skipping it can
    // advance the resume cursor past a lifecycle event the view never saw.
    const value = JSON.parse(raw) as T
    if (!value.type && name) value.type = name
    return value
  }
  try {
    while (true) {
      signal?.throwIfAborted()
      const { value, done } = await reader.read()
      signal?.throwIfAborted()
      buffer += decoder.decode(value, { stream: !done })
      let consumed = 0
      while (true) {
        const nl = buffer.indexOf('\n', searched)
        if (nl === -1) { searched = buffer.length; break }
        const line = buffer.slice(consumed, nl).replace(/\r$/, '')
        consumed = searched = nl + 1
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
        else if (line === '') {
          const value = flush()
          if (value) { yield value; count++ }
        }
        signal?.throwIfAborted()
        if (count >= 128 || performance.now() - sliceAt >= 4) {
          await yieldToMain(signal)
          count = 0
          sliceAt = performance.now()
        }
      }
      buffer = buffer.slice(consumed)
      searched -= consumed
      if (done) {
        if (buffer.startsWith('data:')) data.push(buffer.slice(5).replace(/^ /, '').replace(/\r$/, ''))
        const value = flush()
        if (value) yield value
        return
      }
    }
  } finally {
    signal?.removeEventListener('abort', abort)
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
