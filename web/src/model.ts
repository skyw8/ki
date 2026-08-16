import type { ChatNode, Entry, LoopEvent, Message, SessionDetail, TrajRecord, ViewState } from './types'

export function emptyView(): ViewState {
  return {
    nodes: [],
    records: [],
    busy: false,
    error: null,
    model: '',
    provider: '',
    cwd: '',
    title: '',
    turn: 0,
  }
}

export function messageText(m?: Message | null): string {
  if (!m?.content) return ''
  return m.content
    .filter(c => c.type === 'text' || c.type === '')
    .map(c => c.text ?? '')
    .join('')
}

export function messageThinking(m?: Message | null): string {
  if (!m?.content) return ''
  return m.content
    .filter(c => c.type === 'thinking')
    .map(c => c.thinking || c.text || '')
    .join('')
}

function previewOf(text: string, n = 160): string {
  const t = text.replace(/\s+/g, ' ').trim()
  return t.length > n ? t.slice(0, n) + '…' : t
}

function toolResultText(result: unknown): string {
  if (result == null) return ''
  if (typeof result === 'string') return result
  if (typeof result === 'object') {
    const o = result as Record<string, unknown>
    const content = o.Content ?? o.content
    if (Array.isArray(content)) {
      return content.map((c: { text?: string }) => c?.text ?? '').join('')
    }
    try {
      return JSON.stringify(result, null, 2)
    } catch {
      return String(result)
    }
  }
  return String(result)
}

function tsMs(m?: Message, fallback?: string): number | undefined {
  if (m?.timestamp) return m.timestamp
  if (fallback) {
    const n = Date.parse(fallback)
    if (!Number.isNaN(n)) return n
  }
  return undefined
}

export function loadHistory(detail: SessionDetail): ViewState {
  const s = emptyView()
  s.model = detail.model ?? ''
  s.provider = detail.provider ?? ''
  s.cwd = detail.cwd ?? ''
  s.title = detail.title ?? ''
  s.busy = !!detail.running
  const entries = detail.entries ?? []
  if (entries.length === 0 && detail.messages) {
    for (const m of detail.messages) applyMessage(s, m, crypto.randomUUID(), undefined)
    return s
  }
  for (const e of entries) applyEntry(s, e)
  return s
}

function applyEntry(s: ViewState, e: Entry) {
  if (e.type === 'message' && e.message) {
    applyMessage(s, e.message, e.id, e.timestamp)
    return
  }
  if (e.type === 'compaction') {
    const summary = e.summary || '上下文已压缩'
    s.nodes.push({ kind: 'compaction', id: e.id, summary, tokensBefore: e.tokensBefore })
    s.records.push({
      id: e.id,
      kind: 'compacted',
      turn: s.turn || 1,
      preview: previewOf(summary),
      output: summary,
      usage: e.usage,
      startedAt: tsMs(undefined, e.timestamp),
    })
  }
}

function applyMessage(s: ViewState, m: Message, id: string, stamp?: string) {
  if (m.role === 'user') {
    const text = messageText(m)
    s.turn += 1
    s.nodes.push({ kind: 'user', id, text, ts: tsMs(m, stamp) })
    s.records.push({
      id,
      kind: 'user',
      turn: s.turn,
      preview: previewOf(text),
      output: text,
      startedAt: tsMs(m, stamp),
    })
    if (!s.title) s.title = previewOf(text, 80)
    return
  }
  if (m.role === 'assistant') {
    const text = messageText(m)
    const thinking = messageThinking(m)
    s.nodes.push({
      kind: 'assistant',
      id,
      text,
      thinking,
      usage: m.usage,
      ttftMs: m.ttftMs,
      latencyMs: m.latencyMs,
      error: m.errorMessage,
    })
    s.records.push({
      id,
      kind: 'assistant',
      turn: s.turn || 1,
      preview: previewOf(thinking ? thinking : text),
      output: text,
      input: thinking || undefined,
      usage: m.usage,
      durationMs: m.latencyMs,
      ttftMs: m.ttftMs,
      startedAt: tsMs(m, stamp),
      error: !!m.errorMessage,
    })
    for (const c of m.content ?? []) {
      if (c.type !== 'toolCall' || !c.id) continue
      const args = c.arguments
      s.nodes.push({
        kind: 'tool',
        id: c.id,
        name: c.name || 'tool',
        args,
      })
      s.records.push({
        id: c.id,
        kind: 'tool',
        turn: s.turn || 1,
        preview: previewOf(c.name + ' ' + compactArgs(args)),
        input: args,
        name: c.name,
        startedAt: tsMs(m, stamp),
      })
    }
    return
  }
  if (m.role === 'toolResult') {
    const text = messageText(m)
    const tid = m.toolCallId || id
    patchTool(s, tid, {
      result: text,
      isError: m.isError,
      durationMs: m.durationMs,
      running: false,
      name: m.toolName,
    })
  }
}

