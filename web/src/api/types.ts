import type { DisplayRevision } from '../lib/stream-metrics'
import type { BodyKind } from '../lib/transcriptCoverage'
import type { TurnId } from '../lib/transcriptIdentity'

export type Usage = {
  input?: number
  output?: number
  cacheRead?: number
  cacheWrite?: number
  totalTokens?: number
	 cost?: { input: number; output: number; cacheRead: number; cacheWrite: number; total: number }
}

export type Content = {
  type: string
  text?: string
  thinking?: string
  data?: string
  mimeType?: string
  id?: string
  name?: string
	path?: string
	size?: number
	toolType?: string
	input?: string
  arguments?: Record<string, unknown>
}

export type Message = {
  role: string
  clientRequestId?: string
  completion?: { taskId: string; generation: number }
  content?: Content[]
  origin?: string
	external?: Record<string, string>
  timestamp?: number
  usage?: Usage | null
  stopReason?: string
  errorMessage?: string
  cancelReason?: string
  cancelSource?: string
  toolCallId?: string
  toolName?: string
	toolType?: string
  isError?: boolean
  latencyMs?: number
  ttftMs?: number
  durationMs?: number
	details?: unknown
  model?: string
  provider?: string
}

/** View-only estimates of full persisted content, not provider tokenization. */
export type ContextEstimate = {
  system?: number
  tools?: number
  message?: number
  summary?: number
}

export type Entry = {
  type: string
  /** Browser-only body quality; independent of the persisted entry identity. */
  bodyKind?: BodyKind
  id: string
  parentId?: string
  /** Browser-only traversal bridge while preceding persisted metadata is absent. */
  previousId?: string
  timestamp?: string
  message?: Message
  summary?: string
  /** Public marker only; provider-owned remote checkpoint payload is never exposed. */
  remoteContext?: boolean
  contextEstimate?: ContextEstimate
  firstKeptEntryId?: string
  tokensBefore?: number
  usage?: Usage | null
  details?: unknown
	sideband?: boolean
  provider?: string
  modelId?: string
  system?: string
  tools?: ToolSchema[]
	thinkingEffort?: string
	usedTokens?: number
	contextWindow?: number
	estimated?: boolean
	promptUnchanged?: boolean
	truncated?: boolean
}

export type IndexEntry = {
  type: string
  id: string
  parentId?: string
  timestamp?: string
  role?: string
  name?: string
  preview?: string
  toolCallId?: string
  isError?: boolean
  parentCallId?: string
  cellId?: string
  requestedToolName?: string
  truncated?: boolean
  usage?: Usage | null
  durationMs?: number
  ttftMs?: number
  origin?: string
  sideband?: boolean
  tokensBefore?: number
  stopReason?: string
  remoteContext?: boolean
  contextEstimate?: ContextEstimate
}

export type ToolSchema = {
	type?: string
  name: string
  description?: string
  parameters?: Record<string, unknown>
	format?: { type: string; syntax: string; definition: string }
}

export type ProcessSnapshot = {
 session_id: number; revision?: number; owner_session_id?: string; run_id?: string; tool_call_id?: string; agent_id?: string; generation?: number; cmd?: string; workdir?: string; tty: boolean
 status: 'running' | 'exited'; output?: string; output_file?: string; exit_code?: number
 total_bytes: number; started_at: string; finished_at?: string; error?: string
}
export type AgentRunStats = {
 mailbox_wait_ms?: number; tools: number; requests: number; tool_failures: number; input_tokens: number; output_tokens: number
 cache_read_tokens: number; cache_write_tokens: number; total_tokens: number; context_tokens: number
}
export type AgentSnapshot = {
 pending_tasks?: number; lifetime_stats_complete?: boolean; queue_wait_ms?: number; revision?: number; run_id?: string; phase?: string; waiting_for?: string; last_activity_at?: string
 current_tools?: { call_id: string; name: string; started_at: string }[]; run_stats?: AgentRunStats; agent_lifetime_stats?: AgentRunStats
 task_name: string; session_id: string; agent_id?: string; generation?: number; status: string
 description?: string; started_at?: string; finished_at?: string; tool_use_count?: number; total_tokens?: number
}

