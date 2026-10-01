import type { Content, Entry, Message, PromptSnapshot, RequestView } from '../../api/types'

export const CATEGORY_ORDER = ['system', 'tools', 'human', 'agent', 'extension', 'assistant', 'tool', 'compaction', 'remote'] as const
export type ContextCategory = typeof CATEGORY_ORDER[number]
export type SystemSource = 'builtin' | 'configuration' | 'tools' | 'guidelines' | 'append-builtin' | 'append-operator' | 'extension' | 'skills' | 'project' | 'environment' | 'unknown'

export type SystemSection = {
  id: string
  source: SystemSource
  title: string
  text: string
  tokens: number
  attribution: 'structural' | 'unknown'
}

export type ContextItem = {
  id: string
  entryId?: string
  title: string
  text: string
  tokens: number
  /** False for unavailable metadata estimates; numeric zero is otherwise valid. */
  tokensKnown?: boolean
  truncated: boolean
  sourceTags: string[]
  systemSections?: SystemSection[]
  hasNonText?: boolean
}

export type ContextGroup = { id: ContextCategory; items: ContextItem[]; tokens: number }
export type ContextInput = { entries: Entry[]; requests: RequestView[]; leafId?: string; requestId?: string | null }
export type ContextProjection = {
  request?: RequestView
  categories: ContextGroup[]
  totalTokens: number
  approximate: true
  notices: string[]
  missingParentIds: string[]
}
export type ContextTrendPoint = {
  requestId: string
  turn: number
  step: number
  tokens?: number
  categories: Partial<Record<ContextCategory, number>>
  basis: 'usage' | 'meter' | 'estimate' | 'unavailable'
  approximate: true
  partial?: boolean
  /** Missing numeric coverage, independent of whether the bodies are hydrated. */
  estimatesPartial?: boolean
  contextWindow?: number
  checkpoint?: 'remote' | 'local'
}

const encoder = new TextEncoder()

/** Match the harness's cheap UTF-8/4 fallback, not a provider tokenizer. */
export function estimateTokens(text: string): number {
  return Math.ceil(encoder.encode(text).byteLength / 4)
}

type Span = { start: number; end: number; source: SystemSource; title: string }

/** Structural hints only: operator APPEND files have no source delimiters. */
export function systemSections(system: string): SystemSection[] {
  if (!system) return []
  const spans: Span[] = []
  const add = (re: RegExp, source: SystemSource, title: string | ((match: RegExpExecArray) => string)) => {
    for (const match of system.matchAll(re)) {
      spans.push({ start: match.index, end: match.index + match[0].length, source, title: typeof title === 'string' ? title : title(match) })
    }
  }
  add(/^Ki configuration \(KI_HOME:[^\n]*/gm, 'configuration', 'Ki configuration')
  add(/^Available tools:\n[\s\S]*?(?=\nIn addition to the tools above,|$)/gm, 'tools', 'Tool descriptions')
  add(/^In addition to the tools above, you may have access to other custom tools depending on the project\./gm, 'builtin', 'Ki base instructions')
  add(/^Guidelines:\n- Be concise in your responses\n- Show file paths clearly when working with files/gm, 'guidelines', 'Ki guidelines')
  add(/^IMPORTANT: Prefer read, grep, and glob over shell equivalents[^\n]*\n\nIn shell commands or pipelines,[^\n]*/gm, 'append-builtin', 'Built-in APPEND')
  add(/<extension_instructions name="([^"]*)">[\s\S]*?<\/extension_instructions>/g, 'extension', m => `Extension: ${m[1]}`)
  add(/(?:The following skills provide specialized instructions for specific tasks\.[\s\S]*?)?<available_skills>[\s\S]*?<\/available_skills>/g, 'skills', 'Skills catalog')
  add(/<project_instructions path="([^"]*)">[\s\S]*?<\/project_instructions>/g, 'project', m => m[1])
  add(/^Runtime environment:\n[\s\S]*$/gm, 'environment', 'Runtime environment')
  spans.sort((a, b) => a.start - b.start)
  const result: SystemSection[] = []
  let cursor = 0
  let afterAppend = false
  let bytes = 0
  let priced = 0
  const push = (text: string, source: SystemSource, title: string) => {
    if (!text) return
    bytes += encoder.encode(text).byteLength
    const total = Math.ceil(bytes / 4)
    result.push({
      // Price a partition of one system block, not independently rounded duplicate blocks.
      id: `system-section-${result.length}`, source, title, text, tokens: total - priced,
      attribution: source === 'unknown' || source === 'append-operator' ? 'unknown' : 'structural',
    })
    priced = total
  }
  const gap = (text: string) => {
    // Keep wrapper/separator bytes in the partition without presenting them as operator instructions.
    const wrapperOnly = !text.replace(/<\/?project_context>|Project-specific instructions and guidelines:/g, '').trim()
    if (!text.trim() || wrapperOnly) push(text, 'builtin', 'Ki structure')
    else if (cursor === 0 && text.startsWith('You are a helpful assistant operating inside ki,')) push(text, 'builtin', 'Ki base instructions')
    else if (afterAppend) push(text, 'append-operator', 'Operator APPEND (global/project source unavailable)')
    else push(text, 'unknown', 'Unattributed system text')
  }
  for (const span of spans) {
    if (span.start < cursor) continue
    gap(system.slice(cursor, span.start))
    push(system.slice(span.start, span.end), span.source, span.title)
    cursor = span.end
    afterAppend = span.source === 'append-builtin' || span.source === 'extension'
  }
  gap(system.slice(cursor))
  return result
}

