export type Usage = {
  input?: number
  output?: number
  cacheRead?: number
  cacheWrite?: number
  totalTokens?: number
}

export type Content = {
  type: string
  text?: string
  thinking?: string
  data?: string
  mimeType?: string
  id?: string
  name?: string
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
  isError?: boolean
  latencyMs?: number
  ttftMs?: number
  durationMs?: number
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
  provider?: string
  modelId?: string
  system?: string
  tools?: ToolSchema[]
}

export type ToolSchema = {
  name: string
  description?: string
  parameters?: Record<string, unknown>
}

export type LoopEvent = {
  type: string
  message?: Message
  toolCallId?: string
  toolName?: string
  args?: Record<string, unknown>
  result?: unknown
  isError?: boolean
  assistantMessageEvent?: { type: string; delta?: string; partial?: Message }
  system?: string
  tools?: ToolSchema[]
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
}

export type SessionDetail = SessionInfo & {
  leafId?: string
  entries?: Entry[]
  messages?: Message[]
  skills?: Toggle
  mcp?: Toggle
}

export type Toggle = { only?: string[]; disabled?: string[] }

export type ModelInfo = {
  provider: string
  id: string
  api?: string
  contextWindow?: number
  spec: string
}

export type ChatNode =
  | { kind: 'user'; id: string; text: string; ts?: number }
  | { kind: 'assistant'; id: string; text: string; thinking?: string; usage?: Usage | null; ttftMs?: number; latencyMs?: number; streaming?: boolean; error?: string; images?: { data: string; mimeType: string }[]; ts?: number }
  | { kind: 'tool'; id: string; name: string; args?: unknown; result?: string; isError?: boolean; durationMs?: number; running?: boolean }
  | { kind: 'compaction'; id: string; summary: string; tokensBefore?: number }

export type TrajKind = 'user' | 'assistant' | 'tool' | 'compacted' | 'system'

export type TrajRecord = {
  id: string
  kind: TrajKind
  turn: number
  preview: string
  input?: unknown
  output?: unknown
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
}
