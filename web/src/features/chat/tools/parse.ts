import type { Content, ProcessSnapshot } from '../../../api/types'

export type NestedCall = { text: string; isError: boolean }

export type CodeModeSnapshot = {
  status: string
  cellId: string
  body: string
  error: string
  images: { data: string; mimeType: string }[]
  truncated: boolean
  calls: NestedCall[] | null
}

export type CodeModePreview = { text: string; calls?: number; status?: string }
export type CodeModeStatusKey = 'tool.cell.completed' | 'tool.cell.running' | 'tool.cell.terminated' | 'tool.cell.failed'

const CODE_STATUS_KEYS: Record<string, CodeModeStatusKey> = {
  completed: 'tool.cell.completed',
  running: 'tool.cell.running',
  terminated: 'tool.cell.terminated',
  failed: 'tool.cell.failed',
}

export function codeModeStatusKey(status: string): CodeModeStatusKey | null {
  return CODE_STATUS_KEYS[status] ?? null
}

export type ExecSnapshot = {
  cmd: string
  workdir: string
  tty: boolean
  status: string
  exitCode?: number
  output: string
  outputFile?: string
  truncated: boolean
  totalBytes?: number
  sessionID?: number
  startedAt?: string
  finishedAt?: string
}

export type SpillReference = {
  path: string
  bytes: number
  totalBytes: number
  lines: number
  truncated: boolean
  incomplete?: boolean
}

export type DiffFile = {
  path: string
  kind: string
  movePath?: string
  diff: string
}

export type GrepHit = { path: string; line: number; text: string; truncated?: boolean }
export type GrepDetails = {
  mode: string
  files: number
  matches: number
  truncated: boolean
  hits?: GrepHit[]
  counts?: { path: string; count: number }[]
  paths?: string[]
  offset?: number
  limit?: number
}

export type GlobDetails = {
  paths?: string[]
  root: string
  files: number
  limit: number
  truncated: boolean
}

export type WriteDetails = { path: string; bytes: number; lines: number; created: boolean }

export type ReadDetails = {
  truncation?: {
    truncated: boolean
    reason?: string
    total_bytes: number
    total_lines: number
    output_bytes: number
    output_lines: number
    next_offset?: number
  }
  image?: { width: number; height: number; mime_type: string; bytes: number; resized: boolean }
}