type Branch = { entries: Entry[]; missing: string[] }

function branchOf(entries: Entry[], leafId?: string): Branch {
  const byId = new Map(entries.map(e => [e.id, e]))
  const result: Entry[] = []
  const missing: string[] = []
  const seen = new Set<string>()
  let id = leafId ?? entries.at(-1)?.id
  while (id) {
    if (seen.has(id)) { missing.push(id); break }
    seen.add(id)
    const entry = byId.get(id)
    if (!entry) { missing.push(id); break }
    result.push(entry)
    const parent = entry.parentId
    if (parent && !byId.has(parent) && entry.previousId && byId.has(entry.previousId)) {
      // A browser projection bridge permits browsing, but does not prove contiguous history.
      missing.push(parent)
      id = entry.previousId
    } else id = parent
  }
  return { entries: result.reverse(), missing }
}

function categoryOf(message: Message): ContextCategory | undefined {
  if (message.role === 'assistant') return 'assistant'
  if (message.role === 'toolResult' || message.role === 'tool') return 'tool'
  if (message.role === 'user') {
    if (message.origin === 'agent' || message.origin?.startsWith('agent:')) return 'agent'
    if (message.origin?.startsWith('extension:')) return 'extension'
    return 'human'
  }
  return undefined
}

function blockText(block: Content): string {
  if (block.type === 'text') return block.text ?? ''
  if (block.type === 'thinking') return block.thinking ?? ''
  if (block.type === 'toolCall') {
    return `${block.name ?? 'tool'}\n${block.input ?? JSON.stringify(block.arguments ?? {}, null, 2)}`
  }
  // Binary payloads are never stringified or priced as if they were text tokens.
  return `[${block.type}${block.path ? `: ${block.path}` : ''}${block.mimeType ? ` (${block.mimeType})` : ''}]`
}

function messageItem(entry: Entry, calls: Map<string, Content>): ContextItem {
  const message = entry.message!
  const blocks = message.content ?? []
  const sourceTags = message.origin ? [message.origin] : []
  const call = message.toolCallId ? calls.get(message.toolCallId) : undefined
  const path = call?.arguments?.file_path
  if (call?.name?.toLowerCase() === 'read' && typeof path === 'string' && /(?:^|[\\/])SKILL\.md$/i.test(path)) {
    sourceTags.push(`skill:${path}`)
  }
  const text = blocks.map(blockText).join('\n\n')
  const hasNonText = blocks.some(b => !['text', 'thinking', 'toolCall'].includes(b.type))
  const tokens = entryMessageTokens(entry)
  return {
    id: entry.id, entryId: entry.id, title: message.toolName ?? message.origin ?? message.role,
    text, tokens: tokens ?? 0, tokensKnown: tokens !== undefined,
    truncated: !!entry.truncated || entry.bodyKind === 'index' || entry.bodyKind === 'helper' || entry.bodyKind === 'slim',
    sourceTags, hasNonText,
  }
}

function entryMessageTokens(entry: Entry): number | undefined {
  // The server prices full persisted content before it removes bodies. Never
  // price an index preview as that body, or change the estimate on hydration.
  return entry.contextEstimate?.message ?? (partialBody(entry) ? undefined : entry.message ? messageTokens(entry.message) : undefined)
}

