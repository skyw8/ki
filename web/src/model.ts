import type { ChatNode, Entry, LoopEvent, Message, Meta, ModelInfo, SessionDetail, TrajRecord, Usage, ViewState } from './types'

const LAST_MODEL_KEY = 'ki-last-model'
const THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type ComposerModel = { provider: string; model: string; thinkingEffort: string }

export function loadLastComposerModel(): ComposerModel | null {
  try {
    const raw = localStorage.getItem(LAST_MODEL_KEY)
    if (!raw) return null
    const v = JSON.parse(raw) as Partial<ComposerModel>
    if (!v.provider || !v.model) return null
    return { provider: v.provider, model: v.model, thinkingEffort: v.thinkingEffort ?? '' }
  } catch {
    return null
  }
}

export function saveLastComposerModel(cfg: ComposerModel): void {
  if (!cfg.provider || !cfg.model) return
  try {
    localStorage.setItem(LAST_MODEL_KEY, JSON.stringify(cfg))
  } catch {
    // Private mode / quota should not block composer updates.
  }
}

export function initialView(): ViewState {
  const last = loadLastComposerModel()
  if (!last) return emptyView()
  return { ...emptyView(), provider: last.provider, model: last.model, thinkingEffort: last.thinkingEffort }
}

export function keepComposer(view: ViewState): ViewState {
  return { ...emptyView(), provider: view.provider, model: view.model, thinkingEffort: view.thinkingEffort }
}

// Matches provider.ClampThinking / DefaultThinking so a model switch keeps the
// nearest effort instead of snapping to thinkingLevels[0] ("off").
export function clampThinkingEffort(effort: string, model?: Pick<ModelInfo, 'thinkingLevels' | 'defaultThinking'>): string {
  const levels = model?.thinkingLevels ?? []
  if (!levels.length) return ''
  const fallback = model?.defaultThinking && levels.includes(model.defaultThinking) ? model.defaultThinking : levels[0]
  if (!effort) return fallback
  if (levels.includes(effort)) return effort
  const idx = THINKING_LEVELS.indexOf(effort)
  if (idx < 0) return fallback
  for (let i = idx; i < THINKING_LEVELS.length; i++) {
    if (levels.includes(THINKING_LEVELS[i])) return THINKING_LEVELS[i]
  }
  for (let i = idx - 1; i >= 0; i--) {
    if (levels.includes(THINKING_LEVELS[i])) return THINKING_LEVELS[i]
  }
  return fallback
}

export function pickComposerModel(
  models: ModelInfo[],
  preferred: ComposerModel | null,
  fallback?: Pick<Meta, 'provider' | 'model' | 'thinkingEffort'> | null,
): ComposerModel {
  const found = (provider?: string, model?: string) =>
    provider && model ? models.find(m => m.provider === provider && m.id === model) : undefined
  const chosen = found(preferred?.provider, preferred?.model)
    ?? found(fallback?.provider, fallback?.model)
    ?? models[0]
  const effortHint = preferred?.thinkingEffort || fallback?.thinkingEffort || ''
  if (!chosen) {
    return {
      provider: preferred?.provider || fallback?.provider || '',
      model: preferred?.model || fallback?.model || '',
      thinkingEffort: effortHint,
    }
  }
  return {
    provider: chosen.provider,
    model: chosen.id,
    thinkingEffort: clampThinkingEffort(effortHint, chosen),
  }
}

export function sessionCreateBody(
  workspaceId: string | null | undefined,
  composer: ComposerModel,
  models: ModelInfo[],
): { workspaceId?: string; model?: string; thinkingEffort?: string } {
  const body: { workspaceId?: string; model?: string; thinkingEffort?: string } = {}
  if (workspaceId) body.workspaceId = workspaceId
  const found = models.find(m => m.provider === composer.provider && m.id === composer.model)
  const spec = found?.spec || (composer.provider && composer.model ? `${composer.provider}/${composer.model}` : composer.model)
  if (spec) body.model = spec
  const effort = found ? clampThinkingEffort(composer.thinkingEffort, found) : composer.thinkingEffort
  if (effort) body.thinkingEffort = effort
  return body
}

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
	thinkingEffort: '',
	allEntries: [],
	queued: [],
	extQueued: [],
	extensionUi: [],
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