export type LoopEvent = {
 process?: ProcessSnapshot
 agent?: AgentSnapshot
 requestedToolName?: string
  /** Local monotonic receive/queue times; never sent to the server. */
  display?: DisplayRevision
  messageStream?: number
  messagePatch?: { baseSeq: number; changes: { path: string[]; op: 'set' | 'append' | 'remove' | 'resize'; value?: unknown }[] }
  type: string
	role?: string
	runId?: string
	external?: Record<string, string>
	entryId?: string
  /** Durable lifecycle row; entryId on compaction_end remains the checkpoint. */
  lifecycleEntryId?: string
	parentId?: string
	timestamp?: number
	durationMs?: number
  message?: Message
  toolCallId?: string
  parentCallId?: string
  cellId?: string
  toolName?: string
  args?: Record<string, unknown>
  result?: unknown
	partialResult?: unknown
  isError?: boolean
  assistantMessageEvent?: { type: string; delta?: string; partial?: Message }
  system?: string
  tools?: ToolSchema[]
	/**
	 * The server drops the system/tools body of a replayed request_header that
	 * repeats the previous one and sets this instead; the client reuses the
	 * prompt it already has (same rule as the persisted entry).
	 */
	promptUnchanged?: boolean
	/** Per-run SSE sequence number, the cursor a client resumes from. */
	seq?: number
  reason?: string
  cancelSource?: string
  ok?: boolean
	status?: 'committed' | 'empty' | 'cancelled' | 'failed' | string
	willRetry?: boolean
	strategy?: string
	fromExtension?: boolean
	firstKeptEntryId?: string
	tokensBefore?: number
	usage?: Usage
	provider?: string
	model?: string
	catalogVersion?: number
	usedTokens?: number
	contextWindow?: number
	estimated?: boolean
	server?: string
	messageText?: string
	reloadRequired?: boolean
	options?: string[]
}

export type AuthStatus = {
  authenticated: boolean
  serverId: string
  csrfCookieName: string
}

/**
 * One frame of `GET /v1/events`:
 *   - `{ type: 'ready', serverId }` — the subscription is live; refetch
 *     everything, since the stream replays nothing. A changed identity
 *     requires reauthentication before using state from this instance.
 *   - `{ type: 'invalidate', scope }` — that slice of state changed; refetch it
 *     through the ordinary REST endpoint.
 *   - a session sideband loop event carrying the `sessionId` it belongs to.
 */
export type PushEvent = LoopEvent & {
  serverId?: string
	sessionId?: string
	scope?: string
}

export type SessionInfo = {
  id: string
  cwd: string
  dir?: string
  provider: string
  model: string
  timestamp?: string
  updatedAt?: string
  parentSessionId?: string
  forkMode?: 'flat' | 'tree'
  title: string
  running?: boolean
  activeDescendantCount?: number
  workspaceId?: string
  pinned?: boolean
  pinnedAt?: string
	thinkingEffort?: string
	metadata?: Record<string, unknown>
}

export type WorkspaceInfo = {
  id: string
  path: string
  title: string
  createdAt?: string
  updatedAt?: string
  status?: string
  temp?: boolean
  sessionIds?: string[]
}

export type FsEntry = { name: string; path: string; hidden: boolean; directory?: boolean; size?: number }

export type FsListing = {
  path: string
  home: string
  separator: string
  crumbs: FsEntry[]
  entries: FsEntry[]
  truncated: boolean
}

export type SearchHit = {
  id: string
  title: string
  cwd?: string
  model?: string
  updatedAt?: string
  workspaceId?: string
  workspaceTitle?: string
  snippet?: string
}

export type CatalogSkill = {
  name: string
  description?: string
  path?: string
  source?: string
  enabled: boolean
}