function compactArgs(args: unknown): string {
  if (args == null) return ''
  if (typeof args === 'string') return args
  try {
    return JSON.stringify(args)
  } catch {
    return ''
  }
}

function patchTool(s: ViewState, id: string, patch: Partial<Extract<ChatNode, { kind: 'tool' }>> & { output?: unknown }) {
  s.nodes = s.nodes.map(n => {
    if (n.kind !== 'tool' || n.id !== id) return n
    return { ...n, ...patch, running: patch.running ?? n.running }
  })
  s.records = s.records.map(r => {
    if (r.kind !== 'tool' || r.id !== id) return r
    const result = patch.result ?? (typeof r.output === 'string' ? r.output : '')
    return {
      ...r,
      output: result,
      running: patch.running ?? r.running,
      error: patch.isError ?? r.error,
      durationMs: patch.durationMs ?? r.durationMs,
      name: patch.name || r.name,
      preview: previewOf((patch.name || r.name || 'tool') + ' ' + (result || compactArgs(r.input))),
    }
  })
}

function lastUserText(s: ViewState): string | null {
  for (let i = s.nodes.length - 1; i >= 0; i--) {
    const n = s.nodes[i]
    if (n.kind === 'user') return n.text
  }
  return null
}

function persistedAssistantsAfterLastUser(s: ViewState): number {
  let i = s.nodes.length - 1
  while (i >= 0 && s.nodes[i].kind !== 'user') i--
  let n = 0
  for (let j = i + 1; j < s.nodes.length; j++) {
    const node = s.nodes[j]
    if (node.kind === 'assistant' && !node.streaming) n++
  }
  return n
}

export function applyEvent(s: ViewState, ev: LoopEvent): ViewState {
  const next: ViewState = {
    ...s,
    nodes: s.nodes.slice(),
    records: s.records.slice(),
  }
  switch (ev.type) {
    case 'agent_start':
      next.busy = true
      next.error = null
      break
    case 'agent_end':
      next.busy = false
      next.nodes = next.nodes.map(n =>
        n.kind === 'assistant' && n.streaming ? { ...n, streaming: false } : n.kind === 'tool' && n.running ? { ...n, running: false } : n,
      )
      next.records = next.records.map(r => r.running ? { ...r, running: false } : r)
      break
    case 'message_start':
    case 'message_end':
    case 'message_update':
      applyLiveMessage(next, ev)
      break
    case 'tool_execution_start':
      if (ev.toolCallId) {
        const existing = next.nodes.some(n => n.kind === 'tool' && n.id === ev.toolCallId)
        if (!existing) {
          next.nodes.push({
            kind: 'tool',
            id: ev.toolCallId,
            name: ev.toolName || 'tool',
            args: ev.args,
            running: true,
          })
          next.records.push({
            id: ev.toolCallId,
            kind: 'tool',
            turn: next.turn || 1,
            preview: previewOf((ev.toolName || 'tool') + ' ' + compactArgs(ev.args)),
            input: ev.args,
            name: ev.toolName,
            running: true,
            startedAt: Date.now(),
          })
        } else {
          patchTool(next, ev.toolCallId, { running: true, args: ev.args, name: ev.toolName })
        }
      }
      break
    case 'tool_execution_end':
      if (ev.toolCallId) {
        patchTool(next, ev.toolCallId, {
          result: toolResultText(ev.result),
          isError: ev.isError,
          running: false,
          name: ev.toolName,
        })
      }
      break
    default:
      break
  }
  return next
}