function summaryTokens(entry: Entry): number | undefined {
  return entry.contextEstimate?.summary ?? (partialBody(entry) ? undefined : estimateTokens(`Previous conversation summary:\n${entry.summary ?? ''}`))
}

function messageTokens(message: Message): number {
  return (message.content ?? []).reduce((sum, block) => sum + (['text', 'thinking', 'toolCall'].includes(block.type) ? estimateTokens(blockText(block)) : 0), 0)
}

function partialBody(entry: Entry): boolean {
  return !!entry.truncated || ['index', 'helper', 'slim'].includes(entry.bodyKind ?? '')
}

function callsOf(entries: Entry[]): Map<string, Content> {
  const calls = new Map<string, Content>()
  for (const entry of entries) {
    for (const block of entry.message?.content ?? []) {
      if (block.type === 'toolCall' && block.id) calls.set(block.id, block)
    }
  }
  return calls
}

function remoteCompaction(entry: Entry): boolean {
  const details = entry.details as { strategy?: unknown } | undefined
  return entry.type === 'compaction' && ((entry as Entry & { remoteContext?: boolean }).remoteContext === true
    || details?.strategy === 'remote' || !!(entry as Entry & { responses?: unknown }).responses
    || entry.summary === '[remote compaction]' || entry.summary === 'Provider remote compaction')
}

function retainedTail(entry: Entry): Message[] | undefined {
  // Full entry responses can carry retainedTail even though the slim frontend Entry omits it.
  return (entry as Entry & { retainedTail?: Message[] }).retainedTail
}

function contextSurface(entries: Entry[], notices: Set<string>): Entry[] {
  let checkpoint = -1
  for (let i = entries.length - 1; i >= 0; i--) {
    if (entries[i].type === 'compaction') { checkpoint = i; break }
  }
  if (checkpoint < 0) return entries.filter(e => e.message && !e.sideband)
  const comp = entries[checkpoint]
  const suffix = entries.slice(checkpoint + 1).filter(e => e.message && !e.sideband)
  if (remoteCompaction(comp)) {
    notices.add('remote-context')
    // Earlier messages are encapsulated in provider-owned opaque state, not
    // missing plaintext categories. Their effective contents/counts are unknown.
    notices.add('remote-history-encapsulated')
    return [comp, ...suffix]
  }
  // Summary content replaces earlier messages; an empty category does not
  // mean those historical messages never existed in this session.
  notices.add('local-history-summarized')
  const tail = retainedTail(comp)
  if (tail?.length) {
    // Retained messages may differ from their original entries; do not offer a false hydration ID.
    return [comp, ...tail.map((message, i) => ({ type: 'message', id: `retained:${comp.id}:${i}`, message })), ...suffix]
  }
  if (comp.firstKeptEntryId) {
    const start = entries.findIndex(e => e.id === comp.firstKeptEntryId)
    if (start >= 0) {
      notices.add('retained-tail-reconstructed')
      return [comp, ...entries.slice(start, checkpoint).filter(e => e.message && !e.sideband), ...suffix]
    }
    notices.add('retained-tail-unavailable')
  }
  return [comp, ...suffix]
}

function promptAt(entries: Entry[], request?: RequestView): PromptSnapshot | undefined {
  if (request?.prompt && (request.prompt.system || request.prompt.tools.length)) return request.prompt
  let prompt: PromptSnapshot | undefined
  for (const entry of entries) {
    if (entry.type !== 'request_header') continue
    if (!entry.promptUnchanged) {
      // A bodyless header is a new unknown epoch, not evidence that the old prompt survived.
      prompt = partialBody(entry) && !entry.system && !entry.tools?.length ? undefined
        : { system: entry.system ?? '', tools: entry.tools ?? [], provider: entry.provider, model: entry.modelId, thinkingEffort: entry.thinkingEffort }
    }
  }
  return prompt
}

function promptItems(prompt: PromptSnapshot | undefined): { system: ContextItem[]; tools: ContextItem[] } {
  if (!prompt) return { system: [], tools: [] }
  return {
    system: prompt.system ? [{
      id: 'system', title: 'System', text: prompt.system, tokens: estimateTokens(prompt.system), truncated: false,
      sourceTags: [], systemSections: systemSections(prompt.system),
    }] : [],
    tools: prompt.tools.map((tool, i) => {
      const text = JSON.stringify(tool, null, 2)
      return { id: `schema:${i}:${tool.name}`, title: tool.name, text, tokens: estimateTokens(JSON.stringify(tool)), truncated: false, sourceTags: [] }
    }),
  }
}

