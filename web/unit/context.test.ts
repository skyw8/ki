import { describe, expect, test } from 'bun:test'
import type { Entry, Message, RequestView } from '../src/api/types'
import { buildContext, buildContextTrend, CATEGORY_ORDER, estimateTokens, systemSections } from '../src/features/context/model'

const request = (id: string, options: Partial<RequestView> = {}): RequestView => ({ id, turn: 1, step: 1, status: 'complete', ...options })
const message = (id: string, parentId: string | undefined, role: string, text: string, origin?: string): Entry => ({
  id, parentId, type: 'message', message: { role, content: [{ type: 'text', text }], origin },
})
const group = (projection: ReturnType<typeof buildContext>, id: typeof CATEGORY_ORDER[number]) => projection.categories.find(g => g.id === id)!

describe('full-content estimates on bodyless metadata', () => {
  const metadata = (entry: Entry): Entry => ({ ...entry, bodyKind: 'index', truncated: true })
  test('categorizes historical System, schemas, human, agent, assistant and tool without downloading bodies', () => {
    const entries: Entry[] = [
      metadata({ ...message('u', undefined, 'user', 'tiny preview'), contextEstimate: { message: 50 } }),
      metadata({ ...message('agent', 'u', 'user', 'tiny preview', 'agent:work'), contextEstimate: { message: 80 } }),
      metadata({ ...message('a', 'agent', 'assistant', 'tiny preview'), contextEstimate: { message: 120 } }),
      metadata({ ...message('tool', 'a', 'toolResult', 'tiny preview'), contextEstimate: { message: 600 } }),
      metadata({ id: 'r', type: 'request_header', parentId: 'tool', contextEstimate: { system: 1000, tools: 2000 } }),
    ]
    const projection = buildContext({ entries, requests: [request('r')], requestId: 'r' })
    expect(projection.categories.filter(g => g.tokens > 0).map(g => [g.id, g.tokens])).toEqual([
      ['system', 1000], ['tools', 2000], ['human', 50], ['agent', 80], ['assistant', 120], ['tool', 600],
    ])
    expect(group(projection, 'agent').items[0]).toMatchObject({ text: 'tiny preview', tokens: 80, tokensKnown: true, truncated: true })
    expect(group(projection, 'system').items[0]).toMatchObject({ text: '', tokens: 1000, tokensKnown: true, truncated: true, entryId: 'r' })
    expect(group(projection, 'tools').items[0]).toMatchObject({ id: 'schema:metadata', tokens: 2000, tokensKnown: true, entryId: 'r' })
    const trend = buildContextTrend({ entries, requests: [request('r')] })[0]
    expect(trend.categories).toEqual({ system: 1000, tools: 2000, human: 50, agent: 80, assistant: 120, tool: 600 })
    expect(trend).toMatchObject({ tokens: 3850, basis: 'estimate', partial: true, estimatesPartial: false, approximate: true })
  })

  test('known zeros stay known while legacy missing estimates remain unavailable', () => {
    const entries: Entry[] = [
      metadata({ ...message('agent', undefined, 'user', 'not the body', 'agent:zero'), contextEstimate: { message: 0 } }),
      metadata({ id: 'r', type: 'request_header', parentId: 'agent', contextEstimate: { system: 0, tools: 0 } }),
    ]
    const projection = buildContext({ entries, requests: [], leafId: 'r' })
    expect(group(projection, 'agent').items[0]).toMatchObject({ tokens: 0, tokensKnown: true })
    expect(buildContextTrend({ entries, requests: [request('r')] })[0]).toMatchObject({ tokens: 0, basis: 'estimate', estimatesPartial: false })
    const legacy = entries.map(entry => ({ ...entry, contextEstimate: undefined }))
    expect(buildContext({ entries: legacy, requests: [], leafId: 'r' }).categories.find(g => g.id === 'agent')?.items[0]).toMatchObject({ tokens: 0, tokensKnown: false })
    expect(buildContextTrend({ entries: legacy, requests: [request('r')] })[0]).toMatchObject({ tokens: undefined, basis: 'unavailable', estimatesPartial: true })
  })

  test('hydration never changes captured estimates and remote state never receives a summary price', () => {
    const slim = metadata({ ...message('agent', undefined, 'user', 'preview', 'agent:work'), contextEstimate: { message: 777 } })
    const full = { ...message('agent', undefined, 'user', 'full text but different formatter', 'agent:work'), contextEstimate: { message: 777 } }
    expect(group(buildContext({ entries: [slim], requests: [] }), 'agent').tokens).toBe(777)
    expect(group(buildContext({ entries: [full], requests: [] }), 'agent').tokens).toBe(777)
    const checkpoint = metadata({ type: 'compaction', id: 'c', parentId: 'agent', summary: 'preview', contextEstimate: { summary: 150 } })
    expect(group(buildContext({ entries: [slim, checkpoint], requests: [] }), 'compaction').tokens).toBe(150)
    const remote = { ...checkpoint, remoteContext: true }
    const remoteContext = buildContext({ entries: [slim, remote], requests: [] })
    expect(group(remoteContext, 'remote').items[0]).toMatchObject({ tokens: 0, tokensKnown: false })
    expect(group(remoteContext, 'compaction').tokens).toBe(0)
    const trend = buildContextTrend({ entries: [slim, remote, { id: 'r', type: 'request_header', parentId: 'c', contextEstimate: { system: 1, tools: 0 }, truncated: true }], requests: [request('r', { usage: { input: 12345 } })] })[0]
    expect(trend).toMatchObject({ tokens: 12345, basis: 'usage', estimatesPartial: true, checkpoint: 'remote' })
    expect(trend.categories).toEqual({ remote: 0, system: 1, tools: 0 })
  })
})