// Message timestamps come from two places: the SSE event's message.timestamp
// (server clock, authoritative) and a fallback. The fallback is either a
// number (client Date.now() for optimistic nodes, live path) or a string
// (jsonl entry.timestamp, history path) — accepting both keeps one function
// for both paths.
function tsMs(m?: Message, fallback?: string | number): number | undefined {
  if (m?.timestamp) return m.timestamp
  if (fallback != null) {
    const n = typeof fallback === 'number' ? fallback : Date.parse(fallback)
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
  s.commands = detail.commands ?? []
	s.thinkingEffort = detail.thinkingEffort ?? ''
	s.queued = detail.queued ?? []
	s.extQueued = detail.extQueued ?? []
	s.extensionUi = detail.extensionUi ?? []
  const entries = detail.entries ?? []
	s.allEntries = entries
	s.leafId = detail.leafId
  if (entries.length === 0 && detail.messages) {
    for (const m of detail.messages) applyMessage(s, m, crypto.randomUUID(), undefined)
    return s
  }
	const byId = new Map(entries.map(e => [e.id, e]))
	const active: Entry[] = []
	let id = detail.leafId || entries.at(-1)?.id
	const seen = new Set<string>()
	while (id && !seen.has(id)) {
		seen.add(id)
		const entry = byId.get(id)
		if (!entry) break
		active.push(entry)
		id = entry.parentId
	}
	active.reverse()
	for (const e of active) applyEntry(s, e)
  return s
}

function lastSystem(s: ViewState): TrajRecord | undefined {
  for (let i = s.records.length - 1; i >= 0; i--) {
    if (s.records[i].kind === 'system') return s.records[i]
  }
  return undefined
}

function normalizeTools(raw?: TrajRecord['tools'] | unknown): TrajRecord['tools'] {
  if (!Array.isArray(raw)) return raw as TrajRecord['tools']
  return raw.map(item => {
    if (!item || typeof item !== 'object') return { name: String(item) }
    const o = item as Record<string, unknown>
    const parameters = o.parameters ?? o.Parameters
    return {
      name: String(o.name ?? o.Name ?? ''),
      description: o.description != null || o.Description != null ? String(o.description ?? o.Description) : undefined,
      parameters: parameters && typeof parameters === 'object' ? parameters as Record<string, unknown> : undefined,
    }
  })
}

function applyRequestHeader(s: ViewState, id: string, system: string, tools: TrajRecord['tools'], stamp?: string) {
  const prev = lastSystem(s)
  tools = normalizeTools(tools)
  s.records.push({
    id,
    kind: 'system',
    turn: s.turn || 1,
    preview: previewOf(system || `${tools?.length ?? 0} tools`),
    system,
    tools,
    previousSystem: prev?.system,
    previousTools: prev?.tools,
    input: system,
    output: tools,
    startedAt: tsMs(undefined, stamp),
  })
}

function applyEntry(s: ViewState, e: Entry) {
	if (e.type === 'context_usage') {
		s.contextUsage = { usedTokens: e.usedTokens ?? 0, contextWindow: e.contextWindow ?? 0, estimated: !!e.estimated }
		return
	}
  if (e.type === 'request_header') {
    applyRequestHeader(s, e.id, e.system ?? '', e.tools, e.timestamp)
    return
  }
  if (e.type === 'message' && e.message) {
    applyMessage(s, e.message, e.id, e.timestamp, e.parentId)
    return
  }
	if (e.type === 'patch_apply_updated' && e.details && typeof e.details === 'object') {
		const details = e.details as { toolCallId?: string; toolName?: string; partialResult?: unknown }
		if (details.toolCallId) patchApplyPreview(s, details.toolCallId, details.toolName, details.partialResult, e.timestamp)
		return
	}
  if (e.type === 'compaction') {
    const summary = e.summary || ''
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
    return
  }
  // compaction_start/end entries are persisted alongside SSE; show them as a
  // compact record on replay.
  if (e.type === 'compaction_start' || e.type === 'compaction_end') {
    applyCompactEvent(s, e.id, e.type, e.details, e.timestamp)
  }
}

function compactReason(details?: unknown): string {
  if (details && typeof details === 'object') {
    const d = details as { reason?: string; ok?: boolean }
    return d.reason || ''
  }
  return ''
}

function applyCompactEvent(s: ViewState, id: string, type: string, details?: unknown, stamp?: string) {
  const reason = compactReason(details)
  if (type === 'compaction_start') {
    s.records.push({
      id,
      kind: 'compact',
      turn: s.turn || 1,
      preview: `Compacting (${reason || 'auto'})…`,
      running: true,
      startedAt: tsMs(undefined, stamp),
    })
    return
  }
  const rec = s.records.find(r => r.id === id)
  if (rec && rec.kind === 'compact') {
    rec.running = false
    rec.preview = `Compacted (${reason || 'auto'})`
  }
}

function applyMessage(s: ViewState, m: Message, id: string, stamp?: string | number, parentId?: string) {
  if (m.role === 'user') {
    const text = messageText(m)
    s.turn += 1
    s.nodes.push({ kind: 'user', id, parentId, text, content: m.content ?? [], ts: tsMs(m, stamp), origin: m.origin })
    s.records.push({
      id,
	  parentId,
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
	  parentId,
      text,
      thinking,
      usage: m.usage,
      ttftMs: m.ttftMs,
      latencyMs: m.latencyMs,
      error: m.errorMessage,
	  stopReason: m.stopReason,
      ts: tsMs(m, stamp),
      images: (m.content ?? []).filter(c => c.type === 'image' && c.data).map(c => ({ data: c.data!, mimeType: c.mimeType || 'image/png' })),
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
	  if (s.nodes.some(n => n.kind === 'tool' && n.id === c.id)) {
		patchTool(s, c.id, { name: c.name, args: c.arguments ?? (c.input !== undefined ? { input: c.input } : undefined) })
		continue
	  }
	  const args = c.arguments ?? (c.input !== undefined ? { input: c.input } : undefined)
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
	  details: m.details ?? (m.toolName === 'apply_patch' ? { status: 'failed', exact: true, changes: [] } : undefined),
      running: false,
      name: m.toolName,
    })
  }
}

function patchApplyPreview(s: ViewState, id: string, name?: string, details?: unknown, stamp?: string | number) {
	if (!s.nodes.some(n => n.kind === 'tool' && n.id === id)) {
		s.nodes.push({ kind: 'tool', id, name: name || 'apply_patch', details, running: true })
		s.records.push({ id, kind: 'tool', turn: s.turn || 1, preview: name || 'apply_patch', name: name || 'apply_patch', details, running: true, startedAt: tsMs(undefined, stamp) ?? Date.now() })
		return
	}
	patchTool(s, id, { name: name || 'apply_patch', details, running: true })
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
	  details: patch.details ?? r.details,
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
	case 'context_usage':
		next.contextUsage = { usedTokens: ev.usedTokens ?? 0, contextWindow: ev.contextWindow ?? 0, estimated: !!ev.estimated }
		break
    case 'agent_start':
      next.busy = true
      next.error = null
      break
    case 'agent_end':
      next.busy = false
      next.stopping = false
      next.nodes = next.nodes.map(n =>
        n.kind === 'assistant' && n.streaming ? { ...n, streaming: false } : n.kind === 'tool' && n.running ? { ...n, running: false } : n,
      )
      next.records = next.records.map(r => r.running ? { ...r, running: false } : r)
      break
    case 'steer_accepted':
      if (ev.message?.content) return appendOptimisticUser(s, ev.message.content)
      break
    case 'run_aborted':
      next.stopping = true
      break
    case 'extension_notice':
    case 'extension_ui_prompt':
      break
    case 'extension_ui_updated':
      break
    case 'request_header': {
      const prev = lastSystem(next)
      if (prev && prev.system === (ev.system ?? '') && JSON.stringify(prev.tools) === JSON.stringify(ev.tools)) break
      applyRequestHeader(next, `live-sys-${next.records.length}`, ev.system ?? '', ev.tools)
      break
    }
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
	case 'patch_apply_updated':
		if (ev.toolCallId) patchApplyPreview(next, ev.toolCallId, ev.toolName, ev.partialResult)
		break
    case 'compaction_start':
    case 'compaction_end': {
      // id is not part of the wire event; match by kind+order on the live path.
      const stamp = Date.now()
      if (ev.type === 'compaction_start') {
        next.records.push({
          id: `compact-live-${stamp}`,
          kind: 'compact',
          turn: next.turn || 1,
          preview: `Compacting (${ev.reason || 'auto'})…`,
          running: true,
          startedAt: stamp,
        })
      } else {
        const rec = [...next.records].reverse().find(r => r.kind === 'compact' && r.running)
        if (rec) {
          rec.running = false
          rec.preview = `Compacted (${ev.reason || 'auto'})${ev.ok === false ? ' (failed)' : ''}`
        }
      }
      break
    }
    case 'tool_execution_end':
      if (ev.toolCallId) {
		const resultDetails = ev.result && typeof ev.result === 'object'
		  ? ((ev.result as Record<string, unknown>).details ?? (ev.result as Record<string, unknown>).Details)
		  : undefined
		patchTool(next, ev.toolCallId, {
          result: toolResultText(ev.result),
          isError: ev.isError,
          running: false,
          name: ev.toolName,
		  details: resultDetails ?? (ev.toolName === 'apply_patch' ? { status: 'failed', exact: true, changes: [] } : undefined),
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
    if (lastUserText(s) === text) {
      // Same text is already on screen. Two cases:
      //   1. history replay: loadHistory already read the jsonl entry, which
      //      has the same timestamp the event carries — nothing to do.
      //   2. optimistic append: the bubble was drawn before the request was
      //      sent, so its ts is a client-side guess (or absent). We must NOT
      //      skip the event entirely: the event's timestamp is the server's
      //      authoritative one and is what a reload would show. Backfill it so
      //      the live view matches the reloaded view. (This was the bug where
      //      the time under a just-sent message only appeared after refresh.)
      if (m.timestamp) {
        for (let i = s.nodes.length - 1; i >= 0; i--) {
          const n = s.nodes[i]
          if (n.kind === 'user' && n.text === text) {
			const nextId = ev.entryId || n.id
            s.nodes[i] = { ...n, id: nextId, content: m.content ?? n.content, ts: m.timestamp, origin: m.origin ?? n.origin }
            // Keep the trajectory record in sync so the detail panel shows the
            // same start time as the chat bubble.
			s.records = s.records.map(r => (r.id === n.id ? { ...r, id: nextId, startedAt: m.timestamp } : r))
            break
          }
        }
      }
      return
    }
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
    // The assistant has no optimistic placeholder — this event is the first
    // sight of the reply, so its timestamp is the earliest we can show and
    // matches what a reload would display. Recording it at start means the
    // bubble and the trajectory panel show it even while streaming.
    const ts = m.timestamp
    s.nodes.push({
      kind: 'assistant',
      id,
      text: messageText(m),
      thinking: messageThinking(m),
      streaming: true,
      ts,
    })
    s.records.push({
      id,
      kind: 'assistant',
      turn: s.turn || 1,
      preview: previewOf(messageText(m) || messageThinking(m) || '…'),
      running: true,
      // Fall back to the local clock only if the event somehow carries no
      // timestamp; server time is preferred for cross-client consistency.
      startedAt: ts ?? Date.now(),
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
    // Streaming deltas (message_update) usually carry no timestamp, so keep
    // the one from message_start; message_end carries the final authoritative
    // value, which wins when present.
    ts: m.timestamp ?? node.ts,
    usage: m.usage ?? node.usage,
    ttftMs: m.ttftMs ?? node.ttftMs,
    latencyMs: m.latencyMs ?? node.latencyMs,
    error: m.errorMessage || node.error,
	stopReason: m.stopReason || node.stopReason,
    streaming: ev.type !== 'message_end',
  }
	if (ev.type === 'message_end' && ev.entryId) {
		s.nodes[idx] = { ...s.nodes[idx] as Extract<ChatNode, { kind: 'assistant' }>, id: ev.entryId }
	}
  s.records = s.records.map(r => r.id === node.id
    ? {
        ...r,
		id: ev.type === 'message_end' && ev.entryId ? ev.entryId : r.id,
        preview: previewOf(text || thinking || r.preview),
        output: text,
        input: thinking || r.input,
        startedAt: m.timestamp ?? r.startedAt,
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
	  const args = c.arguments ?? (c.input !== undefined ? { input: c.input } : undefined)
	  s.nodes.push({ kind: 'tool', id: c.id, name: c.name || 'tool', args })
      s.records.push({
        id: c.id,
        kind: 'tool',
        turn: s.turn || 1,
		preview: previewOf((c.name || 'tool') + ' ' + compactArgs(args)),
		input: args,
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

export function appendOptimisticUser(s: ViewState, content: import('./types').Content[]): ViewState {
  const text = content.filter(c => c.type === 'text' || c.type === '').map(c => c.text ?? '').join('\n')
  if (text && lastUserText(s) === text) {
    return { ...s, busy: true, error: null }
  }
  const next = { ...s, nodes: s.nodes.slice(), records: s.records.slice(), busy: true, error: null }
  // The bubble is drawn before the server confirms, so there is no real
  // timestamp yet. Date.now() (a number, not a string) lets tsMs use the local
  // clock so the time shows immediately; the SSE event later backfills the
  // server's authoritative timestamp.
  applyMessage(next, { role: 'user', content }, `opt-user-${Date.now()}`, Date.now())
  return next
}

export type SessionStats = {
  turns: number
  steps: number
  /** Uncached + cache read + cache write. */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  hasCost: boolean
  cost: number
  ttftMs: number
  ttftSteps: number
  decodeMs: number
  decodeTokens: number
}

function emptyStats(): SessionStats {
  return {
    turns: 0, steps: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
    hasCost: false, cost: 0, ttftMs: 0, ttftSteps: 0, decodeMs: 0, decodeTokens: 0,
  }
}

function addUsage(out: SessionStats, usage?: Usage | null, timing?: { ttftMs?: number; latencyMs?: number }) {
  if (usage) {
    out.input += (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0)
    out.output += usage.output ?? 0
    out.cacheRead += usage.cacheRead ?? 0
    out.cacheWrite += usage.cacheWrite ?? 0
    if (usage.cost) {
      out.hasCost = true
      out.cost += usage.cost.total
    }
  }
  if (timing?.ttftMs != null && Number.isFinite(timing.ttftMs) && timing.ttftMs > 0) {
    out.ttftMs += timing.ttftMs
    out.ttftSteps += 1
  }
  // Decode span is first-token → message_end. A step missing TTFT is dropped
  // rather than treating the whole latency as decode (that would inflate TPS).
  if (timing?.latencyMs != null && timing.ttftMs != null && usage && (usage.output ?? 0) > 0) {
    const decodeMs = Math.max(0, timing.latencyMs - timing.ttftMs)
    if (decodeMs > 0) {
      out.decodeMs += decodeMs
      out.decodeTokens += usage.output ?? 0
    }
  }
}

function activePath(s: ViewState): Entry[] {
  if (s.allEntries.length === 0) return []
  const byId = new Map(s.allEntries.map(e => [e.id, e]))
  const path: Entry[] = []
  const seen = new Set<string>()
  let id = s.leafId || s.allEntries.at(-1)?.id
  while (id && !seen.has(id)) {
    seen.add(id)
    const entry = byId.get(id)
    if (!entry) break
    path.push(entry)
    id = entry.parentId
  }
  return path
}

/** Whole-branch session figures. Walks the current leaf path so compacted
 * assistants still count, then adds live nodes that are not yet in jsonl. */
export function sessionStats(s: ViewState): SessionStats {
  const out = emptyStats()
  const counted = new Set<string>()
  for (const e of activePath(s)) {
    counted.add(e.id)
    if (e.type === 'message' && e.message?.role === 'user') {
      out.turns += 1
      continue
    }
    if (e.type === 'message' && e.message?.role === 'assistant') {
      out.steps += 1
      addUsage(out, e.message.usage, { ttftMs: e.message.ttftMs, latencyMs: e.message.latencyMs })
      continue
    }
    if (e.type === 'compaction' && e.usage) {
      out.steps += 1
      addUsage(out, e.usage)
    }
  }
  for (const n of s.nodes) {
    if (counted.has(n.id)) continue
    if (n.kind === 'user') {
      out.turns += 1
      continue
    }
    if (n.kind !== 'assistant' || n.streaming) continue
    out.steps += 1
    addUsage(out, n.usage, { ttftMs: n.ttftMs, latencyMs: n.latencyMs })
  }
  return out
}

export function cacheHitPercent(s: SessionStats): number | null {
  if (s.input <= 0 || s.cacheRead <= 0) return null
  return Math.round(s.cacheRead / s.input * 100)
}

export function formatTokens(n: number): string {
  const scaled = (v: number): string => (v >= 100 ? String(Math.round(v)) : String(Math.round(v * 10) / 10))
  if (n < 1_000) return String(n)
  if (n < 1_000_000) return `${scaled(n / 1_000)}K`
  return `${scaled(n / 1_000_000)}M`
}

export function formatDuration(ms: number): string {
  const s = Math.max(0, ms) / 1_000
  if (s < 60) return `${Math.round(s * 10) / 10}s`
  const whole = Math.round(s)
  return `${Math.floor(whole / 60)}m${String(whole % 60).padStart(2, '0')}s`
}

export function formatTokensPerSecond(tps: number): string {
  const clamped = Math.max(0, tps)
  return clamped >= 10 ? String(Math.round(clamped)) : String(Math.round(clamped * 10) / 10)
}

export function formatCost(total: number): string {
  const n = Math.max(0, total)
  return n < 0.01 ? n.toFixed(4) : n.toFixed(2)
}