/** Browse only loaded, branch-bound content; never claim this reproduces provider mutations. */
export function buildContext(input: ContextInput): ContextProjection {
  const request = input.requestId ? input.requests.find(r => r.id === input.requestId) : undefined
  const branch = branchOf(input.entries, input.requestId ?? input.leafId)
  const notices = new Set(['historical-reconstruction'])
  if (branch.missing.length) notices.add('partial-history')
  if (input.requestId && !request) notices.add('request-unavailable')
  const prompt = promptAt(branch.entries, request)
  if (!prompt) notices.add('prompt-unavailable')
  const parts = promptItems(prompt)
  let header: Entry | undefined
  let latestHeader: Entry | undefined
  for (let i = branch.entries.length - 1; i >= 0; i--) {
    if (branch.entries[i].type === 'request_header' && !latestHeader) latestHeader = branch.entries[i]
    if (branch.entries[i].type === 'request_header' && !branch.entries[i].promptUnchanged) { header = branch.entries[i]; break }
  }
  header ??= latestHeader
  const systemEstimate = latestHeader?.contextEstimate?.system ?? header?.contextEstimate?.system
  const toolsEstimate = latestHeader?.contextEstimate?.tools ?? header?.contextEstimate?.tools
  if (header) {
    for (const item of [...parts.system, ...parts.tools]) {
      item.entryId = header.id
      item.truncated = partialBody(header)
      if (item.id === 'system') {
        if (systemEstimate !== undefined) item.tokens = systemEstimate
        else if (partialBody(header)) item.tokens = 0
        item.tokensKnown = systemEstimate !== undefined || !partialBody(header)
        // Partial System text cannot partition the full captured size into
        // sources. Keep the known total, hydrate before source attribution.
        if (partialBody(header)) item.systemSections = undefined
      } else if (partialBody(header)) {
        item.tokensKnown = false
        item.tokens = 0
      }
    }
    if (partialBody(header)) {
      notices.add('partial-content')
      if (!prompt?.system && !prompt?.tools.length) notices.add('prompt-unavailable')
      if (!parts.system.length) parts.system.push({
        id: 'system', entryId: header.id, title: 'System (not loaded)', text: '', tokens: systemEstimate ?? 0,
        tokensKnown: systemEstimate !== undefined, truncated: true, sourceTags: [],
      })
      if (toolsEstimate !== undefined) {
        // A header estimate knows the schema-set total, not each preview's
        // contribution. Keep one aggregate item rather than proportionally
        // assigning its tokens to truncated individual schema bodies.
        parts.tools = toolsEstimate > 0 ? [{
          id: 'schema:metadata', entryId: header.id, title: 'Tool schema aggregate (not loaded)',
          text: prompt?.tools.length ? JSON.stringify(prompt.tools, null, 2) : '',
          tokens: toolsEstimate, tokensKnown: true, truncated: true, sourceTags: [],
        }] : []
      }
    }
    if (!parts.tools.length && toolsEstimate !== undefined && toolsEstimate > 0) parts.tools.push({
      id: 'schema:metadata', entryId: header.id, title: 'Tool schema aggregate (not loaded)', text: '',
      tokens: toolsEstimate, tokensKnown: true, truncated: true, sourceTags: [],
    })
  }
  if (parts.system.some(item => item.systemSections?.some(section => section.source === 'append-operator'))) notices.add('append-source-unknown')
  const groups = CATEGORY_ORDER.map(id => ({ id, items: [] as ContextItem[], tokens: 0 }))
  const byCategory = new Map(groups.map(group => [group.id, group]))
  byCategory.get('system')!.items = parts.system
  byCategory.get('tools')!.items = parts.tools
  const calls = callsOf(branch.entries)
  for (const entry of contextSurface(branch.entries, notices)) {
    if (entry.type === 'compaction') {
      const remote = remoteCompaction(entry)
      const text = remote ? 'Opaque remote context (content unavailable)' : `Previous conversation summary:\n${entry.summary ?? ''}`
      if (partialBody(entry)) notices.add('partial-content')
      if (!remote && !entry.summary) notices.add('compaction-content-unavailable')
      byCategory.get(remote ? 'remote' : 'compaction')!.items.push({
        id: entry.id, entryId: entry.id, title: remote ? 'Remote context' : 'Compaction summary',
        text, tokens: remote ? 0 : summaryTokens(entry) ?? 0, tokensKnown: !remote && summaryTokens(entry) !== undefined,
        truncated: partialBody(entry), sourceTags: ['compaction'],
      })
    } else if (entry.message) {
      const category = categoryOf(entry.message)
      if (!category) continue
      const item = messageItem(entry, calls)
      if (entry.id.startsWith('retained:')) delete item.entryId
      if (item.truncated) notices.add('partial-content')
      if (item.hasNonText) notices.add('non-text-estimate')
      if (entry.message.role === 'assistant' && entry.message.stopReason === 'error') notices.add('failed-assistant-replay-unknown')
      byCategory.get(category)!.items.push(item)
    }
  }
  for (const group of groups) group.tokens = group.items.reduce((sum, item) => sum + item.tokens, 0)
  if (systemEstimate !== undefined) byCategory.get('system')!.tokens = systemEstimate
  if (toolsEstimate !== undefined) byCategory.get('tools')!.tokens = toolsEstimate
  return {
    request, categories: groups, totalTokens: groups.reduce((sum, group) => sum + group.tokens, 0),
    approximate: true, notices: [...notices], missingParentIds: branch.missing,
  }
}