describe('context reconstruction', () => {
  test('separates human, agent, extension and tool content without a generic injection category', () => {
    const entries = [
      message('u', undefined, 'user', 'question'),
      message('agent', 'u', 'user', 'done', 'agent:research'),
      message('ext', 'agent', 'user', 'extra', 'extension:notes'),
      message('a', 'ext', 'assistant', 'answer'),
      message('t', 'a', 'toolResult', 'output'),
    ]
    const context = buildContext({ entries, requests: [], leafId: 't' })
    expect(group(context, 'human').items.map(i => i.id)).toEqual(['u'])
    expect(group(context, 'agent').items.map(i => i.id)).toEqual(['agent'])
    expect(group(context, 'extension').items.map(i => i.id)).toEqual(['ext'])
    expect(group(context, 'assistant').items.map(i => i.id)).toEqual(['a'])
    expect(group(context, 'tool').items.map(i => i.id)).toEqual(['t'])
    expect(CATEGORY_ORDER).not.toContain('inject' as never)
    expect(context.approximate).toBe(true)
    expect(context.notices).toContain('historical-reconstruction')
  })

  test('selects the request parent branch and excludes its response and sibling branches', () => {
    const entries: Entry[] = [
      message('u', undefined, 'user', 'question'),
      { id: 'r', parentId: 'u', type: 'request_header', system: 'system', tools: [] },
      message('a', 'r', 'assistant', 'answer'),
      message('sibling', 'u', 'user', 'other branch'),
    ]
    const context = buildContext({ entries, requests: [request('r')], requestId: 'r' })
    expect(group(context, 'human').items.map(i => i.id)).toEqual(['u'])
    expect(group(context, 'assistant').items).toHaveLength(0)
    expect(group(context, 'system').items[0].text).toBe('system')
  })

  test('records sparse branch bridges and truncated body coverage', () => {
    const entries: Entry[] = [
      message('u', undefined, 'user', 'question'),
      { ...message('a', 'omitted', 'assistant', 'preview'), previousId: 'u', truncated: true },
    ]
    const context = buildContext({ entries, requests: [], leafId: 'a' })
    expect(context.missingParentIds).toEqual(['omitted'])
    expect(context.notices).toContain('partial-history')
    expect(context.notices).toContain('partial-content')
    expect(group(context, 'human').items).toHaveLength(1)
  })

  test('local compaction replaces old messages, retaining only the declared tail and suffix', () => {
    const entries: Entry[] = [
      message('old', undefined, 'user', 'discarded'),
      message('kept', 'old', 'assistant', 'retained'),
      { id: 'c', parentId: 'kept', type: 'compaction', summary: 'summary', firstKeptEntryId: 'kept' },
      message('next', 'c', 'user', 'next'),
    ]
    const context = buildContext({ entries, requests: [], leafId: 'next' })
    expect(group(context, 'human').items.map(i => i.id)).toEqual(['next'])
    expect(group(context, 'assistant').items.map(i => i.id)).toEqual(['kept'])
    expect(group(context, 'compaction').items[0].text).toContain('summary')
    expect(context.notices).toContain('retained-tail-reconstructed')
  })

  test('uses verbatim retainedTail when available instead of original pre-compaction entries', () => {
    const tail: Message[] = [{ role: 'assistant', content: [{ type: 'text', text: 'rewritten retained content' }] }]
    const comp: Entry & { retainedTail: Message[] } = { id: 'c', parentId: 'old', type: 'compaction', summary: 'summary', retainedTail: tail }
    const context = buildContext({ entries: [message('old', undefined, 'assistant', 'original'), comp], requests: [], leafId: 'c' })
    expect(group(context, 'assistant').items[0].text).toBe('rewritten retained content')
    expect(group(context, 'assistant').items[0].entryId).toBeUndefined()
  })

  test('opaque remote context never leaks payload or gets priced as plaintext', () => {
    const comp: Entry & { responses: unknown } = { type: 'compaction', id: 'c', responses: { encrypted: 'SECRET' } }
    const context = buildContext({ entries: [comp], requests: [], leafId: 'c' })
    expect(group(context, 'remote').tokens).toBe(0)
    expect(group(context, 'remote').items[0].text).not.toContain('SECRET')
    expect(context.notices).toContain('remote-context')
    const redacted = buildContext({ entries: [{ type: 'compaction', id: 'c', summary: 'Provider remote compaction', bodyKind: 'index' }], requests: [] })
    expect(group(redacted, 'remote').items).toHaveLength(1)
    const marked: Entry & { remoteContext: boolean } = { type: 'compaction', id: 'remote', remoteContext: true }
    const markerContext = buildContext({ entries: [marked], requests: [] })
    expect(group(markerContext, 'remote').items).toHaveLength(1)
    expect(group(markerContext, 'compaction').items).toHaveLength(0)
  })

  test('remote checkpoints encapsulate earlier agents without erasing historical metadata or later agents', () => {
    const entries: Entry[] = [
      { ...message('agent-before', undefined, 'user', 'notification preview', 'agent:task-before'), bodyKind: 'index', truncated: true },
      { id: 'before', parentId: 'agent-before', type: 'request_header', system: 'System', tools: [] },
      { id: 'remote', parentId: 'before', type: 'compaction', remoteContext: true, tokensBefore: 257696 },
      { id: 'after', parentId: 'remote', type: 'request_header', promptUnchanged: true },
      message('agent-after', 'after', 'user', 'later notification', 'agent:task-after'),
    ]
    const requests = [request('before'), request('after', { step: 2 })]
    const historical = buildContext({ entries, requests, requestId: 'before' })
    expect(group(historical, 'agent').items).toHaveLength(1)
    expect(group(historical, 'agent').items[0]).toMatchObject({
      entryId: 'agent-before', truncated: true, tokens: 0, sourceTags: ['agent:task-before'],
    })
    expect(historical.notices).toContain('partial-content')
    expect(historical.notices).not.toContain('remote-history-encapsulated')
    expect(group(historical, 'remote').items).toHaveLength(0)

    const afterCheckpoint = buildContext({ entries, requests, requestId: 'after' })
    expect(group(afterCheckpoint, 'agent').items).toHaveLength(0)
    expect(group(afterCheckpoint, 'compaction').items).toHaveLength(0)
    expect(group(afterCheckpoint, 'remote').items[0]).toMatchObject({ entryId: 'remote', tokens: 0 })
    expect(afterCheckpoint.notices).toContain('remote-history-encapsulated')
    expect(afterCheckpoint.missingParentIds).toEqual([])
    const current = buildContext({ entries, requests, leafId: 'agent-after' })
    expect(group(current, 'agent').items.map(item => item.id)).toEqual(['agent-after'])
    expect(current.notices).toContain('remote-history-encapsulated')

    const trend = buildContextTrend({ entries, requests, leafId: 'agent-after' })
    expect(trend[0].categories.agent).toBeUndefined() // Legacy metadata proves identity, not a zero-sized body.
    expect(trend[0].estimatesPartial).toBe(true)
    expect(trend[0].partial).toBe(true)
    expect(trend[1].categories.agent).toBeUndefined()
    expect(trend[1].categories.remote).toBe(0)
    expect(trend[1].categories.compaction).toBeUndefined()
    expect(trend[1].partial).toBe(true)
    expect(trend[1].tokens).toBeUndefined()
    expect(trend[1].checkpoint).toBe('remote')
  })

  test('local summary hydration restores its body without restoring agents outside the retained tail', () => {
    const checkpoint: Entry = {
      id: 'local', parentId: 'before', type: 'compaction',
      summary: 'summary preview', truncated: true, bodyKind: 'slim',
    }
    const entries = [
      message('agent-before', undefined, 'user', 'old notification', 'agent:task-before'),
      { id: 'before', parentId: 'agent-before', type: 'request_header', system: 'System', tools: [] } as Entry,
      checkpoint,
      message('agent-after', 'local', 'user', 'new notification', 'agent:task-after'),
    ]
    const historical = buildContext({ entries, requests: [request('before')], requestId: 'before' })
    expect(group(historical, 'agent').items.map(item => item.id)).toEqual(['agent-before'])
    expect(group(historical, 'compaction').items).toHaveLength(0)
    const slim = buildContext({ entries, requests: [], leafId: 'agent-after' })
    expect(group(slim, 'compaction').items[0]).toMatchObject({ entryId: 'local', truncated: true })
    expect(group(slim, 'agent').items.map(item => item.id)).toEqual(['agent-after'])
    expect(slim.notices).toContain('local-history-summarized')
    const summary = 'full summary '.repeat(5000)
    const hydrated = buildContext({
      entries: entries.map(entry => entry.id === 'local' ? { ...checkpoint, summary, truncated: false, bodyKind: 'full' } : entry),
      requests: [], leafId: 'agent-after',
    })
    expect(group(hydrated, 'compaction').items[0]).toMatchObject({
      entryId: 'local', truncated: false, text: `Previous conversation summary:\n${summary}`,
    })
    expect(group(hydrated, 'agent').items.map(item => item.id)).toEqual(['agent-after'])
    expect(hydrated.notices).not.toContain('remote-history-encapsulated')
  })

  test('tags skill reads but keeps their actual content in tool results', () => {
    const call: Entry = { id: 'call', type: 'message', message: { role: 'assistant', content: [{ type: 'toolCall', id: 'tc', name: 'read', arguments: { file_path: 'C:\\skills\\review\\SKILL.md' } }] } }
    const result: Entry = { id: 'result', type: 'message', parentId: 'call', message: { role: 'toolResult', toolCallId: 'tc', toolName: 'read', content: [{ type: 'text', text: 'skill instructions' }] } }
    const context = buildContext({ entries: [call, result], requests: [], leafId: 'result' })
    expect(group(context, 'tool').items[0].sourceTags).toEqual(['skill:C:\\skills\\review\\SKILL.md'])
    expect(group(context, 'system').items).toHaveLength(0)
  })

  test('does not count base64 images as text or tool diagnostics as model content', () => {
    const entry: Entry = { id: 'u', type: 'message', message: { role: 'user', details: { secret: 'not model visible' }, content: [{ type: 'image', data: 'x'.repeat(10000) }] } }
    const context = buildContext({ entries: [entry], requests: [] })
    expect(group(context, 'human').tokens).toBe(0)
    expect(group(context, 'human').items[0].text).toBe('[image]')
    expect(context.notices).toContain('non-text-estimate')
  })

  test('reuses persisted promptUnchanged and request snapshots without present-day configuration', () => {
    const entries: Entry[] = [
      { id: 'r1', type: 'request_header', system: 'historical system', tools: [{ name: 'read' }] },
      { id: 'r2', parentId: 'r1', type: 'request_header', promptUnchanged: true },
    ]
    const context = buildContext({ entries, requests: [request('r2')], requestId: 'r2' })
    expect(group(context, 'system').items[0].text).toBe('historical system')
    expect(group(context, 'tools').items[0].title).toBe('read')
  })

  test('unknown metadata header invalidates an earlier prompt and provides actionable hydration', () => {
    const entries: Entry[] = [
      { id: 'old', type: 'request_header', system: 'old system', tools: [{ name: 'old-tool' }] },
      { id: 'new', parentId: 'old', type: 'request_header', bodyKind: 'index', truncated: true },
    ]
    const context = buildContext({ entries, requests: [request('new')], requestId: 'new' })
    expect(context.notices).toContain('prompt-unavailable')
    expect(group(context, 'system').items[0]).toMatchObject({ entryId: 'new', truncated: true, text: '', tokens: 0 })
    expect(group(context, 'tools').items).toHaveLength(0)
    const point = buildContextTrend({ entries, requests: [request('new')] })[0]
    expect(point.tokens).toBeUndefined()
    expect(point.categories.system).toBeUndefined()
    expect(point.estimatesPartial).toBe(true)
  })
})

