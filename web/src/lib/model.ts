import type { CompactTurn, ChatNode, Content, Entry, IndexEntry, LoopEvent, Message, Meta, ModelInfo, PromptChange, PromptSnapshot, RequestView, SessionDetail, ToolSchema, TrajRecord, Usage, ViewState } from '../api/types'

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
	s.runtimeReady = detail.runtime?.ready !== false
	s.leafId = detail.leafId
	s.hasMore = !!detail.hasMore
	s.oldestId = detail.oldestId
	s.compactTurns = detail.compactTurns
	setIndex(s, detail.index)
	addEntries(s, detail.entries ?? [])
	return rebuild(s)
}

/** setIndex records the tree index of a session detail. `undefined` leaves the
 * index unloaded; the server omits it when it would have to parse the whole
 * transcript, and the WebUI asks for it separately. */
function setIndex(s: ViewState, index?: IndexEntry[]) {
  if (index === undefined) return
  s.index = index
  s.indexLoaded = true
}

/**
 * applyIndex merges a lazily fetched tree index into an open session.
 *
 * Why: the conversation renders from the loaded window only, so an index that
 * arrives late must not change which entries have bodies — it fills in the
 * branch/trajectory rows and the absolute turn numbering.
 */
export function applyIndex(s: ViewState, detail: SessionDetail): ViewState {
  const next = { ...s }
  setIndex(next, detail.index ?? [])
  // An index can finish after a page or a live append. It only describes tree
  // metadata; adopting its old leaf/cursor would discard the reader's window.
  return rebuild(next)
}

/**
 * applyCursor adopts the server's paging cursor.
 *
 * Why: the cursor describes the window the server just returned. Once the user
 * paged further back, the loaded entries are older than that window and keeping
 * its cursor would re-fetch pages already on screen.
 */
function applyCursor(next: ViewState, detail: SessionDetail) {
  // Only pagination advances an established boundary. An attached turn-opening
  // user may precede that boundary, so neither min(id) nor array[0] is a cursor.
  if (next.oldestId) return
  if (detail.hasMore !== undefined) next.hasMore = detail.hasMore
  if (detail.oldestId !== undefined) next.oldestId = detail.oldestId
}

/**
 * applyTail merges a tail response into the loaded window, keeping the
 * index warm. New entries are appended to the index as rows, which is sound
 * because the transcript is append-only: they are the newest entries, so they
 * belong at the end of the file order the index follows.
 */
export function applyTail(s: ViewState, detail: SessionDetail, expectedLiveRevision = s.liveRevision): ViewState {
  const next = { ...s }
  const stale = expectedLiveRevision !== s.liveRevision
  next.busy = stale ? s.busy : !!detail.running
  if (!next.busy) {
    next.stopping = false
    next.toolStates = undefined
    // Recovery can miss message_end/agent_end entirely. An authoritative idle
    // snapshot must retire transient rows or rebuild appends the stale partial
    // beside the persisted final answer and leaves tool spinners running.
    next.nodes = next.nodes.filter(n => !(n.kind === 'assistant' && n.streaming) && !(n.kind === 'tool' && n.running))
    next.records = next.records.filter(r => !r.running)
    next.requests = next.requests.filter(r => r.status !== 'running')
  }
  // A GET captures a past frontier. Never rewind entries already delivered by
  // SSE while it was in flight, even if its terminal event is still pending.
  if (!stale) next.leafId = detail.leafId ?? next.leafId
  next.runtimeReady = detail.runtime?.ready !== false
  next.commands = detail.commands ?? next.commands
  next.queued = detail.queued ?? next.queued
  next.extQueued = detail.extQueued ?? next.extQueued
  next.extensionUi = detail.extensionUi ?? next.extensionUi
  const entries = detail.entries ?? []
  const settledThrough = Math.max(0, ...entries.filter(e => e.message?.role === 'assistant').map(e => tsMs(e.message, e.timestamp) ?? 0), ...(detail.compactTurns ?? []).map(t => t.assistantAt ?? 0))
  if (settledThrough > 0) {
    // A newly observed server assistant completion proves preceding work has
    // joined, including tools whose call/result was hidden by compact mode.
    // Never infer this from a later sibling tool result.
    const expired = (n: ChatNode) => (n.kind === 'tool' && n.startedAt != null && n.startedAt < settledThrough)
      || (n.kind === 'assistant' && n.streaming && n.ts != null && n.ts < settledThrough)
    next.nodes = next.nodes.filter(n => !expired(n) || (n.kind === 'tool' && !n.running && n.result !== undefined))
    if (next.toolStates) next.toolStates = Object.fromEntries(Object.entries(next.toolStates).filter(([, n]) => !expired(n)))
    next.records = next.records.filter(r => !(r.running && r.startedAt != null && r.startedAt < settledThrough))
    next.requests = next.requests.map(r => r.status === 'running' && r.startedAt != null && r.startedAt < settledThrough ? { ...r, status: 'complete' } : r)
  }
  addEntries(next, entries)
  mergeCompactTurns(next, detail.compactTurns)
  const snapshotPath = detail.leafId && leafEntries(mergeEntries(next.index, next.entries), detail.leafId, next.compactTurns)
  const descendant = !!(stale && detail.leafId && detail.leafId !== s.leafId && snapshotPath && snapshotPath.some(e => e.id === s.leafId))
  // React may commit the final SSE batch after a terminal GET starts, while
  // the server has already drained another queued prompt. Revision mismatch
  // forbids rewinding, not advancing along a proven immutable descendant.
  if (descendant) next.leafId = detail.leafId
  const connected = !s.leafId || leafEntries(next.entries, next.leafId, next.compactTurns).some(e => e.id === s.leafId)
  if (!connected && (!stale || descendant)) {
    // Old and new tails can be disjoint after suspension. The old root/cursor
    // describes a different loaded interval; retaining it strands the gap.
    next.oldestId = detail.oldestId
    next.hasMore = detail.hasMore
  } else applyCursor(next, detail)
  if (next.indexLoaded && entries.length) {
    const known = new Set(next.index.map(row => row.id))
    const added = entries.filter(e => !known.has(e.id)).map(entryToIndex)
    if (added.length) next.index = [...next.index, ...added]
  }
  // Never delete loaded ranges at run end: keeping the old cursor after a
  // 400-entry trim made the discarded range unreachable and removed the
  // reader's anchor. Body eviction keeps the entry identities instead.
  return rebuild(next)
}