export type CatalogTool = {
  name: string
  description?: string
  source?: string
  enabled: boolean
  available?: boolean
}

export type MCPServerInfo = {
  name: string
  source: 'global' | 'project' | 'runtime'
  transport: 'stdio' | 'http'
  enabled: boolean
  configuredEnabled: boolean
  tools?: number
}

export type ToolSettings = {
  items: CatalogTool[]
  mcp: MCPServerInfo[]
}

export type ToolSettingsPatch = {
  disabled?: string[]
  mcpDisabled?: string[]
}

export type ExtensionText = string | number | boolean | {
  key: string
  params?: Record<string, string | number>
  fallback?: string
}

export type ExtensionI18n = {
  defaultLocale?: string
  resources?: Record<string, Record<string, string>>
}

export type CatalogContribution = {
  name: string
  description?: string
  source?: string
}

export type CatalogProvider = {
  id: string
  name?: string
  api?: string
}

export type CatalogExtension = {
  name: string
  version?: string
  description?: string
  path?: string
  enabled: boolean
  capabilities?: string[]
  error?: string
	configurable?: boolean
	runtime?: { name: string; state: string; error?: string; capabilities?: string[] }
	ui?: ExtensionUI
	i18n?: ExtensionI18n
	skills?: CatalogContribution[]
	tools?: CatalogContribution[]
	commands?: CatalogContribution[]
	promptAppend?: string[]
	pathDirs?: { path: string; exists: boolean }[]
	providers?: CatalogProvider[]
}

export type ExtensionConfig = {
	name: string
	schema: Record<string, unknown>
	config: Record<string, unknown>
	i18n?: ExtensionI18n
}

/** One appended-system-prompt source as the settings editor sees it. */
export type PromptAppendItem = {
	source: 'builtin' | 'global' | 'project' | 'extension' | string
	editable: boolean
	available: boolean
	path?: string
	exists?: boolean
	text: string
	bytes: number
	/** Why an editable source cannot be written, e.g. workspace-required. */
	reason?: string
	name?: string
	paths?: string[]
}

export type PromptAppendView = {
	items: PromptAppendItem[]
	/** The append stack exactly as the model receives it, in render order. */
	effective: string
}

export type SessionCommand = {
  name: string
  description?: string
  argumentHint?: string
  completions?: string[]
  source: 'builtin' | 'prompt' | 'skill' | 'extension' | string
  extension?: string
}

export type QueuedItem = {
  id: string
  clientRequestId?: string
  completion?: { taskId: string; generation: number }
  content?: Content[]
  extension?: string
}

/** Projected GET responses may carry only id plus the requested fields. */
export type SessionDetail = Partial<SessionInfo> & {
  id: string
  leafId?: string
  entries?: Entry[]
  compactTurns?: CompactTurn[]
  index?: IndexEntry[]
  hasMore?: boolean
  oldestId?: string
  availableSkills?: CatalogSkill[]
  availableExtensions?: CatalogExtension[]
  availableMCP?: MCPServerInfo[]
  commands?: SessionCommand[]
  queued?: QueuedItem[]
  processes?: ProcessSnapshot[]
  agents?: AgentSnapshot[]
  extQueued?: QueuedItem[]
  extensionUi?: ExtensionUI[]
  runtime?: { ready: boolean }
  /** fields=system: the newest request_header's full system prompt. */
  systemPrompt?: string
}

/** Whole-turn sparse projection. Hidden reply bodies are fetched on expansion. */
export type CompactTurn = {
  id: string
  parentId?: string
  tailId: string
  entryIds: string[]
  visibleNodeIds: string[]
  omittedNodeIds?: string[]
  hiddenCount: number
  entryCount?: number
  stepCount: number
  cumulativeElapsedMs?: number
  toolStates?: { id: string; finished: boolean; isError?: boolean }[]
  assistantAt?: number
  /** Browser-only overlap reconstructed from the immutable snapshot frontier. */
  baselineNodes?: ChatNode[]
  lastStep?: { usage?: Usage; ttftMs: number; latencyMs: number }
  firstHiddenId?: string
  preview?: string
  stats: {
    turn: number; steps: number; elapsedMs: number; durationMs: number; startedAt?: number
    input: number; output: number; cacheRead: number; cacheWrite: number
    tools: number; toolFailures: number; cacheMisses: number
    hasCost: boolean; cost: number; ttftMs: number; tps: number | null; live: boolean
  }
}

