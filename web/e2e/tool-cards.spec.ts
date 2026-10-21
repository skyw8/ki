import { expect, test, type Page } from '@playwright/test'
import { openSessionRow } from './sidebar.ts'
import type { Entry } from '../src/api/types'
import { serverToken } from './global-setup'
import { openStream } from './stream-fixture'

// Each case owns a private server session and browser context.
test.describe.configure({ mode: 'parallel' })

const execDetails = {
  revision: 5, run_id: 'run', tool_call_id: 'call-exec', session_id: 42,
  cmd: 'go test ./internal/...', workdir: '/repo', tty: false, status: 'exited', pid: 99,
  output: 'ok  ki/internal/session\t0.42s\n--- FAIL: TestReplay (0.31s)\nFAIL\n',
  exit_code: 1, total_bytes: 61, output_file: '/tmp/ki-out.log',
  output_offset: 0, started_at: '2026-10-21T10:00:00Z', finished_at: '2026-10-21T10:00:02Z',
}

function entries(): Entry[] {
  const stamp = 1_800_000_000_000
  return [
    { type: 'message', id: 'u', parentId: undefined, message: { role: 'user', timestamp: stamp, content: [{ type: 'text', text: 'run the suite' }] } },
    {
      type: 'message', id: 'call', parentId: 'u',
      message: {
        role: 'assistant', timestamp: stamp + 10, content: [{
          type: 'toolCall', id: 'call-exec', name: 'exec_command',
          arguments: { cmd: 'go test ./internal/...', description: 'Run the suite' },
        }],
      },
    },
    {
      type: 'message', id: 'result', parentId: 'call',
      message: {
        role: 'toolResult', toolName: 'exec_command', toolCallId: 'call-exec', timestamp: stamp + 2000, durationMs: 2000, isError: true,
        // The snapshot text is what older sessions stored; the card must read
        // details, never this raw JSON.
        content: [{ type: 'text', text: JSON.stringify(execDetails) }],
        details: execDetails,
      },
    },
  ]
}

