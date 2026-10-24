import { yieldToMain } from '../lib/stream-batch'

export const SSE_IDLE_TIMEOUT_MS = 45_000

export type ReadSSEOptions = {
  idleTimeoutMs?: number
}

export async function* readSSE<T extends { type: string }>(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
  opts: ReadSSEOptions = {},
): AsyncGenerator<T> {
  const idleTimeoutMs = opts.idleTimeoutMs ?? SSE_IDLE_TIMEOUT_MS
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

  let lastActivity = performance.now()
  let lastActivityWall = Date.now()
  let stalled = false
  const hasDocument = typeof document !== 'undefined'
  const noteActivity = () => { lastActivity = performance.now(); lastActivityWall = Date.now() }
  const markStalled = () => {
    if (stalled) return
    stalled = true
    void reader.cancel().catch(() => {})
  }
  const watchdog = idleTimeoutMs > 0
    ? setInterval(() => {
        if (stalled) return
        if (hasDocument && document.visibilityState === 'hidden') return
        if (performance.now() - lastActivity >= idleTimeoutMs) markStalled()
      }, Math.max(50, Math.floor(idleTimeoutMs / 3)))
    : undefined
  let hiddenSince: number | null = hasDocument && document.visibilityState === 'hidden' ? Date.now() : null
  const onVisibility = () => {
    if (stalled) return
    if (hasDocument && document.visibilityState === 'hidden') {
      if (hiddenSince === null) hiddenSince = Date.now()
      return
    }
    const since = hiddenSince
    hiddenSince = null
    const resumeGrace = Math.min(2000, idleTimeoutMs)
    if (since !== null && Date.now() - since >= resumeGrace && lastActivityWall <= since) {
      markStalled()
      return
    }
    if (Date.now() - lastActivityWall >= idleTimeoutMs) markStalled()
  }
  const watching = watchdog !== undefined && hasDocument
  if (watching) document.addEventListener('visibilitychange', onVisibility)

  const flush = (): T | null => {
    const name = event
    event = ''
    if (!data.length) return null
    const raw = data.join('\n')
    data = []
    const value = JSON.parse(raw) as T
    if (!value.type && name) value.type = name
    return value
  }
  try {
    while (true) {
      signal?.throwIfAborted()
      const { value, done } = await reader.read()
      signal?.throwIfAborted()
      if (value !== undefined) noteActivity()
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
    if (watchdog !== undefined) clearInterval(watchdog)
    if (watching) document.removeEventListener('visibilitychange', onVisibility)
    signal?.removeEventListener('abort', abort)
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
