import { mergeAgentSnapshots, mergeProcessSnapshots } from './runtime'
import { reconcileQuestions } from '../features/questions/model'
import type { CacheMiss, CompactTurn, ChatNode, Content, Entry, IndexEntry, LoopEvent, Message, Meta, ModelInfo, PromptChange, PromptSnapshot, RequestView, SessionDetail, ToolSchema, TrajRecord, Usage, ViewState } from '../api/types'
import { entryTurns, groupTurns, isHumanEntry, isHumanPrompt, isTransientUserId, reconcileUserNodes, userMessageIdentity } from './transcriptIdentity'
import type { TurnId } from './transcriptIdentity'
import { bodyKind, bodyRank, coveredTurns, projectedBodies } from './transcriptCoverage'
import { compactionOperations, isCompactionLifecycle } from './compactionLifecycle'
export { isHumanPrompt, reconcileUserNodes } from './transcriptIdentity'
export type { CacheMiss } from '../api/types'

const THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type ComposerModel = { provider: string; model: string; thinkingEffort: string }

export function keepComposer(view: ViewState): ViewState {
  return { ...emptyView(), provider: view.provider, model: view.model, thinkingEffort: view.thinkingEffort }
}

export function clampThinkingEffort(effort: string, model?: Pick<ModelInfo, 'thinkingLevels' | 'defaultThinking'>): string {
  const levels = model?.thinkingLevels ?? []
  if (!levels.length) return ''
  const fallback = model?.defaultThinking && levels.includes(model.defaultThinking) ? model.defaultThinking : levels[0]
  if (!effort) return fallback
  if (levels.includes(effort)) return effort
  const fast = effort.endsWith(' fast')
  const base = fast ? effort.slice(0, -5) : effort
  const idx = THINKING_LEVELS.indexOf(base)
  if (idx < 0) return fallback
  const suffix = fast && THINKING_LEVELS.some(level => levels.includes(`${level} fast`)) ? ' fast' : ''
  for (let i = idx; i < THINKING_LEVELS.length; i++) {
    const level = THINKING_LEVELS[i] + suffix
    if (levels.includes(level)) return level
  }
  for (let i = idx - 1; i >= 0; i--) {
    const level = THINKING_LEVELS[i] + suffix
    if (levels.includes(level)) return level
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
    return { provider: '', model: '', thinkingEffort: '' }
  }
  return {
    provider: chosen.provider,
    model: chosen.id,
    thinkingEffort: clampThinkingEffort(effortHint, chosen),
  }
}

export function reconcileComposerModel(
  view: ViewState,
  hasSession: boolean,
  models: ModelInfo[],
  fallback: Pick<Meta, 'provider' | 'model' | 'thinkingEffort'>,
): ViewState {
  if (hasSession) return view
  const picked = pickComposerModel(models, view, fallback)
  if (view.provider === picked.provider && view.model === picked.model && view.thinkingEffort === picked.thinkingEffort) return view
  return { ...view, ...picked }
}

export function sessionCreateBody(
  workspaceId: string | null | undefined,
  composer: ComposerModel,
  models: ModelInfo[],
): { workspaceId?: string; model?: string; thinkingEffort?: string } {
  const body: { workspaceId?: string; model?: string; thinkingEffort?: string } = {}
  if (workspaceId) body.workspaceId = workspaceId
  const found = models.find(m => m.provider === composer.provider && m.id === composer.model)
  if (!found) return body
  body.model = found.spec || `${found.provider}/${found.id}`
  const effort = clampThinkingEffort(composer.thinkingEffort, found)
  if (effort) body.thinkingEffort = effort
  return body
}

export function emptyView(): ViewState {
  return {
    nodes: [],
    records: [],
    requests: [],
    liveRevision: 0,
    busy: false,
    error: null,
    model: '',
    provider: '',
    cwd: '',
    title: '',
    turn: 0,
	thinkingEffort: '',
	entries: [],
	index: [],
	indexLoaded: false,
	turnBase: 0,
	allEntries: [],
	hasMore: false,
	oldestId: undefined,
	queued: [],
	extQueued: [],
	extensionUi: [],
	runtimeReady: true,
  }
}

export function messageText(m?: Message | null): string {
  if (m?.questionReply) return m.questionReply.answer
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
  let preview = '', space = false
  for (let i = 0; i < text.length; i++) {
    const character = text[i]
    if (/\s/.test(character)) { space = preview.length > 0; continue }
    if (space) preview += ' '
    preview += character
    if (preview.length > n) return preview.slice(0, n) + '…'
    space = false
  }
  return preview
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
  s.questionsSessionId = detail.id
  s.questions = detail.questions ?? []
  s.model = detail.model ?? ''
  s.provider = detail.provider ?? ''
  s.cwd = detail.cwd ?? ''
  s.title = detail.title ?? ''
  s.busy = !!detail.running
  s.commands = detail.commands ?? []
	s.thinkingEffort = detail.thinkingEffort ?? ''
	s.queued = detail.queued ?? []
	s.extQueued = detail.extQueued ?? []
 s.processes=detail.processes ?? []
 s.agents=detail.agents ?? []
	s.extensionUi = detail.extensionUi ?? []
	s.runtimeReady = detail.runtime?.ready !== false
	s.leafId = detail.leafId
	s.hasMore = !!detail.hasMore
	s.oldestId = detail.oldestId
	s.compactTurns = detail.compactTurns
  s.sessionStats = detail.sessionStats
	setIndex(s, detail.index)
	addEntries(s, projectedBodies(detail.entries ?? [], detail.compactTurns))
	return rebuild(s)
}

function setIndex(s: ViewState, index?: IndexEntry[] | null) {
  if (index == null) return
  s.index = index
  s.indexLoaded = true
}

export function applyIndex(s: ViewState, detail: SessionDetail): ViewState {
  const next = { ...s }
  setIndex(next, detail.index ?? [])
  return rebuild(next)
}

function applyCursor(next: ViewState, detail: SessionDetail) {
  if (next.oldestId) return
  if (detail.hasMore !== undefined) next.hasMore = detail.hasMore
  if (detail.oldestId !== undefined) next.oldestId = detail.oldestId
}

export function applyTail(s: ViewState, detail: SessionDetail, expectedLiveRevision = s.liveRevision): ViewState {
  const next = { ...s }
  const stale = expectedLiveRevision !== s.liveRevision
  if (detail.questions) {
    next.questionsSessionId = detail.id
    next.questions = reconcileQuestions(s.questionsSessionId === detail.id ? s.questions ?? [] : [], detail.questions, stale)
  }
  next.busy = stale ? s.busy : !!detail.running
  if (!stale) {
    next.title = detail.title ?? next.title
    next.cwd = detail.cwd ?? next.cwd
    next.model = detail.model ?? next.model
    next.provider = detail.provider ?? next.provider
    next.thinkingEffort = detail.thinkingEffort ?? next.thinkingEffort
  }
  if (!next.busy) {
    next.stopping = false
    next.toolStates = undefined
    next.nodes = next.nodes.filter(n => !(n.kind === 'assistant' && n.streaming) && !(n.kind === 'tool' && n.running))
    next.records = next.records.filter(r => !r.running)
    next.requests = next.requests.filter(r => r.status !== 'running')
  }
  if (!stale) next.leafId = detail.leafId ?? next.leafId
  next.runtimeReady = detail.runtime?.ready !== false
  next.commands = detail.commands ?? next.commands
  next.queued = detail.queued ?? next.queued
  next.extQueued = detail.extQueued ?? next.extQueued
 next.processes=detail.processes ?? next.processes
 next.agents=detail.agents ?? next.agents
  next.extensionUi = detail.extensionUi ?? next.extensionUi
  const entries = detail.entries ?? []
  const settledThrough = Math.max(0, ...entries.filter(e => e.message?.role === 'assistant').map(e => tsMs(e.message, e.timestamp) ?? 0), ...(detail.compactTurns ?? []).map(t => t.assistantAt ?? 0))
  if (settledThrough > 0) {
    const expired = (n: ChatNode) => (n.kind === 'tool' && n.startedAt != null && n.startedAt < settledThrough)
      || (n.kind === 'assistant' && n.streaming && n.ts != null && n.ts < settledThrough)
    next.nodes = next.nodes.filter(n => !expired(n) || (n.kind === 'tool' && !n.running && n.result !== undefined))
    if (next.toolStates) next.toolStates = Object.fromEntries(Object.entries(next.toolStates).filter(([, n]) => !expired(n)))
    next.records = next.records.filter(r => !(r.running && r.startedAt != null && r.startedAt < settledThrough))
    next.requests = next.requests.map(r => r.status === 'running' && r.startedAt != null && r.startedAt < settledThrough ? { ...r, status: 'complete' } : r)
  }
  addEntries(next, projectedBodies(entries, detail.compactTurns))
  mergeCompactTurns(next, detail.compactTurns, detail.leafId != null && (!stale || detail.leafId === s.leafId))
  const snapshotPath = detail.leafId && leafEntries(mergeEntries(next.index, next.entries), detail.leafId, next.compactTurns)
  const descendant = !!(stale && detail.leafId && detail.leafId !== s.leafId && snapshotPath && snapshotPath.some(e => e.id === s.leafId))
  if (descendant) next.leafId = detail.leafId
  if (detail.sessionStats && (!stale || descendant || detail.leafId === s.leafId)) {
    next.sessionStats = detail.sessionStats
  }
  const boundary = s.oldestId ?? s.leafId
  const connected = !boundary || leafEntries(next.entries, next.leafId, next.compactTurns, false).some(e => e.id === boundary)
  const sameFrontier = detail.leafId != null && detail.leafId === s.leafId
  if (!connected && (!stale || descendant || sameFrontier)) {
    next.oldestId = detail.oldestId
    next.hasMore = detail.hasMore
  } else applyCursor(next, detail)
  if (next.compactTurns?.length && (!stale || descendant || sameFrontier)) {
    const active = new Set(leafEntries(mergeEntries(next.index, next.entries), next.leafId, next.compactTurns, false).map(e => e.id))
    next.compactTurns = next.compactTurns.filter(turn => active.has(turn.id))
  }
  if (next.indexLoaded && entries.length) {
    const known = new Set(next.index.map(row => row.id))
    const added = entries.filter(e => !known.has(e.id)).map(entryToIndex)
    if (added.length) next.index = [...next.index, ...added]
  }
  return rebuild(next)
}

function addEntries(s: ViewState, incoming: Entry[]) {
  if (!incoming.length) return
  const byId = new Map(incoming.map(e => [e.id, e]))
  const known = new Set(s.entries.map(e => e.id))
  s.entries = s.entries.map(e => {
    const replacement = byId.get(e.id)
    if (replacement && !replacement.truncated && e.truncated) bodyPreviews.set(replacement, e)
    const chosen = replacement && bodyRank(replacement) >= bodyRank(e) ? replacement : e
    return withContextEstimate(chosen, chosen === e ? replacement?.contextEstimate : e.contextEstimate)
  })
  for (const e of incoming) {
    if (!known.has(e.id)) s.entries.push(e)
  }
}

const bodyPreviews = new WeakMap<Entry, Entry>()
const bodySizes = new WeakMap<Entry, number>()
const evictedPreviews = new WeakSet<Entry>()
export const BODY_CACHE_BYTES = 8 * 1024 * 1024