function object(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function num(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function bool(value: unknown): boolean {
  return value === true
}

export function spillOf(details: unknown): SpillReference | null {
  const spill = object(object(details)?.spill)
  if (!spill) return null
  const path = str(spill.path)
  if (!path) return null
  return {
    path,
    bytes: num(spill.bytes) ?? 0,
    totalBytes: num(spill.totalBytes) ?? 0,
    lines: num(spill.lines) ?? 0,
    truncated: bool(spill.truncated),
    incomplete: bool(spill.incomplete),
  }
}

function snapshotSource(details: unknown): Record<string, unknown> | null {
  const outer = object(details)
  if (!outer) return null
  const inner = object(outer.process)
  return inner ?? outer
}

export function execSnapshot(details: unknown, result: string): ExecSnapshot | null {
  const raw = snapshotSource(details)
  if (!raw) return null
  const sessionID = num(raw.session_id)
  if (sessionID === undefined || num(raw.revision) === undefined) return null
  const exitCode = num(raw.exit_code)
  return {
    cmd: str(raw.cmd),
    workdir: str(raw.workdir),
    tty: bool(raw.tty),
    status: str(raw.status),
    exitCode,
    output: str(raw.output),
    outputFile: str(raw.output_file) || undefined,
    truncated: bool(raw.truncated),
    totalBytes: num(raw.total_bytes),
    sessionID,
    startedAt: str(raw.started_at) || undefined,
    finishedAt: str(raw.finished_at) || undefined,
  }
}

export function execSnapshotFromResult(result: string): ExecSnapshot | null {
  const text = result.trim()
  if (!text.startsWith('{')) return null
  try {
    return execSnapshot(JSON.parse(text), text)
  } catch {
    return null
  }
}

export function execSnapshotOf(details: unknown, result: string): ExecSnapshot | null {
  return execSnapshot(details, result) ?? execSnapshotFromResult(result)
}

export function execSnapshotFromProcess(process: ProcessSnapshot): ExecSnapshot {
  return {
    cmd: process.cmd ?? '',
    workdir: process.workdir ?? '',
    tty: process.tty,
    status: process.status,
    exitCode: process.exit_code,
    output: process.output ?? '',
    outputFile: process.output_file,
    truncated: false,
    totalBytes: process.total_bytes,
    sessionID: process.session_id,
    startedAt: process.started_at,
    finishedAt: process.finished_at,
  }
}

const CODE_STATUSES = ['completed', 'terminated', 'failed'] as const

function codeHeader(text: string): { status: string; cellId: string; rest: string } | null {
  const running = /^Script running with cell ID ([^\s]+)/.exec(text)
  if (running) {
    const header = running[0]
    return { status: 'running', cellId: running[1], rest: text.slice(header.length) }
  }
  for (const status of CODE_STATUSES) {
    const header = `Script ${status}`
    if (text.startsWith(header)) return { status, cellId: '', rest: text.slice(header.length) }
  }
  return null
}

export function codeModeSource(args: unknown): string {
  if (typeof args === 'string') return args
  const raw = object(args)
  if (!raw) return ''
  return str(raw.code) || str(raw.input)
}

export function nestedCalls(result: string): NestedCall[] | null {
  const text = result.trim()
  let rest = text
  for (const prefix of ['Script completed', 'Script running']) {
    if (rest.startsWith(prefix)) {
      rest = rest.slice(prefix.length).trimStart()
      break
    }
  }
  if (!rest.startsWith('{') && !rest.startsWith('[')) return null
  const calls: NestedCall[] = []
  let index = 0
  while (index < rest.length) {
    while (index < rest.length && /\s/.test(rest[index])) index++
    if (index >= rest.length) break
    const end = jsonValueEnd(rest, index)
    if (end < 0) return null
    let value: unknown
    try {
      value = JSON.parse(rest.slice(index, end))
    } catch {
      return null
    }
    index = end
    const envelope = value as { content?: { text?: string }[]; isError?: boolean }
    const body = Array.isArray(envelope.content)
      ? envelope.content.map(block => block?.text ?? '').join('')
      : typeof value === 'string' ? value : JSON.stringify(value)
    calls.push({ text: body, isError: !!envelope.isError })
  }
  return calls.length ? calls : null
}

function jsonValueEnd(text: string, start: number): number {
  const open = text[start]
  if (open !== '{' && open !== '[') return -1
  const stack: string[] = [open === '{' ? '}' : ']']
  let inString = false
  let escaped = false
  for (let i = start + 1; i < text.length; i++) {
    const ch = text[i]
    if (inString) {
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === '"') inString = false
      continue
    }
    if (ch === '"') {
      inString = true
      continue
    }
    if (ch === '{') stack.push('}')
    else if (ch === '[') stack.push(']')
    else if (ch === '}' || ch === ']') {
      if (stack.pop() !== ch) return -1
      if (stack.length === 0) return i + 1
    }
  }
  return -1
}

export function codeModeSnapshot(result: string, blocks?: Content[] | null, details?: unknown): CodeModeSnapshot {
  const meta = object(details)
  let status = meta ? str(meta.status) : ''
  let cellId = meta ? str(meta.cell_id) : ''
  const truncated = meta ? bool(meta.truncated) : false
  const images: { data: string; mimeType: string }[] = []
  const texts: string[] = []

  if (blocks?.length) {
    for (const block of blocks) {
      if (block.type === 'image') {
        if (block.data) images.push({ data: block.data, mimeType: block.mimeType || 'image/png' })
        continue
      }
      if (block.type && block.type !== 'text') continue
      texts.push(block.text ?? '')
    }
  } else if (result) {
    texts.push(result)
  }

  const outputs: string[] = []
  texts.forEach((text, index) => {
    if (index === 0) {
      const found = codeHeader(text)
      if (found) {
        if (!status) status = found.status
        if (!cellId) cellId = found.cellId
        if (found.rest) outputs.push(found.rest)
        return
      }
    }
    outputs.push(text)
  })

  const joined = outputs.join('\n')
  const errorMarker = 'Script error:\n'
  const errorAt = joined.indexOf(errorMarker)
  const body = errorAt < 0 ? joined : joined.slice(0, errorAt).replace(/\n+$/, '')
  const error = errorAt < 0 ? '' : joined.slice(errorAt + errorMarker.length)
  const calls = nestedCalls(body)

  return {
    status,
    cellId,
    body: calls ? '' : body,
    error,
    images,
    truncated,
    calls,
  }
}

function firstNonEmpty(text: string): string {
  return text.split('\n').find(line => line.trim()) ?? ''
}

export function codeModePreview(snapshot: CodeModeSnapshot, source: string): CodeModePreview {
  const text = firstNonEmpty(snapshot.error || snapshot.body)
  if (text) return { text }
  if (snapshot.calls?.length) return { text: '', calls: snapshot.calls.length }
  if (snapshot.status === 'running') return { text: snapshot.cellId ? `#${snapshot.cellId}` : '', status: 'running' }
  if (snapshot.status === 'failed' || snapshot.status === 'terminated') return { text: '', status: snapshot.status }
  const code = firstNonEmpty(source)
  if (code) return { text: code }
  return { text: '', status: snapshot.status || undefined }
}

export function diffFiles(details: unknown): DiffFile[] {
  const outer = object(details)
  if (!outer) return []
  const files: DiffFile[] = []
  const single = str(outer.diff)
  if (single) {
    files.push({ path: diffPath(single), kind: 'update', diff: single })
  }
  if (Array.isArray(outer.changes)) {
    for (const entry of outer.changes) {
      const change = object(entry)
      if (!change) continue
      const diff = str(change.unified_diff)
      if (!diff) continue
      files.push({
        path: str(change.path),
        kind: str(change.kind) || 'update',
        movePath: str(change.move_path) || undefined,
        diff,
      })
    }
  }
  return files
}

function diffPath(diff: string): string {
  const match = /^\+\+\+ (?:[ab]\/)?(.+)$/m.exec(diff)
  return match ? match[1].trim() : ''
}

export function grepDetails(details: unknown): GrepDetails | null {
  const raw = object(details)
  if (!raw || typeof raw.mode !== 'string') return null
  const hits = Array.isArray(raw.hits)
    ? raw.hits.flatMap(entry => {
      const hit = object(entry)
      if (!hit) return []
      const line = num(hit.line)
      if (line === undefined) return []
      return [{ path: str(hit.path), line, text: str(hit.text), truncated: bool(hit.truncated) }]
    })
    : undefined
  const counts = Array.isArray(raw.counts)
    ? raw.counts.flatMap(entry => {
      const count = object(entry)
      const value = count ? num(count.count) : undefined
      return count && value !== undefined ? [{ path: str(count.path), count: value }] : []
    })
    : undefined
  return {
    mode: raw.mode,
    files: num(raw.files) ?? 0,
    matches: num(raw.matches) ?? 0,
    truncated: bool(raw.truncated),
    hits,
    counts,
    paths: Array.isArray(raw.paths) ? raw.paths.filter((p): p is string => typeof p === 'string') : undefined,
    offset: num(raw.offset),
    limit: num(raw.limit),
  }
}

export function globDetails(details: unknown): GlobDetails | null {
  const raw = object(details)
  if (!raw || num(raw.files) === undefined) return null
  return {
    paths: Array.isArray(raw.paths) ? raw.paths.filter((p): p is string => typeof p === 'string') : undefined,
    root: str(raw.root),
    files: num(raw.files) ?? 0,
    limit: num(raw.limit) ?? 0,
    truncated: bool(raw.truncated),
  }
}

export function writeDetails(details: unknown): WriteDetails | null {
  const raw = object(details)
  const path = raw ? str(raw.path) : ''
  if (!raw || !path) return null
  return { path, bytes: num(raw.bytes) ?? 0, lines: num(raw.lines) ?? 0, created: bool(raw.created) }
}

export function readDetails(details: unknown): ReadDetails | null {
  const raw = object(details)
  if (!raw) return null
  const truncation = object(raw.truncation)
  const image = object(raw.image)
  if (!truncation && !image) return null
  return {
    truncation: truncation
      ? {
        truncated: bool(truncation.truncated),
        reason: str(truncation.reason) || undefined,
        total_bytes: num(truncation.total_bytes) ?? 0,
        total_lines: num(truncation.total_lines) ?? 0,
        output_bytes: num(truncation.output_bytes) ?? 0,
        output_lines: num(truncation.output_lines) ?? 0,
        next_offset: num(truncation.next_offset),
      }
      : undefined,
    image: image
      ? {
        width: num(image.width) ?? 0,
        height: num(image.height) ?? 0,
        mime_type: str(image.mime_type),
        bytes: num(image.bytes) ?? 0,
        resized: bool(image.resized),
      }
      : undefined,
  }
}

export function diffStat(files: DiffFile[]): { added: number; removed: number } {
  let added = 0
  let removed = 0
  for (const file of files) {
    for (const line of file.diff.split('\n')) {
      if (line.startsWith('+') && !line.startsWith('+++')) added++
      else if (line.startsWith('-') && !line.startsWith('---')) removed++
    }
  }
  return { added, removed }
}

export function syntheticEdit(args: unknown, oldText: string, newText: string): DiffFile {
  const raw = object(args)
  const path = raw ? str(raw.file_path) : ''
  const start = raw ? num(raw.first_changed_line) ?? 1 : 1
  const oldLines = oldText ? oldText.split('\n') : []
  const newLines = newText ? newText.split('\n') : []
  const header = `@@ -${start},${oldLines.length} +${start},${newLines.length} @@`
  const body = [
    ...(oldLines.length ? oldLines.map(line => `-${line}`) : []),
    ...(newLines.length ? newLines.map(line => `+${line}`) : []),
  ]
  return {
    path,
    kind: 'update',
    diff: `--- ${path}\n+++ ${path}\n${header}\n${body.join('\n')}\n`,
  }
}

export function writeDiff(details: WriteDetails, content: string): DiffFile {
  const lines = content ? content.split('\n') : []
  return {
    path: details.path,
    kind: 'add',
    diff: `--- /dev/null\n+++ ${details.path}\n@@ -0,0 +1,${lines.length} @@\n${lines.map(line => `+${line}`).join('\n')}\n`,
  }
}