function applyLiveMessage(s: ViewState, ev: LoopEvent) {
  const m = ev.message ?? ev.assistantMessageEvent?.partial
  if (!m) return
  if (m.role === 'user') {
    const text = messageText(m)
    if (lastUserText(s) === text) return
    applyMessage(s, m, `live-user-${s.nodes.length}`, undefined)
    return
  }
  if (m.role === 'toolResult') {
    applyMessage(s, m, m.toolCallId || `live-tr-${s.nodes.length}`, undefined)
    return
  }
  if (m.role !== 'assistant') return

  if (ev.type === 'message_start') {
    const persisted = persistedAssistantsAfterLastUser(s)
    const live = s.nodes.filter(n => n.kind === 'assistant' && n.streaming).length
    if (live === 0 && persisted > 0) {
      // replay of an already-loaded completed assistant; skip until we run out
      const startedReplay = s.replayed ?? 0
      if (startedReplay < persisted) {
        s.replayed = startedReplay + 1
        return
      }
    }
    const id = `live-asst-${s.nodes.length}`
    s.nodes.push({
      kind: 'assistant',
      id,
      text: messageText(m),
      thinking: messageThinking(m),
      streaming: true,
    })
    s.records.push({
      id,
      kind: 'assistant',
      turn: s.turn || 1,
      preview: previewOf(messageText(m) || messageThinking(m) || '…'),
      running: true,
      startedAt: Date.now(),
    })
    return
  }

  const idx = lastStreamingAssistant(s)
  if (idx < 0) {
    if (ev.type === 'message_end') {
      const text = messageText(m)
      if (s.nodes.some(n => n.kind === 'assistant' && !n.streaming && n.text === text && text !== '')) return
      applyMessage(s, m, `live-asst-${s.nodes.length}`, undefined)
    }
    return
  }
  const node = s.nodes[idx]
  if (node.kind !== 'assistant') return
  const text = messageText(m)
  const thinking = messageThinking(m)
  s.nodes[idx] = {
    ...node,
    text,
    thinking,
    usage: m.usage ?? node.usage,
    ttftMs: m.ttftMs ?? node.ttftMs,
    latencyMs: m.latencyMs ?? node.latencyMs,
    error: m.errorMessage || node.error,
    streaming: ev.type !== 'message_end',
  }
  s.records = s.records.map(r => r.id === node.id
    ? {
        ...r,
        preview: previewOf(text || thinking || r.preview),
        output: text,
        input: thinking || r.input,
        usage: m.usage ?? r.usage,
        durationMs: m.latencyMs ?? r.durationMs,
        ttftMs: m.ttftMs ?? r.ttftMs,
        running: ev.type !== 'message_end',
        error: !!m.errorMessage,
      }
    : r)

  if (ev.type === 'message_end') {
    for (const c of m.content ?? []) {
      if (c.type !== 'toolCall' || !c.id) continue
      if (s.nodes.some(n => n.kind === 'tool' && n.id === c.id)) continue
      s.nodes.push({ kind: 'tool', id: c.id, name: c.name || 'tool', args: c.arguments })
      s.records.push({
        id: c.id,
        kind: 'tool',
        turn: s.turn || 1,
        preview: previewOf((c.name || 'tool') + ' ' + compactArgs(c.arguments)),
        input: c.arguments,
        name: c.name,
        startedAt: Date.now(),
      })
    }
  }
}

function lastStreamingAssistant(s: ViewState): number {
  for (let i = s.nodes.length - 1; i >= 0; i--) {
    const n = s.nodes[i]
    if (n.kind === 'assistant' && n.streaming) return i
  }
  return -1
}

export function appendOptimisticUser(s: ViewState, text: string): ViewState {
  const next = { ...s, nodes: s.nodes.slice(), records: s.records.slice(), busy: true, error: null }
  applyMessage(next, { role: 'user', content: [{ type: 'text', text }] }, `opt-user-${Date.now()}`)
  return next
}

export function usageTotals(s: ViewState): { input: number; output: number; cacheRead: number } {
  let input = 0
  let output = 0
  let cacheRead = 0
  for (const n of s.nodes) {
    if (n.kind !== 'assistant' || !n.usage) continue
    input += n.usage.input ?? 0
    output += n.usage.output ?? 0
    cacheRead += n.usage.cacheRead ?? 0
  }
  return { input, output, cacheRead }
}