function bodySize(entry: Entry): number {
  let bytes = bodySizes.get(entry)
  if (bytes === undefined) { bytes = JSON.stringify(entry).length * 2; bodySizes.set(entry, bytes) }
  return bytes
}

export function evictBodies(s: ViewState, protectedIds: ReadonlySet<string>, budget = BODY_CACHE_BYTES): ViewState {
  let bytes = s.entries.reduce((total, e) => total + (evictedPreviews.has(e) ? 0 : bodySize(e)), 0)
  if (bytes <= budget) return s
  let lastUser = -1
  for (let i = s.entries.length - 1; i >= 0; i--) if (isHumanEntry(s.entries[i])) { lastUser = i; break }
  if (lastUser < 0) lastUser = 0
  let changed = false
  const entries = s.entries.map((e, index) => {
    if (isCompactionLifecycle(e)) return e
    if (bytes <= budget || evictedPreviews.has(e) || index >= lastUser || protectedIds.has(e.id) || protectedIds.has(`system:${e.id}`) || (e.message?.toolCallId && protectedIds.has(e.message.toolCallId)) || e.message?.content?.some(c => c.id && protectedIds.has(c.id))) return e
    const base = bodyPreviews.get(e) ?? e
    const preview: Entry = {
      ...e, bodyKind: bodyKind(e) === 'helper' ? 'helper' : 'slim', truncated: true, tools: undefined,
      details: undefined,
      system: base.system?.slice(0, 160), summary: base.summary?.slice(0, 160),
      message: base.message ? { ...base.message, details: undefined, content: base.message.content?.map(c => ({
        type: c.type, id: c.id, name: c.name, path: c.path, size: c.size, mimeType: c.mimeType, toolType: c.toolType,
        text: c.text?.slice(0, 160), thinking: c.thinking?.slice(0, 160), input: c.input?.slice(0, 160),
      })) } : undefined,
    }
    bytes -= bodySize(e)
    evictedPreviews.add(preview)
    changed = true
    return preview
  })
  return changed ? rebuild({ ...s, entries }) : s
}

function entryToIndex(e: Entry): IndexEntry {
  const row: IndexEntry = {
    type: e.type,
    id: e.id,
    parentId: e.parentId,
    timestamp: e.timestamp,
    sideband: e.sideband,
    tokensBefore: e.tokensBefore,
    remoteContext: e.remoteContext,
    contextEstimate: e.contextEstimate,
    usage: e.usage,
    truncated: e.truncated,
  }
  if (e.message) {
    row.role = e.message.role
    row.origin = e.message.origin
    row.usage = e.message.usage ?? e.usage
    row.durationMs = e.message.durationMs
    row.ttftMs = e.message.ttftMs
    row.stopReason = e.message.stopReason
    row.toolCallId = e.message.toolCallId
    row.isError = e.message.isError
    row.name = e.message.toolName
    row.preview = previewOf(messageText(e.message) || messageThinking(e.message))
  } else if (e.type === 'compaction') {
    row.preview = previewOf(e.summary ?? '')
  }
  if (e.details && typeof e.details === 'object') {
    const audit = { ...e.details as LoopEvent, type: e.type }
    if (isNestedToolEvent(audit)) {
      row.toolCallId = audit.toolCallId
      row.name = audit.toolName
      row.parentCallId = audit.parentCallId
      row.cellId = audit.cellId
      row.requestedToolName = audit.requestedToolName
      row.durationMs = audit.durationMs
      row.isError = audit.isError
    }
  }
  return row
}

function rebuild(s: ViewState): ViewState {
  const streaming = s.nodes.filter(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running))
  const runningRecords = s.records.filter(r => r.running && r.kind !== 'compact')
  const runningRequests = s.requests.filter(r => r.status === 'running')
  const next: ViewState = {
    ...s,
    nodes: [],
    records: [],
    requests: [],
    turn: 0,
    turnId: undefined,
    turnBase: 0,
    promptState: undefined,
    currentRequestId: undefined,
  }
  const all = mergeEntries(next.index, next.entries)
  next.allEntries = all
  const chain = leafEntries(all, next.leafId, next.compactTurns)
  next.loadedTurnIds = coveredTurns(next.entries, next.compactTurns ?? [])
  let windowStart = next.oldestId ? chain.findIndex(e => e.id === next.oldestId) : 0
  windowStart = Math.max(0, windowStart)
  while (windowStart > 0 && !isUserEntry(chain[windowStart])) windowStart--
  const window = new Set(chain.slice(windowStart).map(e => e.id))
  const loaded = new Set(next.entries.filter(e => window.has(e.id)).map(e => e.id))
  const operations = compactionOperations(chain)
  const compactAt = new Map(operations.map(operation => [(operation.end ?? operation.start)!.id, operation]))
  const lastExecution = chain.slice().reverse().find(e => e.type === 'message' || e.type === 'request_header'
    || e.type === 'compaction' || isCompactionLifecycle(e))
  const active = new Set(chain.map(e => e.id))
  const cached = new Set(next.entries.map(e => e.id))
  next.entries = [...next.entries.filter(e => !active.has(e.id)), ...chain.filter(e => cached.has(e.id))]
  if (next.indexLoaded) {
    next.turnBase = chain.reduce((n, e) => n + (!loaded.has(e.id) && isUserEntry(e) ? 1 : 0), 0)
  }
  const ordinals = new Map(next.compactTurns?.map(t => [t.id, Math.max(1, t.stats.turn)]))
  const identities = new Map<string, { id: TurnId; ordinal: number }>()
  let ordinal = 0
  for (const group of entryTurns(chain)) {
    ordinal = ordinals.get(group.id) ?? ordinal + 1
    for (const entry of group.items) identities.set(entry.id, { id: group.id, ordinal })
  }
  const explicitCancellations = new Set<string>()
  let pendingAbortedAssistant: string | undefined
  for (const e of chain) {
    if (e.type === 'message' && e.message?.role === 'assistant') {
      pendingAbortedAssistant = e.message.stopReason === 'aborted' ? e.id : undefined
    } else if (e.type === 'run_aborted' && pendingAbortedAssistant) {
      explicitCancellations.add(pendingAbortedAssistant)
      pendingAbortedAssistant = undefined
    }
  }
  for (const e of chain) {
    const identity = identities.get(e.id)!
    next.turn = identity.ordinal
    next.turnId = identity.id
    const nodeStart = next.nodes.length, recordStart = next.records.length, requestStart = next.requests.length
    applyEntry(next, e, loaded.has(e.id))
    const operation = compactAt.get(e.id)
    if (operation) {
      const terminal = operation.end || operation.summary
      const summaryLoaded = !!operation.summary && loaded.has(operation.summary.id)
      const bodyLoaded = !!operation.start && loaded.has(operation.start.id) || !!operation.end && loaded.has(operation.end.id)
      applyCompactEvent(next, e.id, terminal ? 'compaction_end' : 'compaction_start',
        operation.end ? operation.end.details : (operation.summary ? { status: 'committed', ok: true } : operation.start?.details),
        operation.start?.timestamp ?? operation.end?.timestamp, {
          withNode: bodyLoaded && !summaryLoaded,
          running: !terminal && next.busy && operation.start?.id === lastExecution?.id,
          summaryId: operation.summary?.id,
          truncated: !!operation.end && (!loaded.has(operation.end.id) || operation.end.truncated),
          paired: true,
        })
    }
    for (let i = nodeStart; i < next.nodes.length; i++) next.nodes[i].turnId = identity.id
    for (let i = recordStart; i < next.records.length; i++) next.records[i].turnId = identity.id
    for (let i = requestStart; i < next.requests.length; i++) next.requests[i].turnId = identity.id
    if (loaded.has(e.id) && e.type === 'message' && e.message?.role === 'assistant' &&
      e.message.stopReason === 'aborted' && !explicitCancellations.has(e.id)) {
      next.nodes.push({
        kind: 'cancellation',
        id: `legacy-cancellation:${e.id}`,
        reason: e.message.cancelReason,
        source: e.message.cancelSource,
        ts: tsMs(e.message, e.timestamp),
      })
    }
  }

  const expanded = new Set(next.loadedTurnIds)
  const omitted = new Set(next.compactTurns?.filter(t => !expanded.has(t.id)).flatMap(t => t.omittedNodeIds ?? []))
  const anchors = new Set(next.compactTurns?.map(t => t.id))
  next.nodes = next.nodes.filter(n => !omitted.has(n.id) || anchors.has(n.id))
  const firstUser = next.nodes.find(n => n.kind === 'user' && isHumanPrompt(n.origin))
  const firstRecord = firstUser && next.records.find(r => r.kind === 'user' && r.id === firstUser.id)
  if (firstRecord) next.turnBase = Math.max(0, firstRecord.turn - 1)

  const built = new Map(next.nodes.map(n => [n.id, n]))
  const retired = new Set<string>()
  let laterAssistant = false
  for (let i = s.nodes.length - 1; i >= 0; i--) {
    const node = s.nodes[i]
    if (node.kind === 'assistant' && built.get(node.id)?.kind === 'assistant' && !node.streaming) laterAssistant = true
    else if (laterAssistant && nodeLive(node)) retired.add(node.id)
  }
  for (const node of streaming.filter(n => n.kind === 'assistant' && !retired.has(n.id))) {
    if (!built.has(node.id)) next.nodes.push(node)
  }
  for (const node of Object.values(s.toolStates ?? {})) {
    if (retired.has(node.id)) continue
    const settled = built.get(node.id)
    if (settled && hasResult(settled)) continue
    if (!settled) next.nodes.push(node)
    if (!next.records.some(r => r.id === node.id)) {
      const record = s.records.find(r => r.id === node.id)
      if (record) next.records.push({ ...record })
    }
    patchTool(next, node.id, node)
  }
  for (const node of streaming.filter(n => n.kind === 'tool' && !s.toolStates?.[n.id] && !retired.has(n.id))) {
    const settled = built.get(node.id)
    if (settled && hasResult(settled)) continue
    const at = next.nodes.findIndex(n => n.id === node.id)
    if (at < 0) next.nodes.push(node)
    else next.nodes[at] = node
  }
  if (runningRecords.length) {
    const have = new Set(next.records.map(r => r.id))
    for (const rec of runningRecords) if (!have.has(rec.id) && !retired.has(rec.id)) next.records.push(rec)
  }
  for (const req of runningRequests) {
    if (!next.requests.some(r => r.id === req.id)) next.requests.push(req)
  }
  const confirmed = new Set(next.nodes.filter(n => n.kind === 'user').map(n => userMessageIdentity(n as Extract<ChatNode, { kind: 'user' }>)).filter(Boolean))
  for (const n of s.nodes) {
    if (n.kind !== 'user' || !isTransientUserId(n.id) || (userMessageIdentity(n) && confirmed.has(userMessageIdentity(n)))) continue
    if (next.nodes.some(node => node.id === n.id)) continue
    applyMessage(next, { role: 'user', content: n.content, origin: n.origin, clientRequestId: n.clientRequestId, completion: n.completion, external: n.external, timestamp: n.ts }, n.id, n.ts, n.parentId)
  }
  if (next.requests.length) next.currentRequestId = next.requests[next.requests.length - 1].id
  const oldNodes = new Map(s.nodes.map(n => [n.id, n]))
  next.nodes = next.nodes.map(n => {
    const old = oldNodes.get(n.id)
    if (old?.kind === 'assistant' && n.kind === 'assistant' && old.renderKey) n = { ...n, renderKey: old.renderKey }
    if (old?.kind === 'tool' && n.kind === 'tool' && old.running && old.details && n.details === undefined) n = { ...n, details: old.details }
    return old && sameValue(old, n) ? old : n
  })
  attachCompactBaselines(next)
  return next
}