function inputUsage(request: RequestView): number | undefined {
  const usage = request.usage
  if (!usage || ![usage.input, usage.cacheRead, usage.cacheWrite].some(n => n !== undefined)) return undefined
  return (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0)
}

/** One chronological pass over loaded entries; no per-step body fetch or repeated branch reconstruction. */
export function buildContextTrend(input: Omit<ContextInput, 'requestId'>): ContextTrendPoint[] {
  const branch = branchOf(input.entries, input.leafId)
  const requests = new Map(input.requests.map(request => [request.id, request]))
  const points = new Map<string, ContextTrendPoint>()
  const totals: Partial<Record<ContextCategory, number>> = {}
  const messagePrices = new Map<string, { category: ContextCategory; tokens: number; known: boolean }>()
  const systemPrices = new Map<string, number>()
  const toolPrices = new WeakMap<PromptSnapshot['tools'], number>()
  let prompt: PromptSnapshot | undefined
  let activePoint: ContextTrendPoint | undefined
  let checkpoint: 'remote' | 'local' | undefined
  let partial = branch.missing.length > 0
  let estimatesPartial = branch.missing.length > 0
  let systemTokens: number | undefined
  let toolsTokens: number | undefined
  const promptPrice = (value?: PromptSnapshot) => {
    if (!value) return { system: 0, tools: 0 }
    let system = systemPrices.get(value.system)
    if (system === undefined) {
      system = estimateTokens(value.system)
      systemPrices.set(value.system, system)
    }
    let tools = toolPrices.get(value.tools)
    if (tools === undefined) {
      tools = value.tools.reduce((sum, tool) => sum + estimateTokens(JSON.stringify(tool)), 0)
      toolPrices.set(value.tools, tools)
    }
    return { system, tools }
  }
  const addMessage = (entry: Entry) => {
    if (!entry.message || entry.sideband) return
    const category = categoryOf(entry.message)
    if (!category) return
    partial ||= partialBody(entry)
    const tokens = entryMessageTokens(entry)
    estimatesPartial ||= tokens === undefined || entry.message.content?.some(block => !['text', 'thinking', 'toolCall'].includes(block.type)) === true
    messagePrices.set(entry.id, { category, tokens: tokens ?? 0, known: tokens !== undefined })
    if (tokens !== undefined) totals[category] = (totals[category] ?? 0) + tokens
  }
  for (const entry of branch.entries) {
    if (entry.type === 'request_header') {
      if (!entry.promptUnchanged) prompt = partialBody(entry) && !entry.system && !entry.tools?.length ? undefined
        : { system: entry.system ?? '', tools: entry.tools ?? [] }
      const request = requests.get(entry.id)
      const crossedCheckpoint = checkpoint
      checkpoint = undefined
      if (!request) { activePoint = undefined; continue }
      const effectivePrompt = request.prompt ?? prompt
      const incomplete = partial || (partialBody(entry) && !request.prompt) || !effectivePrompt
      const inferredPrompt = promptPrice(effectivePrompt)
      // Every persisted header carries full system/tools estimates, even when
      // its public body is omitted. Legacy explicit unchanged headers may
      // inherit a proven prior estimate; an unknown new header may not.
      systemTokens = entry.contextEstimate?.system ?? (entry.promptUnchanged ? systemTokens
        : partialBody(entry) ? undefined : inferredPrompt.system)
      toolsTokens = entry.contextEstimate?.tools ?? (entry.promptUnchanged ? toolsTokens
        : partialBody(entry) ? undefined : inferredPrompt.tools)
      const categories = { ...totals }
      if (systemTokens !== undefined) categories.system = systemTokens
      if (toolsTokens !== undefined) categories.tools = toolsTokens
      const incompleteEstimates = estimatesPartial || systemTokens === undefined || toolsTokens === undefined
      const usage = inputUsage(request)
      activePoint = {
        requestId: request.id, turn: request.turn, step: request.step, categories, approximate: true,
        tokens: usage ?? (incompleteEstimates ? undefined : Object.values(categories).reduce((sum, n) => sum + (n ?? 0), 0)),
        basis: usage !== undefined ? 'usage' : incompleteEstimates ? 'unavailable' : 'estimate',
        partial: incomplete,
        estimatesPartial: incompleteEstimates,
        ...(crossedCheckpoint ? { checkpoint: crossedCheckpoint } : {}),
      }
      points.set(request.id, activePoint)
    } else if (entry.type === 'context_usage' && activePoint && entry.usedTokens !== undefined) {
      // Only the meter adjacent to a request header is a request-input anchor.
      if (activePoint.basis !== 'usage') {
        activePoint.tokens = entry.usedTokens
        activePoint.basis = 'meter'
      }
      activePoint.contextWindow = entry.contextWindow
      activePoint = undefined
    } else if (entry.type === 'compaction') {
      activePoint = undefined
      for (const key of CATEGORY_ORDER) delete totals[key]
      partial = partialBody(entry)
      estimatesPartial = false
      const keptPrices = new Map<string, { category: ContextCategory; tokens: number; known: boolean }>()
      if (remoteCompaction(entry)) {
        totals.remote = 0
        checkpoint = 'remote'
        // A fully loaded public marker still cannot disclose encrypted input.
        // Usage/meter may measure it, but plaintext estimates remain incomplete.
        partial = true
        estimatesPartial = true
      }
      else {
        checkpoint = 'local'
        const estimate = summaryTokens(entry)
        if (estimate !== undefined) totals.compaction = estimate
        // A checkpoint index lacks its retained-tail contract even when the
        // summary estimate is known. Do not mark the whole context complete.
        estimatesPartial = estimate === undefined || partialBody(entry)
        partial ||= !entry.summary
        const tail = retainedTail(entry)
        if (tail?.length) {
          for (let i = 0; i < tail.length; i++) {
            const id = `retained:${entry.id}:${i}`
            const category = categoryOf(tail[i])
            if (!category) continue
            const tokens = messageTokens(tail[i])
            totals[category] = (totals[category] ?? 0) + tokens
            keptPrices.set(id, { category, tokens, known: true })
            estimatesPartial ||= tail[i].content?.some(block => !['text', 'thinking', 'toolCall'].includes(block.type)) === true
          }
        } else if (entry.firstKeptEntryId && messagePrices.has(entry.firstKeptEntryId)) {
          partial = true
          let keep = false
          for (const [id, price] of messagePrices) {
            keep ||= id === entry.firstKeptEntryId
            if (keep) {
              totals[price.category] = (totals[price.category] ?? 0) + price.tokens
              keptPrices.set(id, price)
              estimatesPartial ||= !price.known
            }
          }
        } else if (entry.firstKeptEntryId) {
          partial = true
          estimatesPartial = true
        }
      }
      messagePrices.clear()
      for (const [id, price] of keptPrices) messagePrices.set(id, price)
    } else if (entry.message) {
      activePoint = undefined
      addMessage(entry)
    }
  }
  // Requests can exist as metadata without their header/message bodies. Usage remains useful.
  return input.requests.map(request => points.get(request.id) ?? {
    requestId: request.id, turn: request.turn, step: request.step, categories: {},
    tokens: inputUsage(request), basis: inputUsage(request) !== undefined ? 'usage' : 'unavailable', approximate: true,
    partial: true,
    estimatesPartial: true,
  })
}
