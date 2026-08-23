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
  content?: Content[]
  timestamp?: number
  usage?: Usage | null
  stopReason?: string
  errorMessage?: string
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

export type Entry = {
  type: string
  id: string
  parentId?: string
  timestamp?: string
  message?: Message
  summary?: string
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
}

export type ToolSchema = {
	type?: string
  name: string
  description?: string
  parameters?: Record<string, unknown>
	format?: { type: string; syntax: string; definition: string }
}

export type LoopEvent = {
  type: string
	entryId?: string
  message?: Message
  toolCallId?: string
  toolName?: string
  args?: Record<string, unknown>
  result?: unknown
	partialResult?: unknown
  isError?: boolean
  assistantMessageEvent?: { type: string; delta?: string; partial?: Message }
  system?: string
  tools?: ToolSchema[]
  reason?: string
  ok?: boolean
	provider?: string
	model?: string
	catalogVersion?: number
	usedTokens?: number
	contextWindow?: number
	estimated?: boolean
	server?: string
	messageText?: string
	reloadRequired?: boolean
}

export type SessionInfo = {
  id: string
  cwd: string
  dir?: string
  provider: string
  model: string
  timestamp?: string
  parent?: string
  title: string
  running?: boolean
  workspaceId?: string
  pinned?: boolean
  pinnedAt?: string
	thinkingEffort?: string
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

export type CatalogMcp = {
  name: string
  command?: string
  args?: string[]
  url?: string
  source?: string
  enabled: boolean
  tools?: { name: string; description?: string }[]
	status?: 'unloaded' | 'ready' | 'failed' | 'stale'
	error?: string
}

export type SessionCommand = {
  name: string
  description?: string
  argumentHint?: string
  source: 'builtin' | 'prompt' | 'skill' | string
}

export type SessionDetail = SessionInfo & {
  leafId?: string
  entries?: Entry[]
  messages?: Message[]
  availableSkills?: CatalogSkill[]
  availableMcp?: CatalogMcp[]
  commands?: SessionCommand[]
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
	reasoning?: boolean
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
	applyPatchToolType?: 'freeform'
	compat?: Record<string, unknown>
}

export type ProviderView = {
	id: string
	name: string
	api: 'completions' | 'responses' | 'anthropic'
	baseUrl: string
	enabled: boolean
	builtin: boolean
	customized?: boolean
	defaultModel: string
	models: ProviderModel[]
	credential: { configured: boolean; source?: string }
}

export type ProviderCatalog = {
	version: number
	default: { provider: string; model: string }
	providers: ProviderView[]
}

export type Meta = {
	home: string
	provider: string
	model: string
	thinkingEffort?: string
}

export type ChatNode =
  | { kind: 'user'; id: string; parentId?: string; text: string; content: Content[]; ts?: number }
  | { kind: 'assistant'; id: string; parentId?: string; text: string; thinking?: string; usage?: Usage | null; ttftMs?: number; latencyMs?: number; streaming?: boolean; error?: string; images?: { data: string; mimeType: string }[]; stopReason?: string; ts?: number }
  | { kind: 'tool'; id: string; name: string; args?: unknown; result?: string; details?: unknown; isError?: boolean; durationMs?: number; running?: boolean }
  | { kind: 'compaction'; id: string; summary: string; tokensBefore?: number }

export type TrajKind = 'user' | 'assistant' | 'tool' | 'compacted' | 'compact' | 'system'

export type TrajRecord = {
  id: string
	parentId?: string
  kind: TrajKind
  turn: number
  preview: string
  input?: unknown
  output?: unknown
	details?: unknown
  usage?: Usage | null
  durationMs?: number
  ttftMs?: number
  startedAt?: number
  running?: boolean
  name?: string
  error?: boolean
  system?: string
  tools?: ToolSchema[]
  previousSystem?: string
  previousTools?: ToolSchema[]
}

export type ViewState = {
  nodes: ChatNode[]
  records: TrajRecord[]
  busy: boolean
  error: string | null
  model: string
  provider: string
  cwd: string
  title: string
  turn: number
  replayed?: number
  skills?: Toggle
  mcp?: Toggle
	thinkingEffort: string
	contextUsage?: { usedTokens: number; contextWindow: number; estimated: boolean }
	leafId?: string
	allEntries: Entry[]
  commands?: SessionCommand[]
}