async function openTranscript(page: Page) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `tool-cards-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  const list = entries()
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries: list, running: false, leafId: 'result', oldestId: 'u', hasMore: false } })
    : route.fallback())
  // The full-output action opens the spill file through the existing preview
  // endpoint; serve it locally so the assertion does not depend on /tmp state.
  await page.route(url => url.pathname === '/v1/fs' && url.searchParams.get('preview') === '1', route =>
    route.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', body: 'complete output line\n' }))
  // Assertions below are written in English; the default language is zh.
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  await page.goto('/')
  await openSessionRow(page, title)
  await expect(page.getByTestId('chat')).toBeVisible()
}

test('a shell card renders the snapshot instead of the result JSON', async ({ page }) => {
  await openTranscript(page)
  const card = page.getByTestId('tool-card').filter({ hasText: 'exec_command' })
  await expect(card).toHaveAttribute('data-state', 'error')
  // Collapsed row carries the exit code, never a JSON blob.
  await expect(card.getByTestId('tool-exit')).toHaveText('1')
  await expect(card.getByTestId('tool-preview')).not.toContainText('"revision"')

  await card.locator('.tool-row-toggle').click()
  await expect(card.getByTestId('terminal-cmd')).toHaveText('go test ./internal/...')
  await expect(card.getByTestId('terminal-exit')).toHaveText('Exit 1')
  const output = card.getByTestId('terminal-output')
  // Columns survive: the tab-separated test line keeps its exact spacing.
  await expect(output).toContainText('ok  ki/internal/session\t0.42s')
  expect(await output.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('pre')

  await card.getByTestId('terminal-full-output').click()
  const preview = page.getByTestId('output-preview-body')
  await expect(preview).toHaveText('complete output line\n')
})

test('terminal output stays inside the transcript on a phone', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openTranscript(page)
  const card = page.getByTestId('tool-card').filter({ hasText: 'exec_command' })
  await card.locator('.tool-row-toggle').click()
  const width = await page.getByTestId('terminal-output').evaluate(el => ({
    scroll: el.clientWidth,
    page: document.documentElement.scrollWidth,
    viewport: innerWidth,
  }))
  expect(width.page).toBeLessThanOrEqual(width.viewport)
  // A narrow viewport wraps rather than growing a horizontal scroll.
  expect(await page.getByTestId('terminal-output').evaluate(el => getComputedStyle(el).whiteSpace)).toBe('pre-wrap')
})

test('a running shell grows its output from live process snapshots', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  await openStream(page, 0)
  const now = Date.now()
  const send = (event: unknown) => page.evaluate(event => (window as unknown as { streamSend: (value: unknown) => void }).streamSend(event), event)
  await send({ type: 'message_end', runId: 'live', seq: 1, entryId: 'ask', parentId: 'question', message: { role: 'user', timestamp: now, content: [{ type: 'text', text: 'watch the build' }] } })
  await send({ type: 'message_end', runId: 'live', seq: 2, entryId: 'call-live', parentId: 'ask', message: { role: 'assistant', timestamp: now, content: [{ type: 'toolCall', id: 'tc-live', name: 'exec_command', arguments: { cmd: 'make build' } }] } })
  await send({ type: 'tool_execution_start', runId: 'live', seq: 3, toolCallId: 'tc-live', toolName: 'exec_command', timestamp: now, args: { cmd: 'make build' } })
  const process = (revision: number, output: string, status: string) => send({
    type: 'process_updated', runId: 'live', seq: 4 + revision,
    process: { session_id: 5, revision, tool_call_id: 'tc-live', cmd: 'make build', workdir: '/repo', tty: false, status, output, total_bytes: output.length, started_at: new Date(now).toISOString() },
  })

  const card = page.getByTestId('tool-card').filter({ hasText: 'exec_command' })
  await card.locator('.tool-row-toggle').click()
  await process(1, 'compiling\n', 'running')
  await expect(card.getByTestId('terminal-output')).toHaveText('compiling\n')
  await expect(card.getByTestId('terminal-exit')).toHaveText('Running')
  // Snapshots are cumulative: the second one replaces, never appends.
  await process(2, 'compiling\ndone\n', 'exited')
  await expect(card.getByTestId('terminal-output')).toHaveText('compiling\ndone\n')
})

function editEntries(): Entry[] {
  const stamp = 1_800_000_000_000
  const diff = [
    '--- /repo/internal/session/session.go',
    '+++ /repo/internal/session/session.go',
    '@@ -101,4 +101,6 @@',
    ' \tPinned bool',
    ' \tPinnedAt string',
    '+\tCacheKey string',
    '+\t// inherited from the fork source',
    '-\tRemoved bool',
    ' }\n',
  ].join('\n')
  return [
    { type: 'message', id: 'u', parentId: undefined, message: { role: 'user', timestamp: stamp, content: [{ type: 'text', text: 'add the cache key' }] } },
    {
      type: 'message', id: 'call', parentId: 'u',
      message: { role: 'assistant', timestamp: stamp + 10, content: [{ type: 'toolCall', id: 'tc-edit', name: 'edit', arguments: { file_path: '/repo/internal/session/session.go', old_string: '\tRemoved bool', new_string: '\tCacheKey string' } }] },
    },
    {
      type: 'message', id: 'result', parentId: 'call',
      message: {
        role: 'toolResult', toolName: 'edit', toolCallId: 'tc-edit', timestamp: stamp + 900, durationMs: 900,
        content: [{ type: 'text', text: 'Successfully replaced 1 block(s) in internal/session/session.go.' }],
        details: { diff, patch: diff, first_changed_line: 101 },
      },
    },
  ]
}

test('an edit renders a coloured unified diff with line numbers', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await created.json() as { id: string }
  const title = `diff-card-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  const list = editEntries()
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries: list, running: false, leafId: 'result', oldestId: 'u', hasMore: false } })
    : route.fallback())
  await page.goto('/')
  await openSessionRow(page, title)

  const card = page.getByTestId('tool-card').filter({ hasText: 'edit' })
  // The collapsed preview names the file and its net change, not a diff blob.
  await expect(card.getByTestId('tool-preview')).toContainText('internal/session/session.go')
  await card.locator('.tool-row-toggle').click()

  const file = card.getByTestId('diff-file')
  await expect(file).toHaveAttribute('data-path', '/repo/internal/session/session.go')
  await expect(file.getByTestId('diff-kind')).toHaveText('modified')
  // Insert and delete rows are distinguishable by row class and by colour.
  const rows = file.locator('.diff-code')
  await expect(rows.filter({ hasText: 'CacheKey string' })).toHaveClass(/diff-code-insert/)
  await expect(rows.filter({ hasText: 'Removed bool' })).toHaveClass(/diff-code-delete/)
  const colours = await file.evaluate(el => {
    const pick = (selector: string) => {
      const node = [...el.querySelectorAll<HTMLElement>(selector)][0]
      return node ? getComputedStyle(node).backgroundColor : ''
    }
    return { insert: pick('.diff-code-insert'), remove: pick('.diff-code-delete') }
  })
  expect(colours.insert).not.toBe(colours.remove)
  // Line numbers come from the hunk header, not a running counter: unified
  // view prints the new number once per inserted line, starting at 103.
  await expect(file.locator('.diff-gutter-insert').filter({ hasText: /^103$/ })).toHaveCount(1)
})