describe('system source partition', () => {
  test('recognizes Ki sources while keeping operator global/project attribution unknown', () => {
    const system = [
      'You are a helpful assistant operating inside ki, a agent harness.',
      'Ki configuration (KI_HOME: /home/test/.ki): configuration.',
      'Available tools:\n- read: Read a file\n\nIn addition to the tools above, you may have access to other custom tools depending on the project.',
      'Guidelines:\n- Be concise in your responses\n- Show file paths clearly when working with files',
      "IMPORTANT: Prefer read, grep, and glob over shell equivalents (cat, head, sed, awk, echo).\n\nIn shell commands or pipelines, prefer 'rg' and 'fd'. 'fd' respects .gitignore and skips hidden files.",
      'global operator content\n\nproject operator content',
      '<extension_instructions name="notes">\nextension rules\n</extension_instructions>',
      'The following skills provide specialized instructions for specific tasks.\nUse the read tool.\n\n<available_skills>\n<skill><name>review</name></skill>\n</available_skills>',
      '<project_context>\n\nProject-specific instructions and guidelines:\n\n<project_instructions path="/work/AGENTS.md">\nproject rules\n</project_instructions>\n\n</project_context>',
      'Runtime environment:\n- OS: Linux\n- Architecture: amd64\n\nCurrent working directory: /work\nCurrent date: 2026-10-01\nTimezone: UTC',
    ].join('\n\n')
    const sections = systemSections(system)
    expect(sections.map(s => s.text).join('')).toBe(system)
    expect(sections.reduce((sum, section) => sum + section.tokens, 0)).toBe(estimateTokens(system))
    expect(sections.find(s => s.source === 'project')?.title).toBe('/work/AGENTS.md')
    expect(sections.find(s => s.source === 'append-operator')?.attribution).toBe('unknown')
    expect(sections.find(s => s.source === 'append-operator')?.text).toContain('global operator content')
    expect(sections.find(s => s.source === 'skills')?.text).toContain('<available_skills>')
    expect(sections.find(s => s.source === 'extension')?.title).toBe('Extension: notes')
    expect(sections.find(s => s.source === 'environment')?.text).toContain('Timezone')
    expect(sections.map(s => s.source)).not.toContain('append-global' as never)
  })

  test('foreign prompt does not invent project or APPEND sources', () => {
    const sections = systemSections('Follow these unknown instructions.')
    expect(sections[0].source).toBe('unknown')
    expect(sections[0].attribution).toBe('unknown')
    expect(estimateTokens('你好')).toBe(2)
  })
})