function attachCompactBaselines(s: ViewState) {
  if (!s.compactTurns?.length) return
  const loaded = new Set(s.entries.map(e => e.id))
  const graph = branchGraph(s.allEntries, s.compactTurns)
  s.compactTurns = s.compactTurns.map(turn => {
    const baseline = emptyView()
    let path = walkBranch(graph, turn.tailId, turn.id)
    const anchor = graph.byId.get(turn.id)
    if (!turn.parentId && anchor?.parentId && path[0]?.id === turn.id) {
      path = [...walkBranch(graph, anchor.parentId), ...path]
    }
    for (const e of path) if (loaded.has(e.id)) applyEntry(baseline, e)
    const known = new Map(baseline.nodes.map((n, i) => [n.id, i]))
    for (const tool of turn.toolStates ?? []) {
      const at = known.get(tool.id)
      const n: ChatNode = { kind: 'tool', id: tool.id, name: '', ...(tool.finished ? { result: '', isError: !!tool.isError } : {}) }
      if (at === undefined) baseline.nodes.push(n)
      else baseline.nodes[at] = n
    }
    return { ...turn, baselineNodes: baseline.nodes }
  })
}

function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false
  const aa = a as Record<string, unknown>, bb = b as Record<string, unknown>
  const keys = Object.keys(aa)
  return keys.length === Object.keys(bb).length && keys.every(key => Object.hasOwn(bb, key) && sameValue(aa[key], bb[key]))
}

function isUserEntry(e: Entry): boolean {
  return isHumanEntry(e)
}

export function hydrateEntries(s: ViewState, incoming: Entry[], meta?: { hasMore?: boolean; oldestId?: string; compactTurns?: CompactTurn[] }): ViewState {
  if (!incoming.length && meta == null) return s
  const next = { ...s }
  addEntries(next, projectedBodies(incoming, meta?.compactTurns))
  mergeCompactTurns(next, meta?.compactTurns)
  if (meta?.hasMore !== undefined) next.hasMore = meta.hasMore
  if (meta?.oldestId) next.oldestId = meta.oldestId
  return rebuild(next)
}

function mergeCompactTurns(s: ViewState, incoming?: CompactTurn[], authoritative = false) {
  if (!incoming) return
  const turns = new Map(s.compactTurns?.map(t => [t.id, t]))
  const owners = new Map(s.compactTurns?.flatMap(turn => turn.entryIds.map(id => [id, turn.id] as const)))
  const byId = new Map(s.entries.map(e => [e.id, e]))
  const descendsFrom = (tail: string, ancestor: string) => {
    const seen = new Set<string>()
    let id: string | undefined = tail
    while (id && !seen.has(id)) {
      if (id === ancestor) return true
      seen.add(id)
      id = byId.get(id)?.parentId
    }
    return false
  }
  for (const turn of incoming) {
    const previous = turns.get(turn.id)
    if (!authoritative && !previous && turn.entryIds.some(id => owners.has(id) && owners.get(id) !== turn.id)) continue
    if (!authoritative && previous && previous.tailId !== turn.tailId && !descendsFrom(turn.tailId, previous.tailId)) continue
    turns.set(turn.id, turn)
  }
  s.compactTurns = [...turns.values()].sort((a, b) => a.stats.turn - b.stats.turn)
  s.loadedTurnIds = coveredTurns(s.entries, s.compactTurns)
}

export function hydrateTurn(s: ViewState, id: string, entries: Entry[]): ViewState {
  return hydrateEntries(s, [...new Map(entries.map(e => [e.id, e])).values()])
}

export function compactBoundary(s: ViewState, detail: SessionDetail, source: Pick<ViewState, 'oldestId' | 'hasMore' | 'leafId'>): ViewState {
  if (s.oldestId !== source.oldestId || !!s.hasMore !== !!source.hasMore || s.leafId !== source.leafId) return s
  const turn = detail.compactTurns?.[0]
  if (!turn) return s
  const next = { ...s }
  mergeCompactTurns(next, detail.compactTurns)
  if (next.loadedTurnIds?.includes(turn.id)) return hydrateEntries(next, detail.entries ?? [], detail)
  const end = s.entries.findIndex(e => e.id === turn.tailId)
  const entries = end >= 0 ? s.entries.slice(end + 1) : s.entries
  return hydrateEntries({ ...next, entries }, detail.entries ?? [], detail)
}

export function applyRuntimeCatalog(s: ViewState, detail: SessionDetail): ViewState {
  return {
    ...s,
    questionsSessionId: detail.questions ? detail.id : s.questionsSessionId,
    questions: detail.questions ? reconcileQuestions(s.questionsSessionId === detail.id ? s.questions ?? [] : [], detail.questions, true) : s.questions,
    runtimeReady: detail.runtime?.ready !== false,
    processes: detail.processes ? mergeProcessSnapshots(s.processes ?? [], detail.processes) : s.processes,
    agents: detail.agents ? mergeAgentSnapshots(s.agents ?? [], detail.agents) : s.agents,
    commands: detail.commands ?? s.commands,
    extensionUi: detail.extensionUi ?? s.extensionUi,
    queued: detail.queued ?? s.queued,
    extQueued: detail.extQueued ?? s.extQueued,
  }
}

function mergeEntries(index: IndexEntry[] | undefined, entries: Entry[]): Entry[] {
  const full = new Map(entries.map(e => [e.id, e]))
  if (!index?.length) return entries
  const rows = index.map(ix => {
    const body = full.get(ix.id)
    return body ? withContextEstimate(body, ix.contextEstimate) : indexToEntry(ix)
  })
  const known = new Set(index.map(ix => ix.id))
  for (const e of entries) if (!known.has(e.id)) rows.push(e)
  return rows
}

function withContextEstimate(entry: Entry, fallback?: Entry['contextEstimate']): Entry {
  if (!fallback) return entry
  const own = entry.contextEstimate
  if (Object.entries(fallback).every(([key, value]) => value == null || own?.[key as keyof NonNullable<typeof own>] != null)) return entry
  return { ...entry, contextEstimate: {
    message: own?.message ?? fallback.message,
    system: own?.system ?? fallback.system,
    tools: own?.tools ?? fallback.tools,
    summary: own?.summary ?? fallback.summary,
  } }
}

function indexToEntry(ix: IndexEntry): Entry {
  const entry: Entry = {
    type: ix.type,
    id: ix.id,
    parentId: ix.parentId,
    timestamp: ix.timestamp,
    sideband: ix.sideband,
    tokensBefore: ix.tokensBefore,
    remoteContext: ix.remoteContext,
    contextEstimate: ix.contextEstimate,
    truncated: true,
    bodyKind: 'index',
    usage: ix.usage,
  }
  if (ix.role) {
    entry.message = {
      role: ix.role,
      origin: ix.origin,
      usage: ix.usage,
      durationMs: ix.durationMs,
      ttftMs: ix.ttftMs,
      stopReason: ix.stopReason,
      toolCallId: ix.toolCallId,
      isError: ix.isError,
      toolName: ix.name,
      content: [{ type: 'text', text: ix.preview || '' }],
    }
  }
  if (ix.type === 'compaction') entry.summary = ix.preview
  if (ix.parentCallId && ix.toolCallId) {
    entry.details = {
      toolCallId: ix.toolCallId,
      toolName: ix.name,
      parentCallId: ix.parentCallId,
      cellId: ix.cellId,
      requestedToolName: ix.requestedToolName,
      durationMs: ix.durationMs,
      isError: ix.isError,
    }
  }
  return entry
}

function branchGraph(entries: Entry[], turns: CompactTurn[] = []) {
  const previous = new Map<string, string | undefined>()
  const tails = new Map<string, string | undefined>()
  let last: string | undefined
  let ordinal: number | undefined
  let tail: string | undefined
  for (const turn of turns) {
    if (ordinal != null && (turn.stats.turn !== ordinal + 1 || turn.parentId !== tail)) last = undefined
    ordinal = turn.stats.turn
    for (const id of turn.entryIds) { previous.set(id, last); last = id }
    tails.set(turn.tailId, last)
    tail = turn.tailId
  }
  return { byId: new Map(entries.map(e => [e.id, e])), previous, tails, last: entries.at(-1)?.id }
}

function walkBranch(graph: ReturnType<typeof branchGraph>, leafId?: string, stopId?: string, liveBridges = true): Entry[] {
  const { byId, previous, tails } = graph
  const active: Entry[] = []
  let id = leafId || graph.last
  if (id && !byId.has(id)) id = tails.get(id)
  const seen = new Set<string>()
  let sparsePrevious: string | undefined
  while (id && !seen.has(id)) {
    seen.add(id)
    const entry = byId.get(id)
    if (!entry) break
    active.push(entry)
    if (id === stopId) break
    if (previous.has(entry.id)) sparsePrevious = previous.get(entry.id)
    const parent = entry.parentId
    id = parent && byId.has(parent) ? parent
      : parent && tails.has(parent) ? tails.get(parent)
        : liveBridges && entry.previousId && byId.has(entry.previousId) ? entry.previousId
          : liveBridges && entry.previousId && tails.has(entry.previousId) ? tails.get(entry.previousId)
            : sparsePrevious ?? tails.get(entry.id)
  }
  return active.reverse()
}

export function leafEntries(entries: Entry[], leafId?: string, turns: CompactTurn[] = [], liveBridges = true): Entry[] {
  return walkBranch(branchGraph(entries, turns), leafId, undefined, liveBridges)
}


function normalizeTools(raw?: ToolSchema[] | unknown): ToolSchema[] {
  if (!Array.isArray(raw)) return []
  return raw.map(item => {
    if (!item || typeof item !== 'object') return { name: String(item) }
    const o = item as Record<string, unknown>
    const parameters = o.parameters ?? o.Parameters
    const format = o.format && typeof o.format === 'object' ? o.format as ToolSchema['format'] : undefined
    return {
      type: o.type != null || o.Type != null ? String(o.type ?? o.Type) : undefined,
      name: String(o.name ?? o.Name ?? ''),
      description: o.description != null || o.Description != null ? String(o.description ?? o.Description) : undefined,
      parameters: parameters && typeof parameters === 'object' ? parameters as Record<string, unknown> : undefined,
      format,
    }
  })
}