function call(id: string, parent: string, name: string, args: unknown): Entry {
  return { type: 'message', id, parentId: parent, message: { role: 'assistant', content: [{ type: 'toolCall', id, name, arguments: args }] } }
}

function result(id: string, parent: string, name: string, text: string, details: unknown): Entry {
  return {
    type: 'message', id: `${id}-r`, parentId: parent,
    message: { role: 'toolResult', toolName: name, toolCallId: id, timestamp: 1_800_000_001_000, durationMs: 20, content: [{ type: 'text', text }], details },
  }
}

test('search, read and coordination rows render their structured details', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await created.json() as { id: string }
  const title = `structured-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })

  const grepText = 'docs/provider.md:12: cache key\ndocs/session.md:7: fork key\n\n[files: 2; matches: 2; truncated: false]'
  const globText = 'Found 2 files\ndocs/a.md\ndocs/b.md'
  const cellText = 'Script completed{"content":[{"type":"text","text":"cell one"}],"isError":false}{"content":[{"type":"text","text":"cell two"}],"isError":true}'
  const entries: Entry[] = [
    { type: 'message', id: 'u', parentId: undefined, message: { role: 'user', timestamp: 1_800_000_000_000, content: [{ type: 'text', text: 'inspect everything' }] } },
    call('tc-grep', 'u', 'grep', { pattern: 'key' }),
    result('tc-grep', 'tc-grep', 'grep', grepText, {
      mode: 'content', files: 2, matches: 2, truncated: false,
      hits: [
        { path: 'docs/provider.md', line: 12, text: 'cache key' },
        { path: 'docs/session.md', line: 7, text: 'fork key' },
      ],
    }),
    call('tc-glob', 'tc-grep-r', 'glob', { pattern: '**/*.md' }),
    result('tc-glob', 'tc-glob', 'glob', globText, {
      root: '/repo', files: 2, limit: 100, truncated: false, paths: ['docs/a.md', 'docs/b.md'],
    }),
    call('tc-read', 'tc-glob-r', 'read', { file_path: 'a.go' }),
    result('tc-read', 'tc-read', 'read', 'package main\n\nfunc main() {}\n', {
      truncation: { truncated: true, reason: 'limit', total_bytes: 900, total_lines: 900, output_bytes: 20, output_lines: 3, next_offset: 4 },
    }),
    call('tc-agent', 'tc-read-r', 'spawn_agent', { task_name: '/root/audit' }),
    result('tc-agent', 'tc-agent', 'spawn_agent', '{"agent_id":"a-1","task_name":"/root/audit"}', {
      agent_id: 'a-1', task_name: '/root/audit', session_id: '01a0fd07f1927154b7bf6b12827921f4',
    }),
    call('tc-cell', 'tc-agent-r', 'exec', {}),
    result('tc-cell', 'tc-cell', 'exec', cellText, { cell_id: 'cell-1', status: 'completed', truncated: false }),
  ]
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries, running: false, leafId: 'tc-cell-r', oldestId: 'u', hasMore: false } })
    : route.fallback())
  await page.goto('/')
  await openSessionRow(page, title)

  const cardOf = (tool: string) => page.locator(`[data-testid="tool-card"][data-tool="${tool}"]`)
  const grepCard = cardOf('grep')
  await grepCard.locator('.tool-row-toggle').click()
  await expect(grepCard.getByTestId('grep-hit')).toHaveCount(2)
  await expect(grepCard.getByTestId('grep-hit').first()).toContainText('docs/provider.md')
  // The model-facing text is not the renderer's source: no bracket metadata.
  await expect(grepCard).not.toContainText('[files: 2; matches: 2')

  const globCard = cardOf('glob')
  await globCard.locator('.tool-row-toggle').click()
  await expect(globCard.getByTestId('glob-paths').locator('.hit')).toHaveCount(2)

  const readCard = cardOf('read')
  await readCard.locator('.tool-row-toggle').click()
  await expect(readCard.getByTestId('read-meta')).toContainText('of 900')
  await expect(readCard.getByTestId('read-meta')).toContainText('Continue from line 4')

  const agentCard = cardOf('spawn_agent')
  await agentCard.locator('.tool-row-toggle').click()
  await expect(agentCard.getByTestId('agent-receipt')).toContainText('/root/audit')

  const cellCard = cardOf('exec')
  await cellCard.locator('.tool-row-toggle').click()
  // Two envelopes become two blocks; the failed one is marked.
  await expect(cellCard.getByTestId('cell-call')).toHaveCount(2)
  await expect(cellCard.getByTestId('cell-call').nth(1)).toHaveClass(/err/)
  await expect(cellCard.getByTestId('cell-call').nth(0)).toContainText('cell one')
})