export type ExtensionUI = {
  extension: string
  status?: { key: string; text: ExtensionText; tone?: string }
  panel?: {
    title?: ExtensionText
    summary?: ExtensionText
    sections?: Array<Record<string, unknown>>
    actions?: Array<{ id: string; label: ExtensionText; style?: string; disabled?: boolean; title?: ExtensionText }>
    fields?: Array<{ id: string; label?: ExtensionText; type?: string; value?: unknown; options?: string[] }>
    submitLabel?: ExtensionText
  }
  prompt?: { kind: string; title?: ExtensionText; message?: ExtensionText; options?: string[] }
}

export type Toggle = { only?: string[]; disabled?: string[] }

export type ModelInfo = {
  provider: string
  id: string
	name: string
  api?: string
  contextWindow?: number
	maxTokens?: number
	input?: string[]
	applyPatchToolType?: 'freeform'
	execToolType?: 'freeform'
	compaction?: {
		standalone?: 'openai' | 'codex-v2'
		inline?: 'openai'
	}
	reasoning?: boolean
	fastServiceTier?: string
	thinkingLevels?: string[]
	defaultThinking?: string
	builtin?: boolean
	customized?: boolean
  spec: string
}

export type ProviderModel = Omit<ModelInfo, 'spec' | 'thinkingLevels' | 'defaultThinking'> & {
	enabled: boolean
	builtin: boolean
	customized?: boolean
	baseUrl: string
	cost?: { input: number; output: number; cacheRead: number; cacheWrite: number } | null
	thinkingLevelMap?: Record<string, string | null>
	compat?: Record<string, unknown>
}

export type ProviderView = {
	id: string
	name: string
	api: string
	baseUrl: string
	auth?: { type?: string; name?: string; subscription?: boolean }
	runtime?: string
	enabled: boolean
	builtin: boolean
	customized?: boolean
	defaultModel: string
	models: ProviderModel[]
	credential: { configured: boolean; source?: string; type?: string }
}

export type ProviderCatalog = {
	version: number
	default: { provider: string; model: string }
	providers: ProviderView[]
}

export type ProviderAuthStatus = {
	provider: string
	requestId: string
	status: 'pending' | 'completed' | 'error' | 'cancelled' | string
	eventType?: string
	authUrl?: string
	instructions?: string
	userCode?: string
	verificationUri?: string
	intervalSeconds?: number
	expiresInSeconds?: number
	error?: string
}

export type Meta = {
	home: string
	provider: string
	model: string
	thinkingEffort?: string
}

export type ChatNode = { turnId?: TurnId } & (
  | { kind: 'user'; id: string; parentId?: string; text: string; content: Content[]; ts?: number; origin?: string; clientRequestId?: string; completion?: Message['completion']; external?: Record<string, string>; truncated?: boolean }
  | { kind: 'assistant'; id: string; renderKey?: string; display?: DisplayRevision; parentId?: string; text: string; thinking?: string; usage?: Usage | null; ttftMs?: number; latencyMs?: number; streaming?: boolean; error?: string; cancelReason?: string; cancelSource?: string; images?: { data: string; mimeType: string }[]; stopReason?: string; ts?: number; truncated?: boolean }
  | { kind: 'tool'; id: string; name: string; parentCallId?: string; cellId?: string; requestedToolName?: string; args?: unknown; result?: string; details?: unknown; isError?: boolean; durationMs?: number; startedAt?: number; running?: boolean; truncated?: boolean }
  | { kind: 'compaction'; id: string; summary: string; ts?: number; tokensBefore?: number; running?: boolean; failed?: boolean; empty?: boolean; unknown?: boolean; truncated?: boolean; lifecycle?: boolean }
  | { kind: 'cancellation'; id: string; runId?: string; reason?: string; source?: string; ts?: number; truncated?: boolean }
)