function sameTools(a: ToolSchema[], b: ToolSchema[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

function nextRequestStep(s: ViewState, turn: number): number {
  const previous = s.requests.at(-1)
  return previous?.turn === turn ? previous.step + 1 : 1
}

function inspectPrompt(
  previous: PromptSnapshot | undefined,
  prompt: PromptSnapshot,
  id: string,
  time?: number,
): PromptChange | undefined {
  if (previous && previous.system === prompt.system && sameTools(previous.tools, prompt.tools)) return undefined
  const systemChanged = previous !== undefined && previous.system !== prompt.system
  const toolsChanged = previous !== undefined && !sameTools(previous.tools, prompt.tools)
  return {
    kind: previous === undefined
      ? 'initial'
      : systemChanged && toolsChanged
        ? 'system-and-tools'
        : systemChanged ? 'system' : 'tools',
    seq: id,
    time,
    ...(previous === undefined ? {} : { previous }),
  }
}

function updateRequest(s: ViewState, id: string, patch: Partial<RequestView>) {
  let index = s.requests.length - 1
  while (index >= 0 && s.requests[index].id !== id) index--
  const previous = s.requests[index]
  if (!previous || Object.entries(patch).every(([key, value]) => previous[key as keyof RequestView] === value)) return
  s.requests[index] = { ...previous, ...patch }
  for (let i = s.records.length - 1; i >= 0; i--) {
    const record = s.records[i]
    if (record.requestId !== id) continue
    s.records[i] = {
      ...record,
      ...(patch.durationMs === undefined ? {} : { durationMs: patch.durationMs }),
      ...(patch.ttftMs === undefined ? {} : { ttftMs: patch.ttftMs }),
      ...(patch.status === undefined ? {} : { running: patch.status === 'running', error: patch.status === 'error' }),
    }
    if (record.requestOnly) break
  }
}

function applyRequestHeader(
  s: ViewState,
  id: string,
  system: string,
  rawTools: ToolSchema[] | unknown,
  stamp?: string | number,
  metadata?: Pick<RequestView, 'provider' | 'model' | 'thinkingEffort'>,
) {
  const tools = normalizeTools(rawTools)
  const prompt: PromptSnapshot = { ...metadata, system, tools }
  const previous = s.promptState
  const startedAt = tsMs(undefined, stamp)
  const change = inspectPrompt(previous, prompt, id, startedAt)
  const turn = s.turn || 1
  const step = nextRequestStep(s, turn)
  const request: RequestView = {
    id,
    turnId: s.turnId,
    turn,
    step,
    startedAt,
    status: 'running',
    ...metadata,
    prompt,
    ...(change === undefined ? {} : { promptChange: change }),
  }
  s.promptState = prompt
  s.currentRequestId = id
  s.requests.push(request)

  s.records.push({
    id: `request:${id}`,
    kind: 'assistant',
    turn,
    step,
    requestId: id,
    requestOnly: true,
    preview: '',
    startedAt,
    running: true,
  })
  if (change === undefined) return
  const systemId = `system:${id}`
  s.records.push({
    id: systemId,
    kind: 'system',
    turn,
    step,
    requestId: id,
    preview: previewOf(system || `${tools.length} tools`),
    system,
    tools,
    prompt,
    promptChange: change,
    previousSystem: previous?.system,
    previousTools: previous?.tools,
    input: system,
    output: tools,
    startedAt,
  })
}

function applyEntry(s: ViewState, e: Entry, withNode = true) {
	if (e.type === 'context_usage') {
		s.contextUsage = { usedTokens: e.usedTokens ?? 0, contextWindow: e.contextWindow ?? 0, estimated: !!e.estimated }
		return
	}
  if (e.type === 'request_header') {
    if ((!e.system && !e.tools?.length && e.truncated && !e.promptUnchanged) || (e.promptUnchanged && !s.promptState)) {
      const turn = s.turn || 1
      const step = nextRequestStep(s, turn)
      const startedAt = tsMs(undefined, e.timestamp)
      s.promptState = undefined
      s.currentRequestId = e.id
      s.requests.push({ id: e.id, turnId: s.turnId, turn, step, startedAt, status: 'running', provider: e.provider, model: e.modelId, thinkingEffort: e.thinkingEffort })
      s.records.push({ id: `request:${e.id}`, requestId: e.id, kind: 'assistant', turn, step, requestOnly: true, preview: '', startedAt, running: true })
      s.records.push({ id: `system:${e.id}`, requestId: e.id, kind: 'system', turn, step, preview: '', truncated: true })
      return
    }
    if (e.promptUnchanged) {
      applyRequestHeader(s, e.id, s.promptState?.system ?? '', s.promptState?.tools ?? [], e.timestamp, {
        provider: e.provider,
        model: e.modelId,
        thinkingEffort: e.thinkingEffort,
      })
      return
    }
    applyRequestHeader(s, e.id, e.system ?? '', e.tools, e.timestamp, {
      provider: e.provider,
      model: e.modelId,
      thinkingEffort: e.thinkingEffort,
    })
    const record = s.records.find(r => r.id === `system:${e.id}`)
    if (record) record.truncated = e.truncated
    return
  }
  if (e.type === 'message' && e.message) {
    applyMessage(s, e.message, e.id, e.timestamp, e.parentId, e.truncated, withNode, e.cacheMiss)
    return
  }
  if (e.details && typeof e.details === 'object') {
    const event = { ...e.details as LoopEvent, type: e.type }
    if (isNestedToolEvent(event)) {
      applyNestedToolEvent(s, event, withNode, e.timestamp, e.truncated)
      return
    }
  }
  if (e.type === 'run_aborted') {
    const details = e.details && typeof e.details === 'object' ? e.details as { reason?: string; source?: string; runId?: string } : {}
    if (withNode) s.nodes.push({
      kind: 'cancellation',
      id: e.id,
      runId: details.runId,
      reason: details.reason,
      source: details.source,
      ts: tsMs(undefined, e.timestamp),
    })
    return
  }
	if (e.type === 'patch_apply_updated' && e.details && typeof e.details === 'object') {
		const details = e.details as { toolCallId?: string; toolName?: string; partialResult?: unknown }
		if (details.toolCallId) patchApplyPreview(s, details.toolCallId, details.toolName, details.partialResult, e.timestamp)
		return
	}
  if (e.type === 'compaction') {
    const summary = e.summary || ''
    if (withNode) s.nodes.push({ kind: 'compaction', id: e.id, summary, ts: tsMs(undefined, e.timestamp), tokensBefore: e.tokensBefore, truncated: e.truncated })
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
}

function compactDetails(details?: unknown): { reason: string; status: string; ok?: boolean } {
  if (details && typeof details === 'object') {
    const d = details as { reason?: string; status?: string; ok?: boolean }
    return { reason: d.reason || '', status: d.status || '', ok: d.ok }
  }
  return { reason: '', status: '' }
}

function compactTerminal(status: string, reason: string, ok?: boolean) {
  const empty = status === 'empty' || reason === 'empty'
  const failed = !empty && (status === 'failed' || status === 'cancelled' || ok === false)
  return { empty, failed }
}

function applyCompactEvent(
  s: ViewState, id: string, type: string, details?: unknown, stamp?: string | number,
  options: { withNode?: boolean; running?: boolean; summaryId?: string; truncated?: boolean; paired?: boolean } = {},
) {
  const { reason, status, ok } = compactDetails(details)
  const start = type === 'compaction_start'
  const running = start && (options.running ?? true)
  const terminal = compactTerminal(status, reason, ok)
  const failed = terminal.failed || (start && !running)
  const unknown = !start && !status && ok == null
  const preview = running ? `Compacting (${reason || 'auto'})…`
    : unknown ? 'Compaction status not loaded'
    : terminal.empty ? `Nothing to compact (${reason || 'auto'})`
      : `Compacted (${reason || 'auto'})${failed ? ' (failed)' : ''}`
  const pending = start || options.paired ? undefined : [...s.records].reverse().find(r => r.kind === 'compact' && r.running)
  const rowId = pending?.id ?? id
  const record: TrajRecord = {
    id: rowId, kind: 'compact', turn: s.turn || 1, turnId: s.turnId,
    preview, running, startedAt: tsMs(undefined, stamp), truncated: options.truncated,
  }
  if (pending) s.records = s.records.map(r => r === pending ? { ...r, preview, running } : r)
  else s.records.push(record)
  const summary = options.summaryId && s.nodes.some(n => n.kind === 'compaction' && !n.lifecycle && n.id === options.summaryId)
  if (summary) {
    s.nodes = s.nodes.filter(n => !(n.kind === 'compaction' && n.lifecycle && n.id === rowId))
  } else if (options.withNode !== false) {
    const node: ChatNode = {
      kind: 'compaction', id: rowId, turnId: s.turnId, summary: '', lifecycle: true,
      running, failed, empty: terminal.empty, unknown,
      ts: tsMs(undefined, stamp), truncated: options.truncated,
    }
    const at = s.nodes.findIndex(n => n.id === rowId && n.kind === 'compaction' && n.lifecycle)
    if (at < 0) s.nodes.push(node)
    else s.nodes[at] = node
  }
}

function applyMessage(s: ViewState, m: Message, id: string, stamp?: string | number, parentId?: string, truncated?: boolean, withNode = true, cacheMiss?: CacheMiss) {
  if (m.role === 'user') {
    const text = messageText(m)
    if (s.turnId !== id && (isHumanPrompt(m.origin) || !s.turnId)) {
      const firstHuman = s.turnId && isHumanPrompt(m.origin)
        && !s.nodes.some(n => n.kind === 'user' && isHumanPrompt(n.origin))
        && !leafEntries(s.allEntries, s.leafId, s.compactTurns).some(isHumanEntry)
      if (firstHuman) {
        const previous = s.turnId!
        remapTurnIdentity(s, previous, id)
        s.compactTurns = s.compactTurns?.map(t => t.id === id ? {
          ...t, cumulativeElapsedMs: 0,
          stats: { ...t.stats, startedAt: tsMs(m, stamp), elapsedMs: 0 },
        } : t)
        s.loadedTurnIds = s.loadedTurnIds?.filter(turn => turn !== id)
      } else s.turn += 1
      s.turnId = id
    }
    if (withNode) s.nodes.push({ kind: 'user', id, turnId: s.turnId, parentId, text, content: m.content ?? [], ts: tsMs(m, stamp), origin: m.origin, clientRequestId: m.clientRequestId, completion: m.completion, external: m.external, truncated, questionReply: m.questionReply })
    s.records.push({
      id,
      turnId: s.turnId,
	  parentId,
      kind: 'user',
      truncated,
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
    const request = s.currentRequestId ? s.requests.find(item => item.id === s.currentRequestId) : undefined
    const requestId = request?.id
    if (withNode) s.nodes.push({
      kind: 'assistant',
      id,
	  parentId,
      text,
      thinking,
      usage: m.usage,
      cacheMiss,
      ttftMs: m.ttftMs,
      latencyMs: m.latencyMs,
      error: m.errorMessage,
      cancelReason: m.cancelReason,
      cancelSource: m.cancelSource,
	  stopReason: m.stopReason,
      ts: tsMs(m, stamp),
      truncated,
      images: (m.content ?? []).filter(c => c.type === 'image' && c.data).map(c => ({ data: c.data!, mimeType: c.mimeType || 'image/png' })),
    })
    s.records.push({
      id,
      kind: 'assistant',
      turn: s.turn || 1,
      truncated,
      step: request?.step,
      requestId,
      preview: previewOf(thinking ? thinking : text),
      output: text,
      input: thinking || undefined,
      usage: m.usage,
      durationMs: m.latencyMs,
      ttftMs: m.ttftMs,
      startedAt: tsMs(m, stamp),
      error: !!m.errorMessage,
      sourceBlocks: m.content ?? [],
    })
    if (requestId) {
      updateRequest(s, requestId, {
        status: m.errorMessage ? 'error' : 'complete',
        completedAt: tsMs(m, stamp),
        resultId: id,
        usage: m.usage,
        ttftMs: m.ttftMs,
        durationMs: m.latencyMs,
        error: m.errorMessage,
      })
    }
    for (const c of m.content ?? []) {
      if (c.type !== 'toolCall' || !c.id) continue
	  const args = c.arguments ?? (c.input !== undefined ? { input: c.input } : undefined)
      if (withNode) {
        if (s.nodes.some(n => n.kind === 'tool' && n.id === c.id)) {
          patchTool(s, c.id, { name: c.name, args, truncated })
          continue
        }
        s.nodes.push({
          kind: 'tool',
          id: c.id,
          name: c.name || 'tool',
          args,
          truncated,
        })
      }
      s.records.push({
        id: c.id,
        kind: 'tool',
        turn: s.turn || 1,
        step: request?.step,
        requestId,
        preview: previewOf(c.name + ' ' + compactArgs(args)),
        input: args,
        name: c.name,
        startedAt: tsMs(m, stamp),
        sourceBlocks: [c],
      })
    }
    return
  }
  if (m.role === 'toolResult') {
    const text = messageText(m)
    const tid = m.toolCallId || id
    const finishedAt = tsMs(m, stamp)
    const startedAt = finishedAt != null && m.durationMs != null
      ? finishedAt - Math.max(0, m.durationMs)
      : undefined
    const haveToolNode = withNode && s.nodes.find(n => n.kind === 'tool' && n.id === tid)
    if (!haveToolNode) {
      if (withNode) s.nodes.push({
        kind: 'tool',
        id: tid,
        name: m.toolName || 'tool',
        result: text,
        isError: m.isError,
        durationMs: m.durationMs,
        details: m.details,
        truncated,
      })
      s.records.push({
        id: tid,
        kind: 'tool',
        turn: s.turn || 1,
        preview: previewOf((m.toolName || 'tool') + ' ' + text),
        output: text,
        name: m.toolName,
        durationMs: m.durationMs,
        startedAt,
        error: !!m.isError,
      })
    }
    patchTool(s, tid, {
      result: text,
      isError: m.isError,
      durationMs: m.durationMs,
	  startedAt,
	  outputBlocks: m.content ?? [],
	  details: m.details,
      running: false,
      name: m.toolName,
      truncated: truncated || !!(haveToolNode && haveToolNode.truncated),
	  })
	  if (s.currentRequestId) {
	    const request = s.requests.find(item => item.id === s.currentRequestId)
	    if (request?.status === 'running') {
	      updateRequest(s, request.id, { status: 'running' })
	    }
	  }
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

function patchApplyPreview(s: ViewState, id: string, name?: string, details?: unknown, stamp?: string | number) {
	if (!s.nodes.some(n => n.kind === 'tool' && n.id === id)) {
		const startedAt = tsMs(undefined, stamp) ?? Date.now()
		s.nodes.push({ kind: 'tool', id, name: name || 'apply_patch', details, running: true, startedAt })
		s.records.push({
			id,
			kind: 'tool',
			turn: s.turn || 1,
			preview: name || 'apply_patch',
			name: name || 'apply_patch',
			details,
			running: true,
			startedAt,
		})
		return
	}
	patchTool(s, id, { name: name || 'apply_patch', details, running: true })
}

function patchTool(s: ViewState, id: string, patch: Partial<Extract<ChatNode, { kind: 'tool' }>> & { outputBlocks?: Content[]; sourceBlocks?: Content[] }) {
  const { outputBlocks, sourceBlocks, ...nodePatch } = patch
  s.nodes = s.nodes.map(n => {
    if (n.kind !== 'tool' || n.id !== id) return n
    return {
      ...n,
      ...nodePatch,
      ...(outputBlocks === undefined ? {} : { outputBlocks }),
      running: patch.running ?? n.running,
    }
  })
  s.records = s.records.map(r => {
    if (r.kind !== 'tool' || r.id !== id) return r
    const result = patch.result ?? (typeof r.output === 'string' ? r.output : '')
    return {
      ...r,
      output: result,
      truncated: patch.truncated ?? r.truncated,
      running: patch.running ?? r.running,
      error: patch.isError ?? r.error,
      durationMs: patch.durationMs ?? r.durationMs,
      startedAt: patch.startedAt ?? r.startedAt,
      name: patch.name || r.name,
      ...(patch.parentCallId === undefined ? {} : { parentCallId: patch.parentCallId }),
      ...(patch.cellId === undefined ? {} : { cellId: patch.cellId }),
      ...(patch.requestedToolName === undefined ? {} : { requestedToolName: patch.requestedToolName }),
      details: patch.details ?? r.details,
      ...(outputBlocks === undefined ? {} : { outputBlocks }),
      ...(sourceBlocks === undefined ? {} : { sourceBlocks }),
      preview: previewOf((patch.name || r.name || 'tool') + ' ' + (result || compactArgs(r.input))),
    }
  })
}

function isNestedToolEvent(event: LoopEvent): boolean {
  return !!event.parentCallId && !!event.toolCallId
    && ['tool_execution_start', 'tool_execution_update', 'tool_execution_end'].includes(event.type)
}

function applyNestedToolEvent(s: ViewState, event: LoopEvent, withNode = true, stamp?: string, truncated?: boolean) {
  const id = event.toolCallId!
  if (id === event.parentCallId) {
    if (event.type === 'tool_execution_update') patchTool(s, id, { details: event.partialResult })
    return
  }
  const metadata = {
    parentCallId: event.parentCallId,
    cellId: event.cellId,
    requestedToolName: event.requestedToolName,
  }
  const node = s.nodes.find(n => n.kind === 'tool' && n.id === id)
  const record = s.records.find(r => r.kind === 'tool' && r.id === id)
  const finished = event.type === 'tool_execution_end'
  const timestamp = event.timestamp ?? tsMs(undefined, stamp)
  const startedAt = record?.startedAt ?? (timestamp != null && finished && event.durationMs != null
    ? timestamp - Math.max(0, event.durationMs) : timestamp)
  const running = !finished && s.busy && !(node && hasResult(node))
  const name = event.toolName || record?.name || 'tool'
  if (withNode && !node) {
    s.nodes.push({ kind: 'tool', id, turnId: s.turnId, name, args: event.args, startedAt, running, truncated, ...metadata })
  }
  if (!record) {
    const parent = s.records.find(r => r.kind === 'tool' && r.id === event.parentCallId)
    const requestId = parent?.requestId ?? s.currentRequestId
    const request = s.requests.find(r => r.id === requestId)
    s.records.push({
      id, kind: 'tool', turnId: s.turnId, turn: s.turn || 1,
      step: parent?.step ?? request?.step, requestId,
      preview: previewOf(name + ' ' + compactArgs(event.args)),
      name, input: event.args, startedAt, running, truncated, ...metadata,
    })
  }
  const result = event.result && typeof event.result === 'object' ? event.result as Record<string, unknown> : undefined
  const content = result?.content ?? result?.Content
  patchTool(s, id, {
    ...metadata, name, startedAt, truncated, running,
    ...(event.type === 'tool_execution_update' ? { details: event.partialResult } : {}),
    ...(finished ? {
      result: toolResultText(event.result),
      isError: event.isError,
      durationMs: event.durationMs,
      details: result?.details ?? result?.Details,
      ...(Array.isArray(content) ? { outputBlocks: content as Content[] } : {}),
    } : {}),
  })
}

function persistLiveEntry(s: ViewState, value: Entry) {
  const known = s.allEntries.find(e => e.id === value.id)
  const onBranch = known && leafEntries(s.allEntries, s.leafId, s.compactTurns).some(e => e.id === value.id)
  const entry: Entry = {
    ...value,
    parentId: value.parentId ?? known?.parentId ?? (s.leafId !== value.id ? s.leafId : undefined),
    previousId: known?.previousId ?? (!onBranch && s.leafId !== value.id ? s.leafId : undefined),
    timestamp: value.timestamp ?? (value.message?.timestamp ? new Date(value.message.timestamp).toISOString() : undefined),
  }
  addEntries(s, [entry])
  s.allEntries = mergeEntries(s.index, s.entries)
  if (!onBranch) s.leafId = entry.id
  if (entry.message?.role === 'toolResult' && entry.message.toolCallId && s.toolStates) {
    s.toolStates = { ...s.toolStates }
    delete s.toolStates[entry.message.toolCallId]
  }
}

function rememberTool(s: ViewState, id?: string) {
  const node = s.nodes.find(n => n.kind === 'tool' && n.id === id)
  if (node?.kind === 'tool') s.toolStates = { ...s.toolStates, [node.id]: node }
}

function settleToolBatch(s: ViewState) {
  s.nodes = s.nodes.map(n => n.kind === 'tool' && n.running && !n.parentCallId ? { ...n, running: false } : n)
  s.records = s.records.map(r => r.kind === 'tool' && r.running && !r.parentCallId ? { ...r, running: false } : r)
  if (s.toolStates) s.toolStates = Object.fromEntries(Object.entries(s.toolStates).map(([id, n]) => [id, n.parentCallId ? n : { ...n, running: false }]))
}

export function applyEvent(s: ViewState, ev: LoopEvent): ViewState {
  const next: ViewState = {
    ...s,
    liveRevision: (s.liveRevision ?? 0) + 1,
    nodes: s.nodes.slice(),
    records: s.records.slice(),
    requests: s.requests.slice(),
  }
  if (isNestedToolEvent(ev)) {
    if (ev.entryId) {
      const known = next.entries.some(entry => entry.id === ev.entryId && !entry.truncated && entry.details)
      persistLiveEntry(next, {
        type: ev.type, id: ev.entryId, parentId: ev.parentId,
        timestamp: ev.timestamp == null ? undefined : new Date(ev.timestamp).toISOString(),
        details: {
          toolCallId: ev.toolCallId, parentCallId: ev.parentCallId, cellId: ev.cellId,
          toolName: ev.toolName, requestedToolName: ev.requestedToolName,
          timestamp: ev.timestamp, durationMs: ev.durationMs, isError: ev.isError,
          args: ev.args, partialResult: ev.partialResult, result: ev.result,
        },
      })
      if (next.toolStates?.[ev.toolCallId!]) {
        next.toolStates = { ...next.toolStates }
        delete next.toolStates[ev.toolCallId!]
      }
      if (!known) applyNestedToolEvent(next, ev)
      return next
    }
    applyNestedToolEvent(next, { ...ev, timestamp: ev.timestamp ?? Date.now() })
    if (ev.toolCallId !== ev.parentCallId) rememberTool(next, ev.toolCallId)
    return next
  }
  switch (ev.type) {
    case 'question_updated':
      if (ev.question) {
        const prior = next.questions ?? []
        const old = prior.find(q => q.questionItemId === ev.question!.questionItemId)
        if (!old || old.revision <= ev.question.revision) next.questions = [...prior.filter(q => q.questionItemId !== ev.question!.questionItemId), ev.question]
      }
      break
	case 'context_usage':
		next.contextUsage = { usedTokens: ev.usedTokens ?? 0, contextWindow: ev.contextWindow ?? 0, estimated: !!ev.estimated }
		break
    case 'agent_start':
      next.busy = true
      next.error = null
      break
    case 'agent_end':
      settleToolBatch(next)
      next.busy = false
      next.stopping = false
      next.nodes = next.nodes.map(n =>
        n.kind === 'assistant' && n.streaming ? { ...n, streaming: false }
          : n.kind === 'tool' && n.running ? { ...n, running: false }
            : n.kind === 'compaction' && n.lifecycle && n.running ? { ...n, running: false, failed: true } : n,
      )
      next.records = next.records.map(r => r.running ? { ...r, running: false } : r)
      next.requests = next.requests.map(request => request.status === 'running'
        ? { ...request, status: 'complete', completedAt: request.completedAt ?? Date.now() }
        : request)
      break
    case 'steer_accepted':
      if (ev.message?.content && isHumanPrompt(ev.message.origin)) return appendOptimisticUser(s, ev.message)
      return s
    case 'run_aborted':
      next.stopping = true
      {
        const id = ev.entryId || `run-aborted:${ev.runId || next.liveRevision}`
        const repeatedLiveFrame = !!s.stopping && next.nodes.some(node =>
          node.kind === 'cancellation' && node.reason === ev.reason && node.source === ev.cancelSource)
        if (!next.nodes.some(node => node.id === id ||
          (node.kind === 'cancellation' && ev.runId && node.runId === ev.runId)) &&
          !repeatedLiveFrame) {
          next.nodes.push({
            kind: 'cancellation',
            id,
            runId: ev.runId,
            reason: ev.reason,
            source: ev.cancelSource,
            ts: ev.timestamp,
          })
        }
        if (ev.entryId) persistLiveEntry(next, {
          type: 'run_aborted', id: ev.entryId, parentId: ev.parentId,
          timestamp: ev.timestamp ? new Date(ev.timestamp).toISOString() : undefined,
          details: { runId: ev.runId, reason: ev.reason, source: ev.cancelSource },
        })
      }
      break
    case 'extension_notice':
    case 'extension_ui_prompt':
      break
    case 'extension_ui_updated':
    case 'runtime_ready':
      break
    case 'request_header': {
      settleToolBatch(next)
      const previous = next.promptState
      const system = ev.promptUnchanged ? previous?.system ?? '' : ev.system ?? ''
      const tools = ev.promptUnchanged ? previous?.tools ?? [] : ev.tools
      applyRequestHeader(next, ev.entryId || `live-request-${next.requests.length + 1}`, system, tools, ev.timestamp || Date.now(), {
        provider: ev.provider,
        model: ev.model,
      })
      break
    }
    case 'message_start':
    case 'message_end':
    case 'message_update':
      applyLiveMessage(next, ev)
      if (ev.type === 'message_end' && ev.entryId && ev.message) persistLiveEntry(next, { type: 'message', id: ev.entryId, parentId: ev.parentId, message: ev.message, cacheMiss: ev.cacheMiss })
      break
    case 'tool_execution_start':
      if (ev.toolCallId) {
        const startedAt = ev.timestamp ?? Date.now()
        const existing = next.nodes.some(n => n.kind === 'tool' && n.id === ev.toolCallId)
        if (!existing) {
          next.nodes.push({
            kind: 'tool',
            id: ev.toolCallId,
            name: ev.toolName || 'tool',
            args: ev.args,
            startedAt,
            running: true,
          })
          next.records.push({
            id: ev.toolCallId,
            kind: 'tool',
            turn: next.turn || 1,
            step: next.requests.find(item => item.id === next.currentRequestId)?.step,
            requestId: next.currentRequestId,
            preview: previewOf((ev.toolName || 'tool') + ' ' + compactArgs(ev.args)),
            input: ev.args,
            name: ev.toolName,
            running: true,
            startedAt,
          })
        } else {
          patchTool(next, ev.toolCallId, { running: true, args: ev.args, name: ev.toolName, startedAt })
        }
        next.records = next.records.map(r => r.kind === 'tool' && r.id === ev.toolCallId
          ? { ...r, startedAt, running: true }
          : r)
      }
      rememberTool(next, ev.toolCallId)
      break
	case 'patch_apply_updated':
		if (ev.toolCallId) {
			patchApplyPreview(next, ev.toolCallId, ev.toolName, ev.partialResult, ev.timestamp)
			rememberTool(next, ev.toolCallId)
		}
		break
    case 'compaction_start':
    case 'compaction_end': {
      if (ev.lifecycleEntryId) {
        const known = next.allEntries.some(e => e.id === ev.lifecycleEntryId)
        persistLiveEntry(next, {
          id: ev.lifecycleEntryId, type: ev.type, parentId: ev.parentId,
          timestamp: ev.timestamp == null ? undefined : new Date(ev.timestamp).toISOString(),
          details: { reason: ev.reason, status: ev.status, ok: ev.ok, entryId: ev.entryId, tokensBefore: ev.tokensBefore },
        })
        if (ev.type === 'compaction_start' && !known) next.busy = true
        return rebuild(next)
      }
      applyCompactEvent(next, `compact-live-${next.liveRevision}`, ev.type, ev, ev.timestamp ?? Date.now(), { summaryId: ev.entryId })
      break
    }
    case 'process_updated':
      if (ev.process) next.processes = mergeProcessSnapshots(next.processes ?? [], [ev.process], true)
      break
    case 'agent_updated':
      if (ev.agent) next.agents = mergeAgentSnapshots(next.agents ?? [], [ev.agent], true)
      break
    case 'tool_execution_end':
      if (ev.toolCallId) {
		const result = ev.result && typeof ev.result === 'object' ? ev.result as Record<string, unknown> : undefined
		const resultDetails = result?.details ?? result?.Details
		const content = result?.content ?? result?.Content
		patchTool(next, ev.toolCallId, {
          result: toolResultText(ev.result),
          isError: ev.isError,
          running: false,
          durationMs: ev.durationMs,
          name: ev.toolName,
		  details: resultDetails,
		  ...(Array.isArray(content) ? { outputBlocks: content as Content[] } : {}),
        })
      }
      rememberTool(next, ev.toolCallId)
      break
    default:
      break
  }
  next.nodes = next.nodes.map(n => n.turnId ? n : { ...n, turnId: next.turnId })
  next.records = next.records.map(r => r.turnId ? r : { ...r, turnId: next.turnId })
  return next
}

function applyLiveMessage(s: ViewState, ev: LoopEvent) {
  const m = ev.message ?? ev.assistantMessageEvent?.partial
  if (!m) return
  if (m.role === 'user') {
    if (!isHumanPrompt(m.origin) && ev.type !== 'message_end') return
    if (!ev.entryId && !userMessageIdentity(m)) return
    upsertUser(s, m, ev.entryId, ev.parentId)
    return
  }
  if (m.role === 'toolResult') {
    applyMessage(s, m, m.toolCallId || `live-tr-${s.nodes.length}`, undefined)
    return
  }
  if (m.role !== 'assistant') return
  if (ev.type === 'message_start' || ev.type === 'message_end' || lastStreamingAssistant(s) < 0) settleToolBatch(s)

  if (ev.type === 'message_end' && ev.entryId && s.nodes.some(n => n.id === ev.entryId)) {
    const stale = lastStreamingAssistant(s)
    if (stale >= 0) dropStreamingAssistant(s, stale)
    if (ev.cacheMiss) {
      s.nodes = s.nodes.map(n => n.kind === 'assistant' && n.id === ev.entryId
        ? { ...n, cacheMiss: ev.cacheMiss }
        : n)
    }
    return
  }

  if (ev.type === 'message_start') {
    const live = lastStreamingAssistant(s)
    if (live >= 0) {
      const node = s.nodes[live] as Extract<ChatNode, { kind: 'assistant' }>
      s.nodes[live] = { ...node, text: messageText(m), thinking: messageThinking(m), streaming: true, ts: m.timestamp ?? node.ts }
      if (s.currentRequestId) updateRequest(s, s.currentRequestId, { status: 'running' })
      return
    }
    pushStreamingAssistant(s, m, ev.display)
    return
  }

  const idx = lastStreamingAssistant(s)
  if (idx < 0) {
    if (ev.type === 'message_update') {
      pushStreamingAssistant(s, m, ev.display)
      return
    }
    if (ev.type === 'message_end') {
      applyMessage(s, m, ev.entryId || `live-asst-${s.nodes.length}`, undefined, undefined, false, true, ev.cacheMiss)
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
    display: ev.display,
    renderKey: node.renderKey ?? node.id,
    ts: m.timestamp ?? node.ts,
    usage: m.usage ?? node.usage,
    cacheMiss: ev.cacheMiss ?? node.cacheMiss,
    ttftMs: m.ttftMs ?? node.ttftMs,
    latencyMs: m.latencyMs ?? node.latencyMs,
    error: m.errorMessage || node.error,
	cancelReason: m.cancelReason || node.cancelReason,
	cancelSource: m.cancelSource || node.cancelSource,
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

  if (s.currentRequestId) {
    updateRequest(s, s.currentRequestId, {
      status: ev.type === 'message_end' && m.errorMessage ? 'error' : ev.type === 'message_end' ? 'complete' : 'running',
      completedAt: ev.type === 'message_end' ? tsMs(m) : undefined,
      resultId: ev.type === 'message_end' ? (ev.entryId || node.id) : undefined,
      usage: m.usage,
      ttftMs: m.ttftMs,
      durationMs: m.latencyMs,
      error: m.errorMessage,
    })
  }

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
		step: s.requests.find(item => item.id === s.currentRequestId)?.step,
		requestId: s.currentRequestId,
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

function pushStreamingAssistant(s: ViewState, m: Message, display?: LoopEvent['display']) {
  const id = `live-asst-${s.nodes.length}`
  const ts = m.timestamp
  const renderKey = display?.seq ? `stream:${display.runId}:${display.seq}` : id
  s.nodes.push({ kind: 'assistant', id, renderKey, display, text: messageText(m), thinking: messageThinking(m), streaming: true, ts })
  s.records.push({
    id,
    kind: 'assistant',
    turn: s.turn || 1,
    step: s.requests.find(item => item.id === s.currentRequestId)?.step,
    requestId: s.currentRequestId,
    preview: previewOf(messageText(m) || messageThinking(m) || '…'),
    running: true,
    startedAt: ts ?? Date.now(),
  })
  if (s.currentRequestId) updateRequest(s, s.currentRequestId, { status: 'running' })
}

function dropStreamingAssistant(s: ViewState, idx: number) {
  const node = s.nodes[idx]
  if (!node || node.kind !== 'assistant') return
  s.nodes.splice(idx, 1)
  s.records = s.records.filter(r => r.id !== node.id)
}

export type UserRequest = { id: string; title: string }

export function requestTitle(text: string, content?: Content[]): string {
  const line = text.split('\n').find(l => l.trim())?.replace(/\s+/g, ' ').trim()
  if (line) return line
  if (!content) return ''
  for (const c of content) {
    if (c.type !== 'image' && c.type !== 'file' && c.type !== 'workspace_file') continue
    const name = (c.name || c.path || '').trim()
    if (name) return name
  }
  return ''
}

export function userRequests(entries: Entry[], leafId?: string, nodes: ChatNode[] = [], turns: CompactTurn[] = []): UserRequest[] {
  const out: UserRequest[] = []
  const seen = new Set<string>()
  for (const e of leafEntries(entries, leafId, turns)) {
    if (e.type !== 'message' || e.message?.role !== 'user' || !isHumanPrompt(e.message.origin)) continue
    seen.add(e.id)
    out.push({ id: e.id, title: requestTitle(messageText(e.message), e.message.content) })
  }
  for (const n of reconcileUserNodes(nodes)) {
    if (n.kind !== 'user' || !isHumanPrompt(n.origin) || seen.has(n.id)) continue
    seen.add(n.id)
    out.push({ id: n.id, title: requestTitle(n.text, n.content) })
  }
  return out
}

let optimisticSequence = 0

function remapTurnIdentity(s: ViewState, previous: TurnId, id: TurnId) {
  s.nodes = s.nodes.map(n => n.turnId === previous ? { ...n, turnId: id } : n)
  s.records = s.records.map(r => r.turnId === previous ? { ...r, turnId: id } : r)
  s.requests = s.requests.map(r => r.turnId === previous ? { ...r, turnId: id } : r)
  s.compactTurns = s.compactTurns?.map(t => t.id === previous ? { ...t, id } : t)
  s.loadedTurnIds = s.loadedTurnIds?.map(turn => turn === previous ? id : turn)
}

function upsertUser(s: ViewState, message: Message, entryId?: string, parentId?: string) {
  const identity = userMessageIdentity(message)
  const at = s.nodes.findIndex(n => n.kind === 'user' && (
    (entryId && n.id === entryId) ||
    (identity && userMessageIdentity(n) === identity && (!entryId || isTransientUserId(n.id)))
  ))
  if (at < 0) {
    applyMessage(s, message, entryId || `opt-user-${++optimisticSequence}`, message.timestamp ?? Date.now(), parentId)
    return
  }
  const previous = s.nodes[at] as Extract<ChatNode, { kind: 'user' }>
  const id = entryId || previous.id
  const turnId = previous.turnId === previous.id ? id : previous.turnId
  s.nodes[at] = {
    ...previous, id, turnId, parentId: parentId ?? previous.parentId,
    questionReply: message.questionReply ?? previous.questionReply,
    content: message.content ?? previous.content, text: messageText(message),
    ts: message.timestamp ?? previous.ts, origin: message.origin ?? previous.origin,
    clientRequestId: message.clientRequestId ?? previous.clientRequestId, completion: message.completion ?? previous.completion, external: message.external ?? previous.external,
  }
  if (s.turnId === previous.id) s.turnId = id
  remapTurnIdentity(s, previous.id, id)
  s.records = s.records.map(r => ({
    ...r,
    ...(r.turnId === previous.id ? { turnId: id } : {}),
    ...(r.id === previous.id ? { id, startedAt: message.timestamp ?? r.startedAt, preview: previewOf(messageText(message)) } : {}),
  }))
}

export function appendOptimisticUser(s: ViewState, content: Content[] | Message, clientRequestId?: string): ViewState {
  const message: Message = Array.isArray(content) ? { role: 'user', content, clientRequestId } : content
  const identity = userMessageIdentity(message)
  const known = identity && (s.nodes.some(n => n.kind === 'user' && userMessageIdentity(n) === identity)
    || leafEntries(s.allEntries, s.leafId, s.compactTurns).some(e => e.message?.role === 'user' && userMessageIdentity(e.message) === identity))
  if (known) return s
  const next = {
    ...s, nodes: s.nodes.slice(), records: s.records.slice(), busy: true, error: null,
    liveRevision: s.liveRevision + 1,
  }
  upsertUser(next, message)
  return next
}

export type SessionStats = {
  turns: number
  steps: number
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  hasCost: boolean
  cost: number
  ttftMs: number
  tps: number | null
  turnStartedAt: number
  elapsedMs: number
  turnElapsedMs: number
}

function emptyStats(): SessionStats {
  return {
    turns: 0, steps: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
    hasCost: false, cost: 0, ttftMs: 0, tps: null, turnStartedAt: 0,
    elapsedMs: 0, turnElapsedMs: 0,
  }
}

function activePath(s: ViewState): Entry[] {
  return leafEntries(s.allEntries, s.leafId, s.compactTurns).reverse()
}

function snapshotSuffix(path: Entry[], tailId: string): Entry[] {
  if (!tailId) return path.slice()
  const tail = path.findIndex(entry => entry.id === tailId)
  if (tail >= 0) return path.slice(0, tail)
  const child = path.findIndex(entry => entry.parentId === tailId || entry.previousId === tailId)
  return child >= 0 ? path.slice(0, child + 1) : []
}

export function sessionStats(s: ViewState): SessionStats {
  const path = activePath(s)
  const counted = new Set(path.map(e => e.id))
  const out = emptyStats()
  const seen = new Set<TurnId>()
  let ttftSum = 0
  let ttftCount = 0
  let decodeMs = 0
  let decodeTokens = 0
  const add = (t: Omit<TurnStats, 'turnId'>) => {
    out.input += t.input
    out.output += t.output
    out.cacheRead += t.cacheRead
    out.cacheWrite += t.cacheWrite
    if (t.hasCost) { out.hasCost = true; out.cost += t.cost }
    if (t.ttftMs > 0) { ttftSum += t.ttftMs; ttftCount += 1 }
    if (t.tps != null && t.tps > 0 && t.output > 0) {
      decodeTokens += t.output
      decodeMs += t.output / t.tps * 1_000
    }
  }
  const projected = projectTurnStats(s.nodes, s.turnBase, s.compactTurns)
  for (const t of projected.values()) {
    seen.add(t.turnId)
    add(t)
  }
  for (const turn of s.compactTurns ?? []) {
    if (seen.has(turn.id)) continue
    seen.add(turn.id)
    add(turn.stats)
  }
  out.ttftMs = ttftCount > 0 ? ttftSum / ttftCount : 0
  out.tps = decodeMs > 0 ? decodeTokens / (decodeMs / 1_000) : null
  for (const e of path) {
    if (isHumanEntry(e)) { out.turnStartedAt = tsMs(e.message, e.timestamp) ?? 0; break }
  }
  if (!out.turnStartedAt) {
    const first = path.slice().reverse().find(e => e.type === 'message' && e.message?.role === 'user')
    if (first) out.turnStartedAt = tsMs(first.message, first.timestamp) ?? 0
  }
  for (let i = s.nodes.length - 1; i >= 0; i--) {
    const n = s.nodes[i]
    if (n.kind !== 'user' || !isHumanPrompt(n.origin)) continue
    if (n.ts != null) out.turnStartedAt = n.ts
    break
  }
  for (const e of path) {
    if (isHumanEntry(e)) out.turns += 1
    else if (e.type === 'message' && e.message?.role === 'assistant') out.steps += 1
    else if (e.type === 'compaction' && e.usage) out.steps += 1
  }
  const summary = s.compactTurns?.at(-1)
  if (summary && !s.indexLoaded) {
    const newer = snapshotSuffix(path, summary.tailId)
    out.turns = summary.stats.turn + newer.filter(e => isUserEntry(e) && e.id !== summary.id).length
    out.steps = summary.stepCount + newer.filter(e => e.message?.role === 'assistant' || (e.type === 'compaction' && e.usage)).length
  }
  const summarizedTurns = new Set(s.compactTurns?.map(t => t.id))
  for (const n of s.nodes) {
    if (counted.has(n.id)) continue
    if (n.kind === 'user' && isHumanPrompt(n.origin) && !summarizedTurns.has(n.id)) out.turns += 1
    else if (n.kind === 'assistant' && !n.streaming) out.steps += 1
  }
  if (out.turns === 0 && (path.some(e => e.type === 'message') || s.nodes.length)) out.turns = 1
  applyBranchStats(s, path, out)
  const spans = runElapsedByTurn(s.records)
  for (const t of projected.values()) {
    spans.set(t.turnId, { ordinal: t.turn, elapsedMs: Math.max(spans.get(t.turnId)?.elapsedMs ?? 0, t.elapsedMs) })
  }
  for (const turn of s.compactTurns ?? []) {
    spans.set(turn.id, { ordinal: turn.stats.turn, elapsedMs: Math.max(spans.get(turn.id)?.elapsedMs ?? 0, turn.stats.elapsedMs) })
  }
  let newest: TurnId | undefined
  for (const [id, span] of spans) if (!newest || span.ordinal > spans.get(newest)!.ordinal) newest = id
  for (const [id, span] of spans) {
    if (id === newest) out.turnElapsedMs = span.elapsedMs
    else out.elapsedMs += span.elapsedMs
  }
  const anchor = s.compactTurns?.slice().reverse().find(t => t.cumulativeElapsedMs != null)
  if (anchor?.cumulativeElapsedMs != null) {
    let total = anchor.cumulativeElapsedMs + Math.max(0, (spans.get(anchor.id)?.elapsedMs ?? 0) - anchor.stats.elapsedMs)
    for (const span of spans.values()) if (span.ordinal > anchor.stats.turn) total += span.elapsedMs
    out.elapsedMs = Math.max(0, total - out.turnElapsedMs)
  }
  return out
}

function applyBranchStats(s: ViewState, path: Entry[], out: SessionStats): void {
  const snapshot = s.sessionStats
  if (!snapshot) return
  const totals = { ...snapshot }
  let turnId = snapshot.lastTurnId
  let turnTtft = snapshot.lastTurnTtftMs
  let turnHuman = snapshot.lastTurnHuman
  for (const e of snapshotSuffix(path, snapshot.tailId).reverse()) {
    if (isHumanEntry(e)) {
      if (turnHuman || totals.turns === 0) {
        totals.turns += 1
        turnTtft = 0
      }
      turnId = e.id
      turnHuman = true
    } else if (totals.turns === 0 && (e.message || e.type === 'compaction' || e.type === 'run_aborted')) {
      totals.turns = 1
      turnId = e.id
    }
    if (e.type === 'compaction' && e.usage) totals.steps += 1
    const m = e.message
    if (m?.role !== 'assistant') continue
    totals.steps += 1
    if (!turnTtft && (m.ttftMs ?? 0) > 0) {
      turnTtft = m.ttftMs!
      totals.ttftSumMs += turnTtft
      totals.ttftCount += 1
    }
    const u = m.usage
    if (!u) continue
    totals.input += (u.input ?? 0) + (u.cacheRead ?? 0) + (u.cacheWrite ?? 0)
    totals.output += u.output ?? 0
    totals.cacheRead += u.cacheRead ?? 0
    totals.cacheWrite += u.cacheWrite ?? 0
    if (u.cost) { totals.hasCost = true; totals.cost += u.cost.total }
    const decode = (m.latencyMs ?? 0) - (m.ttftMs ?? 0)
    if ((m.ttftMs ?? 0) > 0 && decode > 0 && (u.output ?? 0) > 0) {
      totals.decodeTokens += u.output!
      totals.decodeMs += decode
    }
  }
  const counted = new Set(path.map(e => e.id))
  for (const n of s.nodes) {
    if (n.kind === 'user' && isHumanPrompt(n.origin) && isTransientUserId(n.id) && !counted.has(n.id) && n.id !== turnId) {
      if (turnHuman || totals.turns === 0) totals.turns += 1
      turnHuman = true
    }
  }
  Object.assign(out, {
    turns: totals.turns, steps: totals.steps, input: totals.input, output: totals.output,
    cacheRead: totals.cacheRead, cacheWrite: totals.cacheWrite, hasCost: totals.hasCost, cost: totals.cost,
    ttftMs: totals.ttftCount > 0 ? totals.ttftSumMs / totals.ttftCount : 0,
    tps: totals.decodeMs > 0 ? totals.decodeTokens / (totals.decodeMs / 1_000) : null,
  })
}

function runElapsedByTurn(records: TrajRecord[]): Map<TurnId, { ordinal: number; elapsedMs: number }> {
  const spans = new Map<TurnId, { ordinal: number; start: number; end: number; anchored: boolean; duration: number }>()
  let fallback: TurnId | undefined
  let ordinal: number | undefined
  for (const r of records) {
    if (!fallback || (r.kind === 'user' && ordinal !== r.turn)) fallback = r.id
    ordinal = r.turn
    const id = r.turnId ?? fallback
    let span = spans.get(id)
    if (!span) {
      span = { ordinal: r.turn, start: 0, end: 0, anchored: false, duration: 0 }
      spans.set(id, span)
    }
    if (r.kind === 'assistant' && !r.requestOnly) span.duration += r.durationMs ?? 0
    if (r.startedAt == null) continue
    const anchored = r.kind === 'user' && r.id === id
    const end = r.kind === 'tool' && r.durationMs != null ? r.startedAt + r.durationMs : r.startedAt
    if (anchored || (!span.anchored && (span.start === 0 || r.startedAt < span.start))) span.start = r.startedAt
    span.anchored ||= anchored
    if ((r.kind === 'tool' || r.kind === 'compacted' || (r.kind === 'assistant' && !r.requestOnly)) && end > span.end) span.end = end
  }
  const out = new Map<TurnId, { ordinal: number; elapsedMs: number }>()
  for (const [id, span] of spans) out.set(id, {
    ordinal: span.ordinal,
    elapsedMs: span.anchored && span.start > 0 && span.end > span.start ? span.end - span.start : span.duration,
  })
  return out
}

export type TurnStats = {
  turnId: TurnId
  turn: number
  steps: number
  startedAt?: number
  elapsedMs: number
  durationMs: number
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  hasCost: boolean
  cost: number
  tools: number
  toolFailures: number
  cacheMisses: number
  ttftMs: number
  tps: number | null
  decodeTokens?: number
  decodeMs?: number
  lastTtftMs?: number
  lastTps?: number | null
  live: boolean
}

export function nodeLive(n: ChatNode): boolean {
  return (n.kind === 'assistant' && !!n.streaming)
    || (n.kind === 'tool' && !!n.running)
    || (n.kind === 'compaction' && !!n.running)
}

function hasResult(n: ChatNode): boolean {
  return n.kind === 'tool' && n.result !== undefined
}

export function turnStats(nodes: ChatNode[], base = 0): Map<string, TurnStats> {
  const out = new Map<string, TurnStats>()
  type Acc = TurnStats & { lastAt: number; decodeMs: number; decodeTokens: number; lastId: string }
  let acc: Acc | null = null
  let turn = base
  const groups = new Map(groupTurns(nodes).map(group => [group.nodes[0].id, group]))
  const flush = () => {
    if (!acc) return
    if (acc.startedAt != null && acc.startedAt > 0 && acc.lastAt > acc.startedAt) {
      acc.elapsedMs = acc.lastAt - acc.startedAt
    }
    if (acc.elapsedMs === 0) acc.elapsedMs = acc.durationMs
    if (acc.steps > 0 || acc.tools > 0 || acc.live) {
      acc.tps = acc.decodeMs > 0 ? acc.decodeTokens / (acc.decodeMs / 1_000) : null
      out.set(acc.lastId, acc)
    }
    acc = null
  }
  for (const n of nodes) {
    const group = groups.get(n.id)
    if (group) {
      flush()
      turn += 1
      const firstUser = group.user ?? group.nodes.find(n => n.kind === 'user')
      const start = firstUser?.kind === 'user' ? firstUser.ts ?? 0 : 0
      acc = {
        turnId: group.id, turn, steps: 0, elapsedMs: 0, durationMs: 0,
        input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
        hasCost: false, cost: 0, ttftMs: 0, tps: null, lastTtftMs: 0, lastTps: null, live: false,
        tools: 0, toolFailures: 0, cacheMisses: 0,
        startedAt: start, lastAt: start, decodeMs: 0, decodeTokens: 0, lastId: n.id,
      }
    }
    if (!acc) continue
    acc.lastId = n.id
    acc.live ||= nodeLive(n)
    if ((n.kind === 'assistant' || (n.kind === 'compaction' && !n.lifecycle)) && n.ts != null) acc.lastAt = Math.max(acc.lastAt, n.ts)
    if (n.kind === 'tool' && n.startedAt != null && n.durationMs != null) {
      const endedAt = n.startedAt + n.durationMs
      if (endedAt > acc.lastAt) acc.lastAt = endedAt
    }
    if (n.kind === 'assistant') {
      if (n.streaming) continue
      acc.steps += 1
      if (n.latencyMs != null) acc.durationMs += n.latencyMs
      if (acc.ttftMs === 0 && n.ttftMs != null && n.ttftMs > 0) acc.ttftMs = n.ttftMs
      acc.lastTtftMs = n.ttftMs != null && n.ttftMs > 0 ? n.ttftMs : 0
      acc.lastTps = null
      const u = n.usage
      if (u) {
        const read = u.cacheRead ?? 0
        const write = u.cacheWrite ?? 0
        acc.input += (u.input ?? 0) + read + write
        acc.output += u.output ?? 0
        acc.cacheRead += read
        acc.cacheWrite += write
        if (u.cost) { acc.hasCost = true; acc.cost += u.cost.total }
      }
      if (n.cacheMiss) acc.cacheMisses += 1
      if (n.latencyMs != null && n.ttftMs != null && n.ttftMs > 0 && (u?.output ?? 0) > 0) {
        const decode = n.latencyMs - n.ttftMs
        if (decode > 0) {
          acc.decodeMs += decode
          acc.decodeTokens += u?.output ?? 0
          acc.lastTps = (u?.output ?? 0) / (decode / 1_000)
        }
      }
    } else if (n.kind === 'tool') {
      acc.tools += 1
      if (n.isError) acc.toolFailures += 1
    } else if (n.kind === 'compaction') {
      if (n.lifecycle) continue
      acc.lastTtftMs = 0
      acc.lastTps = null
      if (!n.running) acc.steps += 1
    }
  }
  flush()
  return out
}

export function cacheHitPercent(s: SessionStats): number | null {
  if (s.input <= 0 || s.cacheRead <= 0) return null
  return s.cacheRead / s.input * 100
}

export function projectTurnStats(nodes: ChatNode[], base = 0, summaries: CompactTurn[] = []): Map<string, TurnStats> {
  const out = turnStats(nodes, base)
  const byTurn = new Map<TurnId, string>()
  for (const [key, stats] of out) byTurn.set(stats.turnId, key)
  const groups = new Map(groupTurns(nodes).map(group => [group.id, group]))
  const current = new Set(nodes.map(n => n.id))
  for (const summary of summaries) {
    const key = byTurn.get(summary.id) ?? groups.get(summary.id)?.nodes.at(-1)?.id ?? summary.visibleNodeIds.at(-1) ?? summary.id
    if (!current.has(key)) continue
    const observed = out.get(key)
    if (!observed) {
      const latest = lastStepReadout(summary.lastStep)
      out.set(key, { ...summary.stats, turnId: summary.id, turn: Math.max(1, summary.stats.turn), lastTtftMs: latest.ttftMs, lastTps: latest.tps })
      continue
    }
    const overlapNodes = (summary.baselineNodes ?? []).filter(n => current.has(n.id))
    const overlap = [...turnStats(overlapNodes).values()][0]
    const merged = { ...observed, turnId: summary.id, turn: Math.max(1, summary.stats.turn), startedAt: observed.startedAt || summary.stats.startedAt }
    for (const field of ['steps', 'tools', 'toolFailures', 'durationMs', 'input', 'output', 'cacheRead', 'cacheWrite', 'cost', 'cacheMisses'] as const) {
      merged[field] = Math.max(0, summary.stats[field] + observed[field] - (overlap?.[field] ?? 0))
    }
    merged.hasCost = summary.stats.hasCost || observed.hasCost
    const lastAt = (groups.get(summary.id)?.nodes ?? []).reduce((end, node) => {
      if ((node.kind === 'assistant' || (node.kind === 'compaction' && !node.lifecycle)) && node.ts != null) return Math.max(end, node.ts)
      if (node.kind === 'tool' && node.startedAt != null && node.durationMs != null) return Math.max(end, node.startedAt + node.durationMs)
      return end
    }, 0)
    merged.elapsedMs = Math.max(summary.stats.elapsedMs, merged.startedAt && lastAt > merged.startedAt ? lastAt - merged.startedAt : merged.durationMs)
    merged.ttftMs = summary.stats.ttftMs || observed.ttftMs
    if (summary.stats.decodeMs !== undefined && summary.stats.decodeTokens !== undefined) {
      merged.decodeMs = Math.max(0, summary.stats.decodeMs + (observed.decodeMs ?? 0) - (overlap?.decodeMs ?? 0))
      merged.decodeTokens = Math.max(0, summary.stats.decodeTokens + (observed.decodeTokens ?? 0) - (overlap?.decodeTokens ?? 0))
      merged.tps = merged.decodeMs > 0 ? merged.decodeTokens / (merged.decodeMs / 1_000) : null
    } else merged.tps = merged.steps === observed.steps ? observed.tps : summary.stats.tps
    if (observed.lastTtftMs === 0 && observed.lastTps == null) {
      const latest = lastStepReadout(summary.lastStep)
      merged.lastTtftMs = latest.ttftMs
      merged.lastTps = latest.tps
    }
    out.set(key, merged)
  }
  return out
}

function lastStepReadout(step: CompactTurn['lastStep']): { ttftMs: number; tps: number | null } {
  if (!step) return { ttftMs: 0, tps: null }
  const ttftMs = step.ttftMs > 0 ? step.ttftMs : 0
  const output = step.usage?.output ?? 0
  const decode = step.latencyMs - step.ttftMs
  const tps = ttftMs > 0 && decode > 0 && output > 0 ? output / (decode / 1_000) : null
  return { ttftMs, tps }
}

export function cacheHitRate(usage?: Usage | null): number | null {
  if (!usage) return null
  const prompt = (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0)
  if (prompt <= 0) return null
  return (usage.cacheRead ?? 0) / prompt * 100
}

export function cacheMisses(nodes: ChatNode[]): Map<string, CacheMiss> {
  const out = new Map<string, CacheMiss>()
  for (const n of nodes) {
    if (n.kind === 'assistant' && n.cacheMiss) out.set(n.id, n.cacheMiss)
  }
  return out
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

export function formatTokens(n: number): string {
  const scaled = (v: number): string => (v >= 100 ? String(Math.round(v)) : String(Math.round(v * 10) / 10))
  if (n < 1_000) return String(n)
  if (n < 1_000_000) return `${scaled(n / 1_000)}K`
  return `${scaled(n / 1_000_000)}M`
}

export function formatCost(total: number): string {
  const n = Math.max(0, total)
  return n < 0.01 ? n.toFixed(4) : n.toFixed(2)
}