/** addEntries records body-loaded entries, replacing earlier copies by id. */
function addEntries(s: ViewState, incoming: Entry[]) {
  if (!incoming.length) return
  const byId = new Map(incoming.map(e => [e.id, e]))
  const known = new Set(s.entries.map(e => e.id))
  s.entries = s.entries.map(e => {
    const replacement = byId.get(e.id)
    if (replacement && !replacement.truncated && e.truncated) bodyPreviews.set(replacement, e)
    // Persisted entries are immutable. A late slim page must not erase a full
    // body and trigger another download when the row is mounted again.
    return replacement && !(replacement.truncated && !e.truncated) ? replacement : e
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

/** Evict cold bodies, never identities or page boundaries. Mounted rows and
 * the newest turn are pinned, even when one of them alone exceeds the budget. */
export function evictBodies(s: ViewState, protectedIds: ReadonlySet<string>, budget = BODY_CACHE_BYTES): ViewState {
  // Compact metadata/preview rows remain as the index of known ranges; only
  // retained bodies are charged. Identity count is deliberately independent
  // of this budget so eviction cannot create a hole in the transcript.
  let bytes = s.entries.reduce((total, e) => total + (evictedPreviews.has(e) ? 0 : bodySize(e)), 0)
  if (bytes <= budget) return s
  let lastUser = -1
  for (let i = s.entries.length - 1; i >= 0; i--) if (s.entries[i].message?.role === 'user') { lastUser = i; break }
  if (lastUser < 0) lastUser = Math.max(0, s.entries.length - 10)
  let changed = false
  const entries = s.entries.map((e, index) => {
    if (bytes <= budget || evictedPreviews.has(e) || index >= lastUser || protectedIds.has(e.id) || protectedIds.has(`system:${e.id}`) || (e.message?.toolCallId && protectedIds.has(e.message.toolCallId)) || e.message?.content?.some(c => c.id && protectedIds.has(c.id))) return e
    const base = bodyPreviews.get(e) ?? e
    const preview: Entry = {
      ...e, truncated: true, tools: undefined, details: undefined,
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

/** entryToIndex is the inverse of indexToEntry: a tree row for a loaded body. */
function entryToIndex(e: Entry): IndexEntry {
  const row: IndexEntry = {
    type: e.type,
    id: e.id,
    parentId: e.parentId,
    timestamp: e.timestamp,
    sideband: e.sideband,
    tokensBefore: e.tokensBefore,
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
    row.name = e.message.toolName
    row.preview = previewOf(messageText(e.message) || messageThinking(e.message))
  } else if (e.type === 'compaction') {
    row.preview = previewOf(e.summary ?? '')
  }
  return row
}

/**
 * rebuild derives the whole view from entries + index.
 *
 * Nodes come from the loaded window only; older entries contribute records
 * (the trajectory table) and the turn offset, so a long session neither builds
 * nor re-renders thousands of chat nodes. Live state from an in-flight run is
 * carried over so a rebuild during a run does not drop the streaming bubble.
 */
function rebuild(s: ViewState): ViewState {
  const streaming = s.nodes.filter(n => (n.kind === 'assistant' && n.streaming) || (n.kind === 'tool' && n.running))
  const runningRecords = s.records.filter(r => r.running)
  const runningRequests = s.requests.filter(r => r.status === 'running')
  const next: ViewState = {
    ...s,
    nodes: [],
    records: [],
    requests: [],
    turn: 0,
    turnBase: 0,
    promptState: undefined,
    currentRequestId: undefined,
  }
  const all = mergeEntries(next.index, next.entries)
  next.allEntries = all
  const chain = leafEntries(all, next.leafId, next.compactTurns)
  let windowStart = next.oldestId ? chain.findIndex(e => e.id === next.oldestId) : 0
  windowStart = Math.max(0, windowStart)
  // A detailed page may start mid-turn and include its opening user as an
  // anchor. Cached ranges before that turn must not jump across a paging gap.
  while (windowStart > 0 && !isUserEntry(chain[windowStart])) windowStart--
  const window = new Set(chain.slice(windowStart).map(e => e.id))
  const loaded = new Set(next.entries.filter(e => window.has(e.id)).map(e => e.id))
  // Keep disconnected body ranges in the cache. Only the active chain renders;
  // a later page/index can bridge a recovery gap without losing old identities.
  const active = new Set(chain.map(e => e.id))
  const cached = new Set(next.entries.map(e => e.id))
  next.entries = [...next.entries.filter(e => !active.has(e.id)), ...chain.filter(e => cached.has(e.id))]
  if (next.indexLoaded) {
    next.turnBase = chain.reduce((n, e) => n + (!loaded.has(e.id) && isUserEntry(e) ? 1 : 0), 0)
  }
  // Records are numbered from the branch root (index-only entries count too),
  // so the count below continues correctly for a live turn; turnStats adds
  // turnBase to the window's nodes, which is what the chat dividers show.
  const ordinals = new Map(next.compactTurns?.map(t => [t.id, t.stats.turn]))
  const explicitCancellations = new Set<string>()
  let pendingAbortedAssistant: string | undefined
  for (const e of chain) {
    if (e.type === 'message' && e.message?.role === 'assistant') {
      pendingAbortedAssistant = e.message.stopReason === 'aborted' ? e.id : undefined
    } else if (e.type === 'run_aborted' && pendingAbortedAssistant) {
      // Context-usage snapshots and undrained steers may sit between the
      // terminal assistant and the event leaf, so ParentID is not a reliable
      // direct link. A later assistant starts a different model step.
      explicitCancellations.add(pendingAbortedAssistant)
      pendingAbortedAssistant = undefined
    }
  }
  for (const e of chain) {
    const ordinal = ordinals.get(e.id)
    if (ordinal != null && isUserEntry(e)) next.turn = ordinal - 1
    applyEntry(next, e, loaded.has(e.id))
    if (loaded.has(e.id) && e.type === 'message' && e.message?.role === 'assistant' &&
      e.message.stopReason === 'aborted' && !explicitCancellations.has(e.id)) {
      // Older transcripts persisted run_aborted as an unparented sideband, so
      // the branch view cannot safely attach that event. The terminal message
      // is branch-correct and unambiguously cancelled; synthesize the generic
      // standalone row rather than resurrecting an ambiguous sideband.
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
  next.nodes = next.nodes.filter(n => !omitted.has(n.id))
  const firstUser = next.nodes.find(n => n.kind === 'user')
  const firstRecord = firstUser && next.records.find(r => r.kind === 'user' && r.id === firstUser.id)
  if (firstRecord) next.turnBase = Math.max(0, firstRecord.turn - 1)

  // Snapshot entries settle work by identity, never by the order in which
  // sibling tools finish. A later model request retires the previous batch in
  // applyEvent; an ordinary hydration has no authority to end live work.
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
    // Nodes, trajectory and composer share the same overlay. Updating only
    // the node made elapsed/output/errors disappear on unrelated hydration.
    patchTool(next, node.id, node)
  }
  // Hand-constructed/in-flight states can predate the execution overlay. Keep
  // their live tools too unless an immutable result already settled them.
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
  if (next.requests.length) next.currentRequestId = next.requests[next.requests.length - 1].id
  // Preserve row identity across pagination/hydration. A sibling acquiring its
  // full body must not re-render every unchanged Markdown/tool row.
  const oldNodes = new Map(s.nodes.map(n => [n.id, n]))
  next.nodes = next.nodes.map(n => {
    const old = oldNodes.get(n.id)
    // Final SSE identity becomes the persisted entry id, while the mounted row
    // keeps its render key and parsed segments through the tail reconciliation.
    if (old?.kind === 'assistant' && n.kind === 'assistant' && old.renderKey) n = { ...n, renderKey: old.renderKey }
    return old && sameValue(old, n) ? old : n
  })
  attachCompactBaselines(next)
  return next
}

/** Reconstruct exactly the locally-known part of each immutable snapshot.
 * Loading an old hidden body grows both observed stats and this overlap, so
 * snapshot + observed - overlap never double-counts an expanded turn. */
function attachCompactBaselines(s: ViewState) {
  if (!s.compactTurns?.length) return
  const loaded = new Set(s.entries.map(e => e.id))
  const graph = branchGraph(s.allEntries, s.compactTurns)
  s.compactTurns = s.compactTurns.map(turn => {
    const baseline = emptyView()
    // Stop at this turn's user and share lookup maps across turns: walking
    // every snapshot back to the root made repeated hydration quadratic.
    const path = walkBranch(graph, turn.tailId, turn.id)
    for (const e of path) if (loaded.has(e.id)) applyEntry(baseline, e)
    // Compact omits even current-batch bodies at keep=0. Batch identities and
    // terminal flags define exactly what the snapshot already accounted for.
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
  return e.type === 'message' && e.message?.role === 'user'
}

export function hydrateEntries(s: ViewState, incoming: Entry[], meta?: { hasMore?: boolean; oldestId?: string; compactTurns?: CompactTurn[] }): ViewState {
  if (!incoming.length && meta == null) return s
  const next = { ...s }
  addEntries(next, incoming)
  mergeCompactTurns(next, meta?.compactTurns)
  if (meta?.hasMore !== undefined) next.hasMore = meta.hasMore
  // An empty oldestId means the page had no older entry (the history ends
  // here, or the cursor was not found): keep the loaded cursor instead of
  // wiping it, which would strand every later `before=` request.
  if (meta?.oldestId) next.oldestId = meta.oldestId
  return rebuild(next)
}

function mergeCompactTurns(s: ViewState, incoming?: CompactTurn[]) {
  if (!incoming) return
  const turns = new Map(s.compactTurns?.map(t => [t.id, t]))
  for (const turn of incoming) {
    const previous = turns.get(turn.id)
    // A delayed page or keep reprojection must not replace a newer sparse
    // frontier: that would sever the current leaf from its opening user.
    if (previous && ((turn.entryCount != null && previous.entryCount != null && turn.entryCount < previous.entryCount)
      || (turn.entryCount == null && (turn.stats.steps < previous.stats.steps || turn.stats.tools < previous.stats.tools || turn.stats.elapsedMs < previous.stats.elapsedMs)))) continue
    turns.set(turn.id, turn)
  }
  s.compactTurns = [...turns.values()].sort((a, b) => a.stats.turn - b.stats.turn)
  const byId = new Map(s.entries.map(e => [e.id, e]))
  const loaded = new Set(s.loadedTurnIds)
  for (const turn of s.compactTurns) {
    // Completeness belongs to this snapshot frontier. A previously expanded
    // prefix does not prove that hidden replies added while away are loaded.
    loaded.delete(turn.id)
    let id: string | undefined = turn.tailId
    const seen = new Set<string>()
    while (id && !seen.has(id)) {
      seen.add(id)
      if (!byId.has(id)) break
      if (id === turn.id) { loaded.add(id); break }
      id = byId.get(id)?.parentId
    }
  }
  s.loadedTurnIds = [...loaded]
}

export function hydrateTurn(s: ViewState, id: string, entries: Entry[]): ViewState {
  return hydrateEntries({ ...s, loadedTurnIds: [...new Set([...(s.loadedTurnIds ?? []), id])] }, [...new Map(entries.map(e => [e.id, e])).values()])
}

/** Convert the partial oldest detailed page to a whole compact turn without
 * downloading the missing folded replies or keeping their partial count. */
export function compactBoundary(s: ViewState, detail: SessionDetail): ViewState {
  const turn = detail.compactTurns?.[0]
  if (!turn) return s
  const next = { ...s }
  mergeCompactTurns(next, detail.compactTurns)
  // Changing presentation must not evict a complete turn already on screen
  // (or one the reader expanded while the projection request was in flight).
  if (next.loadedTurnIds?.includes(turn.id)) return hydrateEntries(next, detail.entries ?? [], detail)
  const end = s.entries.findIndex(e => e.id === turn.tailId)
  const entries = end >= 0 ? s.entries.slice(end + 1) : s.entries
  return hydrateEntries({ ...next, entries }, detail.entries ?? [], detail)
}

export function applyRuntimeCatalog(s: ViewState, detail: SessionDetail): ViewState {
  return {
    ...s,
    runtimeReady: detail.runtime?.ready !== false,
    commands: detail.commands ?? s.commands,
    extensionUi: detail.extensionUi ?? s.extensionUi,
    queued: detail.queued ?? s.queued,
    extQueued: detail.extQueued ?? s.extQueued,
  }
}

function mergeEntries(index: IndexEntry[] | undefined, entries: Entry[]): Entry[] {
  const full = new Map(entries.map(e => [e.id, e]))
  if (!index?.length) return entries
  const rows = index.map(ix => full.get(ix.id) ?? indexToEntry(ix))
  // Bodies the index has not seen yet (a run that landed after the index was
  // fetched) still belong at the end: the transcript is append-only, so the
  // file order the index follows puts them last.
  const known = new Set(index.map(ix => ix.id))
  for (const e of entries) if (!known.has(e.id)) rows.push(e)
  return rows
}

function indexToEntry(ix: IndexEntry): Entry {
  const entry: Entry = {
    type: ix.type,
    id: ix.id,
    parentId: ix.parentId,
    timestamp: ix.timestamp,
    sideband: ix.sideband,
    tokensBefore: ix.tokensBefore,
    truncated: true,
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
      toolName: ix.name,
      content: [{ type: 'text', text: ix.preview || '' }],
    }
  }
  if (ix.type === 'compaction') entry.summary = ix.preview
  return entry
}

function branchGraph(entries: Entry[], turns: CompactTurn[] = []) {
  // Sparse compact bodies retain their canonical parents for edit/fork. Only
  // reading traversal bridges omitted replies in adjacent whole user turns.
  const previous = new Map<string, string | undefined>()
  const tails = new Map<string, string | undefined>()
  let last: string | undefined
  let ordinal: number | undefined
  for (const turn of turns) {
    if (ordinal != null && turn.stats.turn !== ordinal + 1) last = undefined
    ordinal = turn.stats.turn
    for (const id of turn.entryIds) { previous.set(id, last); last = id }
    tails.set(turn.tailId, last)
  }
  return { byId: new Map(entries.map(e => [e.id, e])), previous, tails, last: entries.at(-1)?.id }
}

function walkBranch(graph: ReturnType<typeof branchGraph>, leafId?: string, stopId?: string): Entry[] {
  const { byId, previous, tails } = graph
  const active: Entry[] = []
  let id = leafId || graph.last
  if (id && !byId.has(id)) id = tails.get(id)
  const seen = new Set<string>()
  while (id && !seen.has(id)) {
    seen.add(id)
    const entry = byId.get(id)
    if (!entry) break
    active.push(entry)
    if (id === stopId) break
    const parent = entry.parentId
    id = parent && byId.has(parent) ? parent
      : parent && tails.has(parent) ? tails.get(parent)
        : entry.previousId && byId.has(entry.previousId) ? entry.previousId
          : entry.previousId && tails.has(entry.previousId) ? tails.get(entry.previousId)
            : previous.get(entry.id) ?? tails.get(entry.id)
  }
  return active.reverse()
}

function leafEntries(entries: Entry[], leafId?: string, turns: CompactTurn[] = []): Entry[] {
  return walkBranch(branchGraph(entries, turns), leafId)
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
  let step = 0
  for (const request of s.requests) {
    if (request.turn === turn) step = Math.max(step, request.step)
  }
  return step + 1
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
  const previous = s.requests.find(request => request.id === id)
  if (!previous || Object.entries(patch).every(([key, value]) => previous[key as keyof RequestView] === value)) return
  s.requests = s.requests.map(request => request.id === id ? { ...request, ...patch } : request)
  s.records = s.records.map(record => record.requestId !== id
    ? record
    : {
      ...record,
      ...(patch.durationMs === undefined ? {} : { durationMs: patch.durationMs }),
      ...(patch.ttftMs === undefined ? {} : { ttftMs: patch.ttftMs }),
      ...(patch.status === undefined ? {} : { running: patch.status === 'running', error: patch.status === 'error' }),
    })
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

  // Why: request headers describe every provider call, but the visible ledger
  // should only show the prompt state when it changes. This hidden boundary
  // retains request timing without duplicating system rows.
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
  // Why: request_header is the provider call's system/tools payload. Chat is
  // the user/assistant transcript; a SYSTEM row for the initial (or changed)
  // prompt was noise on every conversation. Trajectory keeps the record and
  // inspector tabs.
}

// applyEntry folds one entry into the view. withNode is false for entries that
// only exist as index rows: they still contribute records and turn numbering,
// but they must not become chat nodes.
function applyEntry(s: ViewState, e: Entry, withNode = true) {
	if (e.type === 'context_usage') {
		s.contextUsage = { usedTokens: e.usedTokens ?? 0, contextWindow: e.contextWindow ?? 0, estimated: !!e.estimated }
		return
	}
  if (e.type === 'request_header') {
    if (e.promptUnchanged) {
      applyRequestHeader(s, e.id, s.promptState?.system ?? '', s.promptState?.tools ?? [], e.timestamp, {
        provider: e.provider,
        model: e.modelId,
        thinkingEffort: e.thinkingEffort,
      })
      return
    }
    if (!e.system && !e.tools?.length && e.truncated) {
      s.records.push({ id: `system:${e.id}`, requestId: e.id, kind: 'system', turn: s.turn || 1, preview: '', truncated: true })
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
    applyMessage(s, e.message, e.id, e.timestamp, e.parentId, e.truncated, withNode)
    return
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
  // compaction_start/end entries are persisted alongside SSE; show them as a
  // compact record on replay.
  if (e.type === 'compaction_start' || e.type === 'compaction_end') {
    applyCompactEvent(s, e.id, e.type, e.details, e.timestamp)
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

function applyCompactEvent(s: ViewState, id: string, type: string, details?: unknown, stamp?: string) {
  const { reason, status, ok } = compactDetails(details)
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
  // Persisted start/end entries have distinct JSONL IDs. Pair the terminal
  // entry with the newest unfinished operation, just like the live event path.
  const rec = [...s.records].reverse().find(r => r.kind === 'compact' && r.running)
  if (rec && rec.kind === 'compact') {
    const { empty, failed } = compactTerminal(status, reason, ok)
    rec.running = false
    rec.preview = empty
      ? `Nothing to compact (${reason || 'auto'})`
      : `Compacted (${reason || 'auto'})${failed ? ' (failed)' : ''}`
  }
}

function applyMessage(s: ViewState, m: Message, id: string, stamp?: string | number, parentId?: string, truncated?: boolean, withNode = true) {
  if (m.role === 'user') {
    const text = messageText(m)
    s.turn += 1
    if (withNode) s.nodes.push({ kind: 'user', id, parentId, text, content: m.content ?? [], ts: tsMs(m, stamp), origin: m.origin, truncated })
    s.records.push({
      id,
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
	      // Tool results keep the model request open; the next request header
	      // will close the current request boundary and start the next step.
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
  // startedAt stays on the node (not just the record): the running row ticks
  // its live elapsed from it, and a settled row keeps it so the chat and the
  // trajectory inspector agree after a history load.
  const { outputBlocks, sourceBlocks, ...nodePatch } = patch
  s.nodes = s.nodes.map(n => {
    if (n.kind !== 'tool' || n.id !== id) return n
    return { ...n, ...nodePatch, running: patch.running ?? n.running }
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
      details: patch.details ?? r.details,
      ...(outputBlocks === undefined ? {} : { outputBlocks }),
      ...(sourceBlocks === undefined ? {} : { sourceBlocks }),
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

// Replay dedupe is state-based, never counted. Why: the server trims its
// replay buffer (superseded chunks and completed messages' starts are gone) and
// a client may resume mid-run with a cursor, so "how many messages will the
// replay cover" is not knowable on the client. A count-based baseline also
// breaks the moment a live completion lands between the baseline and the next
// message_start: the reply then only appeared whole at message_end (text after
// a tool call did not stream). Instead every rule below asks what the view
// already holds:
//   - a second assistant message_start is the same in-flight message replayed
//     (at most one message streams at a time) -> reset that bubble in place;
//   - a message_end whose entry id is already on screen finishes a message the
//     transcript holds -> drop the bubble the replayed start/partial opened.

/** Persisted SSE messages enter the same identity graph as HTTP history.
 * Keeping them only in nodes left the leaf stale until run end, causing clocks
 * to reuse the old prompt and unrelated hydration to erase completed work. */
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
  // A model request starts only after the preceding parallel batch joins.
  // Sibling completion is not such a boundary and must never settle a peer.
  s.nodes = s.nodes.map(n => n.kind === 'tool' && n.running ? { ...n, running: false } : n)
  s.records = s.records.map(r => r.kind === 'tool' && r.running ? { ...r, running: false } : r)
  if (s.toolStates) s.toolStates = Object.fromEntries(Object.entries(s.toolStates).map(([id, n]) => [id, { ...n, running: false }]))
}

export function applyEvent(s: ViewState, ev: LoopEvent): ViewState {
  const next: ViewState = {
    ...s,
    liveRevision: (s.liveRevision ?? 0) + 1,
    nodes: s.nodes.slice(),
    records: s.records.slice(),
    requests: s.requests.slice(),
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
      settleToolBatch(next)
      next.busy = false
      next.stopping = false
      next.nodes = next.nodes.map(n =>
        n.kind === 'assistant' && n.streaming ? { ...n, streaming: false } : n.kind === 'tool' && n.running ? { ...n, running: false } : n,
      )
      next.records = next.records.map(r => r.running ? { ...r, running: false } : r)
      next.requests = next.requests.map(request => request.status === 'running'
        ? { ...request, status: 'complete', completedAt: request.completedAt ?? Date.now() }
        : request)
      break
    case 'steer_accepted':
      if (ev.message?.content) return appendOptimisticUser(s, ev.message.content)
      break
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
      // A replayed header whose system prompt and tools repeat the previous
      // one arrives without its body (the server trims it, exactly like the
      // persisted entry): fold it back into the prompt already on screen.
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
      if (ev.type === 'message_end' && ev.entryId && ev.message) persistLiveEntry(next, { type: 'message', id: ev.entryId, parentId: ev.parentId, message: ev.message })
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
      // id is not part of the wire event; match by kind+order on the live path.
      const stamp = Date.now()
      if (ev.type === 'compaction_start') {
        const id = `compact-live-${stamp}`
        // The chat history has no run stream of its own for a manual /compact,
        // so the same events drive both the timeline record and the chat row.
        next.nodes = [...next.nodes, { kind: 'compaction', id, summary: '', running: true }]
        next.records.push({
          id,
          kind: 'compact',
          turn: next.turn || 1,
          preview: `Compacting (${ev.reason || 'auto'})…`,
          running: true,
          startedAt: stamp,
        })
      } else {
        const terminal = compactTerminal(ev.status || '', ev.reason || '', ev.ok)
        const live = [...next.nodes].reverse().find(n => n.kind === 'compaction' && n.running)
        if (live && live.kind === 'compaction') {
          next.nodes = next.nodes.map(n => n.id === live.id
            ? { ...live, running: false, empty: terminal.empty, failed: terminal.failed }
            : n)
        }
        const rec = [...next.records].reverse().find(r => r.kind === 'compact' && r.running)
        if (rec) {
          rec.running = false
          rec.preview = terminal.empty
            ? `Nothing to compact (${ev.reason || 'auto'})`
            : `Compacted (${ev.reason || 'auto'})${terminal.failed ? ' (failed)' : ''}`
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
          durationMs: ev.durationMs,
          name: ev.toolName,
		  details: resultDetails,
        })
      }
      rememberTool(next, ev.toolCallId)
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
      if (m.timestamp || ev.entryId) {
        for (let i = s.nodes.length - 1; i >= 0; i--) {
          const n = s.nodes[i]
          if (n.kind === 'user' && n.text === text) {
			const nextId = ev.entryId || n.id
            s.nodes[i] = { ...n, id: nextId, content: m.content ?? n.content, ts: m.timestamp ?? n.ts, origin: m.origin ?? n.origin }
            // Keep the trajectory record in sync so the detail panel shows the
            // same start time as the chat bubble.
			s.records = s.records.map(r => (r.id === n.id ? { ...r, id: nextId, startedAt: m.timestamp ?? r.startedAt } : r))
            break
          }
        }
      }
      return
    }
    // A resume can replay a user message the transcript already rendered; the
    // entry id is authoritative, so it is never appended twice.
    if (ev.entryId && s.nodes.some(n => n.id === ev.entryId)) return
    applyMessage(s, m, ev.entryId || `live-user-${s.nodes.length}`, undefined)
    return
  }
  if (m.role === 'toolResult') {
    // applyMessage patches an existing tool node by toolCallId, so a replayed
    // tool result is idempotent without an entry-id check.
    applyMessage(s, m, m.toolCallId || `live-tr-${s.nodes.length}`, undefined)
    return
  }
  if (m.role !== 'assistant') return
  if (ev.type === 'message_start' || ev.type === 'message_end' || lastStreamingAssistant(s) < 0) settleToolBatch(s)

  if (ev.type === 'message_end' && ev.entryId && s.nodes.some(n => n.id === ev.entryId)) {
    // The transcript already holds this message (it finished while we were
    // away, and the refetched tail has it). A replayed start or partial may
    // have opened a bubble for it: drop that bubble instead of letting it
    // stream forever next to the persisted node.
    const stale = lastStreamingAssistant(s)
    if (stale >= 0) dropStreamingAssistant(s, stale)
    return
  }

  if (ev.type === 'message_start') {
    const live = lastStreamingAssistant(s)
    if (live >= 0) {
      // At most one assistant message streams at a time, so a second start can
      // only be that same in-flight message replayed — a re-attach. Reset the
      // bubble in place; pushing another one would leave both streaming.
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
      // The replay may carry only the newest partial: the server trims every
      // superseded chunk and the completed messages' starts. That partial
      // still holds the whole accumulated text, so it is enough to open the
      // bubble and keep streaming from the next live chunk.
      pushStreamingAssistant(s, m, ev.display)
      return
    }
    if (ev.type === 'message_end') {
      // The entry id was checked above; a view whose nodes carry no ids yet
      // (a very old window) falls back to matching the finished text.
      const text = messageText(m)
      if (!ev.entryId && text !== '' && s.nodes.some(n => n.kind === 'assistant' && !n.streaming && n.text === text)) return
      applyMessage(s, m, ev.entryId || `live-asst-${s.nodes.length}`, undefined)
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
    // Streaming deltas (message_update) usually carry no timestamp, so keep
    // the one from message_start; message_end carries the final authoritative
    // value, which wins when present.
    ts: m.timestamp ?? node.ts,
    usage: m.usage ?? node.usage,
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

/**
 * pushStreamingAssistant opens the reply bubble. Two events reach here: a live
 * message_start, and a replayed message_update when the server only kept the
 * newest partial. Both carry the accumulated message, so the record starts
 * with the same text the bubble shows — message_update usually has no
 * timestamp, hence the local-clock fallback.
 */
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

/**
 * dropStreamingAssistant removes a bubble a replay opened for a message the
 * transcript already completed, along with its trajectory record, so a resume
 * cannot leave a duplicate behind.
 */
function dropStreamingAssistant(s: ViewState, idx: number) {
  const node = s.nodes[idx]
  if (!node || node.kind !== 'assistant') return
  s.nodes.splice(idx, 1)
  s.records = s.records.filter(r => r.id !== node.id)
}

const liveUserPrefix = /^(live-user-|opt-user-)/

/** Drop optimistic/live user bubbles once the jsonl copy of the same text exists. */
export function reconcileUserNodes(nodes: ChatNode[]): ChatNode[] {
  const collapsed: ChatNode[] = []
  for (const n of nodes) {
    const last = collapsed.at(-1)
    // Consecutive same-text users are the optimistic bubble plus the live SSE
    // copy, not two turns (a real second turn has an assistant in between).
    if (n.kind === 'user' && last?.kind === 'user' && last.text.trim() === n.text.trim()) {
      collapsed[collapsed.length - 1] = n
      continue
    }
    collapsed.push(n)
  }
  const persisted = new Map<string, ChatNode>()
  for (const n of collapsed) {
    if (n.kind === 'user' && !liveUserPrefix.test(n.id)) persisted.set(n.text, n)
  }
  if (!persisted.size) return collapsed
  const used = new Set<string>()
  const out: ChatNode[] = []
  for (const n of collapsed) {
    if (n.kind !== 'user') {
      out.push(n)
      continue
    }
    const canonical = persisted.get(n.text)
    if (liveUserPrefix.test(n.id) && canonical) {
      if (!used.has(canonical.id)) {
        out.push(canonical)
        used.add(canonical.id)
      }
      continue
    }
    if (canonical && used.has(canonical.id) && n.id === canonical.id) continue
    out.push(n)
    if (canonical && n.id === canonical.id) used.add(n.id)
  }
  return out
}

export type UserRequest = { id: string; title: string }

/** First non-empty line, else the first named attachment. Empty when the user turn has no caption. */
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

/**
 * isHumanPrompt reports whether a user turn was typed by a person.
 *
 * Why: the runtime writes machine turns into the same user role — the Agent
 * tool's subagent directives and `<task-notification>` envelopes carry an
 * `agent`/`agent:<id>` origin. Those belong in the transcript (the chat shows
 * them as dashed bubbles) but not in the request navigator, which is a list of
 * the prompts a person sent. Extension origins stay: they relay a real user
 * (Telegram) or are turns an extension was asked to run, and both are part of
 * the conversation the navigator walks.
 */
export function isHumanPrompt(origin?: string): boolean {
  if (!origin) return true
  return origin.startsWith('extension:')
}

/**
 * userRequests lists the human prompts of the active branch.
 *
 * It walks the entries (which include body-less index rows for history the
 * conversation has not loaded) so the navigator always covers the whole
 * branch, then appends live user nodes the transcript has not caught up with
 * yet — a prompt just sent in this tab shows up before its entry is reloaded.
 */
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

export function appendOptimisticUser(s: ViewState, content: import('../api/types').Content[]): ViewState {
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
  /** Branch totals — how many user turns and assistant/compaction steps exist. */
  turns: number
  steps: number
  /** Session totals across every known step on the branch: the whole prompt
   * (uncached + cacheRead + cacheWrite), its output, and the summed cost. */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  hasCost: boolean
  cost: number
  /** Mean first-token latency across the session's turns, not the newest
   * request's; 0 when the provider reported none. */
  ttftMs: number
  /** Output tokens per second for the whole session — output-weighted over the
   * turns' decode spans; null when no turn reported a usable span. */
  tps: number | null
  /** Unix ms of the newest turn's opening user message, 0 when unknown. Lets
   * the composer show a live elapsed while that turn runs. */
  turnStartedAt: number
  /** Branch total of the settled model-run time of every turn except the newest
   * (`turnElapsedMs`). The composer adds the live span (`now - turnStartedAt`)
   * while the newest turn runs, so the session strip shows how long the model
   * has actually been working rather than just the current turn. */
  elapsedMs: number
  /** Settled model-run time of the newest turn, used when it is not running. */
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

/** Session-wide usage/timing for the composer strip: the branch totals and the
 * summed usage of every known turn, not the newest step's one-shot values.
 * Per-turn stats come from `projectTurnStats`, so a folded compact snapshot is
 * counted once and a just-finished live step counts before the history refetch.
 * TTFT is the mean of the turns' first-token latency and TPS the output-weighted
 * mean of their decode spans (total output over total decode time). turns/steps
 * and the model run time stay branch totals, so they survive turns the browser
 * never loaded. */
export function sessionStats(s: ViewState): SessionStats {
  const path = activePath(s)
  const counted = new Set(path.map(e => e.id))
  const out = emptyStats()
  // Usage/timing across every known turn. `projectTurnStats` merges the loaded
  // window with the compact snapshots; folded turns it dropped (outside the
  // window) still contribute their server totals once.
  const seen = new Set<number>()
  let ttftSum = 0
  let ttftCount = 0
  let decodeMs = 0
  let decodeTokens = 0
  const add = (t: TurnStats) => {
    out.input += t.input
    out.output += t.output
    out.cacheRead += t.cacheRead
    out.cacheWrite += t.cacheWrite
    if (t.hasCost) { out.hasCost = true; out.cost += t.cost }
    if (t.ttftMs > 0) { ttftSum += t.ttftMs; ttftCount += 1 }
    // Reconstruct each turn's decode span from its rate and output; summing
    // them yields total output over total decode time rather than a plain mean
    // of per-turn rates.
    if (t.tps != null && t.tps > 0 && t.output > 0) {
      decodeTokens += t.output
      decodeMs += t.output / t.tps * 1_000
    }
  }
  for (const t of projectTurnStats(s.nodes, s.turnBase, s.compactTurns).values()) {
    seen.add(t.turn)
    add(t)
  }
  for (const turn of s.compactTurns ?? []) {
    if (seen.has(turn.stats.turn)) continue
    seen.add(turn.stats.turn)
    add(turn.stats)
  }
  out.ttftMs = ttftCount > 0 ? ttftSum / ttftCount : 0
  out.tps = decodeMs > 0 ? decodeTokens / (decodeMs / 1_000) : null
  // Start of the newest turn for the composer's live elapsed. activePath is
  // newest-first, so the first user entry is the current turn; a just-sent
  // prompt whose entry has not landed yet falls back to the live node.
  for (const e of path) {
    if (e.type === 'message' && e.message?.role === 'user') { out.turnStartedAt = tsMs(e.message, e.timestamp) ?? 0; break }
  }
  for (let i = s.nodes.length - 1; i >= 0; i--) {
    const n = s.nodes[i]
    if (n.kind !== 'user') continue
    // A second prompt appears optimistically before its entry reaches the
    // branch. Timing the previous persisted prompt includes all the idle time.
    if (n.ts != null) out.turnStartedAt = n.ts
    break
  }
  for (const e of path) {
    if (e.type === 'message' && e.message?.role === 'user') out.turns += 1
    else if (e.type === 'message' && e.message?.role === 'assistant') out.steps += 1
    else if (e.type === 'compaction' && e.usage) out.steps += 1
  }
  const summary = s.compactTurns?.at(-1)
  if (summary && !s.indexLoaded) {
    const at = path.findIndex(e => summary.entryIds.includes(e.id))
    const newer = at >= 0 ? path.slice(0, at) : []
    out.turns = summary.stats.turn + newer.filter(isUserEntry).length
    out.steps = summary.stepCount + newer.filter(e => e.message?.role === 'assistant' || (e.type === 'compaction' && e.usage)).length
  }
  for (const n of s.nodes) {
    if (counted.has(n.id)) continue
    if (n.kind === 'user') out.turns += 1
    else if (n.kind === 'assistant' && !n.streaming) out.steps += 1
  }
  // Session total of the model's actual run time, so the composer strip is not
  // just the current turn's wall clock (which resets on every prompt and
  // vanishes when the run ends). The newest turn is reported separately for the
  // composer to extend live while it runs.
  const spans = runElapsedByTurn(s.records)
  for (const turn of s.compactTurns ?? []) {
    // Folded turns arrive only as server stats; their records were never loaded.
    spans.set(turn.stats.turn, Math.max(spans.get(turn.stats.turn) ?? 0, turn.stats.elapsedMs))
  }
  let newest = 0
  for (const turn of spans.keys()) if (turn > newest) newest = turn
  for (const [turn, ms] of spans) {
    if (turn === newest) out.turnElapsedMs = ms
    else out.elapsedMs += ms
  }
  // Compact opens only a bounded set of turns. Its cumulative prefix keeps
  // session elapsed independent of whether the optional tree index has loaded.
  const anchor = s.compactTurns?.slice().reverse().find(t => t.cumulativeElapsedMs != null)
  if (anchor?.cumulativeElapsedMs != null) {
    // The cumulative prefix is frozen at the snapshot frontier. Subtracting
    // today's extended span from it would make past work shrink as live work
    // grows, hiding exactly that new elapsed time from the total.
    let total = anchor.cumulativeElapsedMs + Math.max(0, (spans.get(anchor.stats.turn) ?? 0) - anchor.stats.elapsedMs)
    for (const [turn, ms] of spans) if (turn > anchor.stats.turn) total += ms
    out.elapsedMs = Math.max(0, total - out.turnElapsedMs)
  }
  return out
}

/** Wall-clock run time per turn from the trajectory records. Records cover the
 * whole branch (index-only entries included), so the session total does not
 * shrink to the loaded window. It mirrors `turnStats`: a turn starts at the
 * prompt's timestamp and ends at the last node's timestamp, with a tool's own
 * `startedAt + durationMs` counting as the tail (an assistant record already
 * carries the completion timestamp, so its `durationMs` is not added again). */
function runElapsedByTurn(records: TrajRecord[]): Map<number, number> {
  const spans = new Map<number, { start: number; end: number }>()
  for (const r of records) {
    if (r.startedAt == null) continue
    const end = r.kind === 'tool' && r.durationMs != null ? r.startedAt + r.durationMs : r.startedAt
    const span = spans.get(r.turn)
    if (!span) spans.set(r.turn, { start: r.startedAt, end })
    else {
      if (r.startedAt < span.start) span.start = r.startedAt
      if (end > span.end) span.end = end
    }
  }
  const out = new Map<number, number>()
  for (const [turn, span] of spans) out.set(turn, Math.max(0, span.end - span.start))
  return out
}

export type TurnStats = {
  /** 1-based turn index — the nth user message on the branch. */
  turn: number
  /** Completed steps in the turn: assistant messages plus settled compactions. */
  steps: number
  /** Unix ms of the turn's opening user message; absent on a folded compact
   * turn, which is always settled. The live divider ticks `now - startedAt`. */
  startedAt?: number
  /** Wall-clock span from the user message to the turn's last persisted node.
   * Falls back to summed step latencies when timestamps are unavailable. */
  elapsedMs: number
  /** Sum of the turn's assistant latencies (used as the elapsed fallback). */
  durationMs: number
  /** Whole prompt of every step: uncached + cacheRead + cacheWrite. */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  hasCost: boolean
  cost: number
  /** Completed tool calls in the turn, and how many of them failed. */
  tools: number
  toolFailures: number
  /** Notable prompt-cache misses in the turn; same rule as `cacheMisses`, so
   * the divider count matches the per-step cache-miss badges. */
  cacheMisses: number
  /** First step's time-to-first-token; 0 when the provider reported none. */
  ttftMs: number
  /** Output tokens per second over the turn's decode spans; null when unknown. */
  tps: number | null
  /** Latest assistant step's time-to-first-token; 0 when the provider reported
   * none. The divider shows this (the most recent message), while `ttftMs`
   * stays the first step for the session average. Optional because the server
   * compact snapshot carries the latest step separately as `lastStep`. */
  lastTtftMs?: number
  /** Latest assistant step's output tokens per second; null when unknown. */
  lastTps?: number | null
  /** True while the turn still has a streaming assistant or a running tool. */
  live: boolean
}

/** A node whose work is still in flight: the streaming bubble, the running
 * tool, or a compaction in progress. Exported so the chat's running placeholder
 * and the fold boundary reuse one definition instead of drifting into checks
 * that forget a compaction. */
export function nodeLive(n: ChatNode): boolean {
  return (n.kind === 'assistant' && !!n.streaming)
    || (n.kind === 'tool' && !!n.running)
    || (n.kind === 'compaction' && !!n.running)
}

/** A tool node whose result has been applied, so it cannot still be running. */
function hasResult(n: ChatNode): boolean {
  return n.kind === 'tool' && n.result !== undefined
}

/**
 * Aggregate a chat branch into per-turn stats, keyed by the id of each turn's
 * last node so the chat can mount a divider right after that node. A turn with
 * no completed step is dropped (a just-sent user message has nothing to report),
 * and a turn that is still streaming/running is marked `live` so the caller can
 * render it with a ticking elapsed (`now - startedAt`) instead of waiting for
 * it to settle. Steps are summed, not averaged, so the strip reads as the cost
 * of the whole turn; `ttftMs` keeps the first step because that is the latency the
 * user actually perceived. Tool calls (total and failed) and notable cache
 * misses are counted per turn as a quick health read for long tool-heavy runs.
 *
 * base is the number of turns on the branch before the loaded window (0 while
 * the tree index has not arrived), so windowed history still numbers its turns
 * absolutely.
 */
export function turnStats(nodes: ChatNode[], base = 0): Map<string, TurnStats> {
  const out = new Map<string, TurnStats>()
  type Acc = TurnStats & { lastAt: number; decodeMs: number; decodeTokens: number; lastId: string }
  let acc: Acc | null = null
  let turn = base
  // Cache-miss detection mirrors `cacheMisses`: `prevPrompt` is the previous
  // completed step's whole prompt and `cacheReported` turns on once any step
  // has shown cache activity (so a provider that never reports caching is not
  // counted). Compaction clears both because the context legitimately changed.
  let prevPrompt = 0
  let cacheReported = false
  const flush = () => {
    if (!acc) return
    // The span reaches the turn's last node timestamp. Tool nodes carry their
    // own start+end, so a turn ending on a tool still counts that tail instead
    // of stopping at the last assistant message.
    if (acc.startedAt != null && acc.startedAt > 0 && acc.lastAt > acc.startedAt) {
      acc.elapsedMs = acc.lastAt - acc.startedAt
    }
    if (acc.elapsedMs === 0) acc.elapsedMs = acc.durationMs
    // Live turns are kept (even before their first step lands) so a caller can
    // tell "still running" apart from "nothing to show"; a settled turn with no
    // step (a just-sent user message) is dropped.
    if (acc.steps > 0 || acc.tools > 0 || acc.live) {
      acc.tps = acc.decodeMs > 0 ? acc.decodeTokens / (acc.decodeMs / 1_000) : null
      out.set(acc.lastId, acc)
    }
    acc = null
  }
  for (const n of nodes) {
    if (n.kind === 'user') {
      flush()
      turn += 1
      acc = {
        turn, steps: 0, elapsedMs: 0, durationMs: 0,
        input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
        hasCost: false, cost: 0, ttftMs: 0, tps: null, lastTtftMs: 0, lastTps: null, live: false,
        tools: 0, toolFailures: 0, cacheMisses: 0,
        startedAt: n.ts ?? 0, lastAt: n.ts ?? 0, decodeMs: 0, decodeTokens: 0, lastId: n.id,
      }
      continue
    }
    if (!acc) continue
    acc.lastId = n.id
    // Parallel tools may settle out of order. Only lifecycle reconciliation,
    // not sibling position, decides whether work is still running.
    acc.live ||= nodeLive(n)
    if ((n.kind === 'assistant' || n.kind === 'compaction') && n.ts != null) acc.lastAt = Math.max(acc.lastAt, n.ts)
    if (n.kind === 'tool' && n.startedAt != null && n.durationMs != null) {
      const endedAt = n.startedAt + n.durationMs
      if (endedAt > acc.lastAt) acc.lastAt = endedAt
    }
    if (n.kind === 'assistant') {
      if (n.streaming) continue
      acc.steps += 1
      if (n.latencyMs != null) acc.durationMs += n.latencyMs
      if (acc.ttftMs === 0 && n.ttftMs != null && n.ttftMs > 0) acc.ttftMs = n.ttftMs
      // The divider reports the most recent message, not the turn's first step,
      // so the latest assistant overwrites these on every step.
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
        // Mirrors `cacheMisses` exactly: a miss is how much of the previous
        // prompt this step failed to read back, once it clears the noise floor
        // and the notice gate. First step, no-cache providers, and appends to
        // the prefix never count.
        const prompt = (u.input ?? 0) + read + write
        if (prompt > 0) {
          if (prevPrompt > 0 && (read + write > 0 || cacheReported)) {
            const missedTokens = Math.min(prevPrompt, prompt) - read
            if (missedTokens > CACHE_MISS_NOISE_FLOOR) {
              const missRatio = missedTokens / prevPrompt
              if (missedTokens >= CACHE_MISS_NOTICE_TOKENS || missRatio >= CACHE_MISS_NOTICE_RATIO) acc.cacheMisses += 1
            }
          }
          prevPrompt = prompt
          cacheReported = cacheReported || read + write > 0
        }
      }
      // Same decode-span rule throughout: no TTFT means no TPS estimate.
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
      // A compaction legitimately rewrites the context, so cache comparison
      // restarts from the next step (pi clears its previous-request state too).
      prevPrompt = 0
      cacheReported = false
      // A compaction has no first-token span, so it clears the latest-step
      // readouts instead of leaving the previous assistant's values showing.
      acc.lastTtftMs = 0
      acc.lastTps = null
      if (!n.running) acc.steps += 1
    }
  }
  flush()
  return out
}

/** Cache-read share of the session's whole prompt, as a percentage. `input` is
 * the summed whole prompt (uncached + cacheRead + cacheWrite). Returns null
 * when nothing was billed or nothing was read from cache. */
export function cacheHitPercent(s: SessionStats): number | null {
  if (s.input <= 0 || s.cacheRead <= 0) return null
  return s.cacheRead / s.input * 100
}

/** One projection for folded, expanded, live and settled turns. A compact
 * snapshot contributes only what the browser has not already accounted for;
 * current observed nodes always win over an older snapshot's live state. */
export function projectTurnStats(nodes: ChatNode[], base = 0, summaries: CompactTurn[] = []): Map<string, TurnStats> {
  const out = turnStats(nodes, base)
  const byTurn = new Map<number, string>()
  for (const [key, stats] of out) byTurn.set(stats.turn, key)
  const current = new Set(nodes.map(n => n.id))
  for (const summary of summaries) {
    const key = byTurn.get(summary.stats.turn) ?? summary.visibleNodeIds.at(-1) ?? summary.id
    if (!current.has(key)) continue
    const observed = out.get(key)
    if (!observed) {
      const latest = lastStepReadout(summary.lastStep)
      out.set(key, { ...summary.stats, lastTtftMs: latest.ttftMs, lastTps: latest.tps })
      continue
    }
    const overlapNodes = (summary.baselineNodes ?? []).filter(n => current.has(n.id))
    const overlap = [...turnStats(overlapNodes).values()][0]
    const merged = { ...observed, startedAt: observed.startedAt || summary.stats.startedAt }
    // Deltas include all numeric additive metrics, not only tool counts. An
    // end event updates a pending snapshot tool's failure without adding it.
    for (const field of ['steps', 'tools', 'toolFailures', 'durationMs', 'input', 'output', 'cacheRead', 'cacheWrite', 'cost', 'cacheMisses'] as const) {
      merged[field] = Math.max(0, summary.stats[field] + observed[field] - (overlap?.[field] ?? 0))
    }
    merged.hasCost = summary.stats.hasCost || observed.hasCost
    merged.elapsedMs = Math.max(summary.stats.elapsedMs, observed.elapsedMs)
    merged.ttftMs = summary.stats.ttftMs || observed.ttftMs
    // Decode duration is recoverable from the snapshot rate and output only
    // when every step reports TTFT; keep the authoritative snapshot estimate
    // until we have the whole turn, rather than inventing a partial average.
    merged.tps = merged.steps === observed.steps ? observed.tps : summary.stats.tps
    // The divider reads the latest assistant step. Observed nodes win (they are
    // current); the snapshot's `lastStep` only fills a turn whose reply is
    // entirely folded away, so the readout does not vanish at keep=0.
    if (observed.lastTtftMs === 0 && observed.lastTps == null) {
      const latest = lastStepReadout(summary.lastStep)
      merged.lastTtftMs = latest.ttftMs
      merged.lastTps = latest.tps
    }
    out.set(key, merged)
  }
  return out
}

/** Latest-step readout for a folded turn whose reply was never sent as a body:
 * recover the rate from the step's own output and decode span, matching
 * `turnStats`. Returns zero/null when the snapshot carries no `lastStep`. */
function lastStepReadout(step: CompactTurn['lastStep']): { ttftMs: number; tps: number | null } {
  if (!step) return { ttftMs: 0, tps: null }
  const ttftMs = step.ttftMs > 0 ? step.ttftMs : 0
  const output = step.usage?.output ?? 0
  const decode = step.latencyMs - step.ttftMs
  const tps = ttftMs > 0 && decode > 0 && output > 0 ? output / (decode / 1_000) : null
  return { ttftMs, tps }
}

/** Cache-read share of a step's prompt, as a percentage (not rounded). `input`
 * is the uncached bucket, so the prompt is input + cacheRead + cacheWrite.
 * Returns null when the step billed no prompt tokens. */
export function cacheHitRate(usage?: Usage | null): number | null {
  if (!usage) return null
  const prompt = (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0)
  if (prompt <= 0) return null
  return (usage.cacheRead ?? 0) / prompt * 100
}

/** Prompt-cache misses at or below this many tokens are cache-breakpoint
 * granularity noise, not a real break. Mirrors pi's `NOISE_FLOOR_TOKENS` and
 * the ~1K-token minimum cacheable prefix OpenAI and Anthropic document, below
 * which a sub-prefix cannot be cached at all. */
export const CACHE_MISS_NOISE_FLOOR = 1024

/** pi's transcript-notice gate: a counted miss only surfaces when it re-billed
 * at least this many tokens, or this fraction of the previous prompt. The
 * ratio catches prefix rewrites on smaller contexts that never reach 20K. */
export const CACHE_MISS_NOTICE_TOKENS = 20_000
export const CACHE_MISS_NOTICE_RATIO = 0.5

export type CacheMiss = {
  /** Tokens that were in the previous step's prompt but not read from cache. */
  missedTokens: number
  /** `missedTokens / previous prompt` — the share of the re-used prefix that
   * was re-billed instead of read from cache. */
  missRatio: number
}

/**
 * Notable prompt-cache misses on a branch, keyed by assistant node id. Uses
 * pi's definition (packages/coding-agent/src/core/cache-stats.ts): a miss is
 * how much of the *previous* request's prompt this request failed to read back
 * from cache — `min(prevPrompt, prompt) - cacheRead`, where
 * `prompt = input + cacheRead + cacheWrite` and `input` is the uncached
 * bucket. A single cached token is not a hit: the delta must exceed the noise
 * floor. Appended new content is never a miss (it was not in the previous
 * prompt); only a rewritten prefix is. The first step, providers that never
 * report cache activity, streaming steps, and sub-noise-floor deltas produce no
 * entry. Compaction resets the comparison because the context legitimately
 * changed (pi clears its previous-request state there too).
 *
 * Only misses large enough to matter are returned: `missedTokens >=
 * CACHE_MISS_NOTICE_TOKENS || missRatio >= CACHE_MISS_NOTICE_RATIO`.
 */
export function cacheMisses(nodes: ChatNode[]): Map<string, CacheMiss> {
  const out = new Map<string, CacheMiss>()
  let prevPrompt = 0
  let reported = false
  for (const n of nodes) {
    if (n.kind === 'compaction') {
      prevPrompt = 0
      reported = false
      continue
    }
    if (n.kind !== 'assistant' || n.streaming) continue
    const u = n.usage
    if (!u) continue
    const read = u.cacheRead ?? 0
    const write = u.cacheWrite ?? 0
    const prompt = (u.input ?? 0) + read + write
    if (prompt <= 0) continue
    // A zero-cache step only counts as a miss once some request has reported
    // cache activity; otherwise the provider may simply not report caching.
    if (prevPrompt > 0 && (read + write > 0 || reported)) {
      const missedTokens = Math.min(prevPrompt, prompt) - read
      if (missedTokens > CACHE_MISS_NOISE_FLOOR) {
        const missRatio = missedTokens / prevPrompt
        if (missedTokens >= CACHE_MISS_NOTICE_TOKENS || missRatio >= CACHE_MISS_NOTICE_RATIO) {
          out.set(n.id, { missedTokens, missRatio })
        }
      }
    }
    prevPrompt = prompt
    reported = reported || read + write > 0
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