describe('lightweight context trend', () => {
  test('marks only the first request crossing local and remote checkpoints while keeping usage authoritative', () => {
    const entries: Entry[] = [
      message('old', undefined, 'user', 'old history'),
      { id: 'local', parentId: 'old', type: 'compaction', summary: 'summary' },
      { id: 'r1', parentId: 'local', type: 'request_header', system: 'System', tools: [] },
      { id: 'remote', parentId: 'r1', type: 'compaction', remoteContext: true },
      { id: 'r2', parentId: 'remote', type: 'request_header', promptUnchanged: true },
      { id: 'r3', parentId: 'r2', type: 'request_header', promptUnchanged: true },
    ]
    const points = buildContextTrend({
      entries, requests: [request('r1'), request('r2', { step: 2, usage: { input: 100, cacheRead: 40 } }), request('r3', { step: 3 })],
    })
    expect(points.map(point => point.checkpoint)).toEqual(['local', 'remote', undefined])
    expect(points[0].partial).toBe(false)
    expect(points[0].basis).toBe('estimate')
    expect(points[1].partial).toBe(true)
    expect(points[1].tokens).toBe(140)
    expect(points[1].basis).toBe('usage')
    expect(points[2].partial).toBe(true)
    expect(points[2].tokens).toBeUndefined()
    expect(points[2].basis).toBe('unavailable')
  })

  test('uses actual input including cache, never cumulative output as context size', () => {
    const points = buildContextTrend({ entries: [], requests: [request('r', { usage: { input: 10, cacheRead: 20, cacheWrite: 3, output: 900, totalTokens: 933 } })] })
    expect(points[0].tokens).toBe(33)
    expect(points[0].basis).toBe('usage')
    expect(points[0].categories).toEqual({})
  })

  test('estimates only loaded pre-response content and anchors adjacent request meters', () => {
    const entries: Entry[] = [
      message('u', undefined, 'user', '12345678'),
      { id: 'r1', parentId: 'u', type: 'request_header', system: '1234', tools: [] },
      { id: 'meter', parentId: 'r1', type: 'context_usage', usedTokens: 100 },
      message('a', 'meter', 'assistant', '12345678'),
      { id: 'after', parentId: 'a', type: 'context_usage', usedTokens: 999 },
      { id: 'r2', parentId: 'after', type: 'request_header', promptUnchanged: true },
    ]
    const points = buildContextTrend({ entries, requests: [request('r1'), request('r2', { step: 2 }), request('not-loaded')] })
    expect(points[0].tokens).toBe(100)
    expect(points[0].basis).toBe('meter')
    expect(points[0].categories.assistant).toBeUndefined()
    expect(points[1].tokens).toBe(5)
    expect(points[1].categories.assistant).toBe(2)
    expect(points[2].tokens).toBeUndefined()
    expect(points[2].basis).toBe('unavailable')
  })

  test('handles many steps without repeated per-request reconstruction', () => {
    const entries: Entry[] = []
    const requests: RequestView[] = []
    let parentId: string | undefined
    for (let i = 0; i < 1000; i++) {
      const user = message(`u${i}`, parentId, 'user', '1234')
      const header: Entry = { id: `r${i}`, parentId: user.id, type: 'request_header', promptUnchanged: i > 0, system: i === 0 ? 'system' : undefined }
      entries.push(user, header)
      requests.push(request(header.id, { turn: i + 1 }))
      parentId = header.id
    }
    const points = buildContextTrend({ entries, requests, leafId: parentId })
    expect(points).toHaveLength(1000)
    expect(points.at(-1)?.categories.human).toBe(1000)
  })

  test('bodyless preview is not a priced message or a complete request estimate', () => {
    const entries: Entry[] = [
      { ...message('u', undefined, 'user', 'tiny preview'), bodyKind: 'index', truncated: true },
      { id: 'r', parentId: 'u', type: 'request_header', system: 'known system', tools: [] },
    ]
    const context = buildContext({ entries, requests: [request('r')], requestId: 'r' })
    expect(group(context, 'human').tokens).toBe(0)
    expect(group(context, 'human').items[0].truncated).toBe(true)
    const point = buildContextTrend({ entries, requests: [request('r')] })[0]
    expect(point.partial).toBe(true)
    expect(point.tokens).toBeUndefined()
    expect(point.basis).toBe('unavailable')
    const billed = buildContextTrend({ entries, requests: [request('r', { usage: { input: 100 } })] })[0]
    expect(billed.tokens).toBe(100)
    expect(billed.partial).toBe(true)
    expect(billed.basis).toBe('usage')
  })

  test('preserves retained original tail across successive compaction estimates', () => {
    const entries: Entry[] = [
      message('old', undefined, 'user', 'old'),
      message('kept', 'old', 'assistant', '12345678'),
      { id: 'c1', parentId: 'kept', type: 'compaction', summary: 'first', firstKeptEntryId: 'kept' },
      { id: 'r1', parentId: 'c1', type: 'request_header', system: 'system' },
      { id: 'c2', parentId: 'r1', type: 'compaction', summary: 'second', firstKeptEntryId: 'kept' },
      { id: 'r2', parentId: 'c2', type: 'request_header', promptUnchanged: true },
    ]
    const points = buildContextTrend({ entries, requests: [request('r1'), request('r2')] })
    expect(points[0].categories.assistant).toBe(2)
    expect(points[1].categories.assistant).toBe(2)
    expect(points[1].partial).toBe(true)
  })
})