export type PromptSnapshot = {
  provider?: string
  model?: string
  thinkingEffort?: string
  system: string
  tools: ToolSchema[]
}

export type PromptChange = {
  kind: 'initial' | 'system' | 'tools' | 'system-and-tools'
  seq?: string
  time?: number
  previous?: PromptSnapshot
}

export type RequestView = {
  id: string
  turnId?: TurnId
  turn: number
  step: number
  startedAt?: number
  completedAt?: number
  status: 'running' | 'complete' | 'error'
  error?: string
  provider?: string
  model?: string
  thinkingEffort?: string
  prompt?: PromptSnapshot
  promptChange?: PromptChange
  resultId?: string
  usage?: Usage | null
  ttftMs?: number
  durationMs?: number
  retry?: number
}

export type TrajKind = 'user' | 'assistant' | 'tool' | 'subtool' | 'compacted' | 'compact' | 'system' | 'context'

export type TrajRecord = {
  turnId?: TurnId
  truncated?: boolean
  id: string
	parentId?: string
  parentCallId?: string
  cellId?: string
  requestedToolName?: string
  kind: TrajKind
  turn: number
  step?: number
  preview: string
  input?: unknown
  output?: unknown
	details?: unknown
  usage?: Usage | null
  durationMs?: number
  ttftMs?: number
  startedAt?: number
  running?: boolean
  requestOnly?: boolean
  requestId?: string
  prompt?: PromptSnapshot
  promptChange?: PromptChange
  name?: string
  error?: boolean
  system?: string
  tools?: ToolSchema[]
  previousSystem?: string
  previousTools?: ToolSchema[]
  sourceBlocks?: Content[]
  outputBlocks?: Content[]
}

export type ViewState = {
  turnId?: TurnId
  /** Live events and accepted local writes revoke older snapshot authority. */
  liveRevision: number
  /** Tool execution overlays until their immutable toolResult arrives. */
  toolStates?: Record<string, Extract<ChatNode, { kind: 'tool' }>>
  nodes: ChatNode[]
  records: TrajRecord[]
  requests: RequestView[]
  busy: boolean
  stopping?: boolean
  error: string | null
  model: string
  provider: string
  cwd: string
  title: string
  turn: number
  skills?: Toggle
	thinkingEffort: string
	contextUsage?: { usedTokens: number; contextWindow: number; estimated: boolean }
	leafId?: string
	/**
	 * Body-loaded entries: the tail window the server returned plus every page
	 * hydrated since. Chat nodes are built from these only, so a long session
	 * renders a bounded number of them.
	 */
	entries: Entry[]
	compactTurns?: CompactTurn[]
	/** Complete node structure through each compact frontier; slim text may remain. */
	loadedTurnIds?: TurnId[]
	/**
	 * Body-less rows for the whole tree. Fetched lazily (fields=index) because
	 * carrying it on open made first paint wait for the full transcript; the
	 * trajectory table, branch navigation and absolute turn numbers need it.
	 */
	index: IndexEntry[]
	/** Whether `index` covers the tree (an empty tree counts as loaded). */
	indexLoaded: boolean
	/** Users on the branch before the loaded window, known from the index. */
	turnBase: number
	allEntries: Entry[]
	hasMore?: boolean
	oldestId?: string
  commands?: SessionCommand[]
  queued?: QueuedItem[]
  processes?: ProcessSnapshot[]
  agents?: AgentSnapshot[]
  extQueued?: QueuedItem[]
  extensionUi?: ExtensionUI[]
  runtimeReady?: boolean
	promptState?: PromptSnapshot
	currentRequestId?: string
}
