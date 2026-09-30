import { expect, test, type Page } from '@playwright/test'
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken } from './global-setup'
import type { Entry } from '../src/api/types'

// A long absence loses the run stream. On resume the transcript is
// authoritative, but the browser still holds the nodes it drew before the
// disconnect: a tool whose end event was lost (or suppressed as
// already-persisted) would otherwise keep a spinner forever, hold its turn
// unfolded, freeze its counts, and add a second divider for the same turn.
test.describe.configure({ mode: 'parallel' })

async function seed(page: Page, title: string) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id, dir } = await created.json() as { id: string; dir: string }
  const path = join(dir, 'config.json')
  const config = JSON.parse(readFileSync(path, 'utf8'))
  const entries: Entry[] = []
  let parent = ''
  const push = (e: Omit<Entry, 'parentId'>) => { entries.push({ ...e, parentId: parent }); parent = e.id }
  const startedAt = Date.now() - 120_000
  push({ type: 'message', id: 'u0', message: { role: 'user', timestamp: startedAt, content: [{ type: 'text', text: 'Long running turn' }] } })
  for (let n = 0; n < 40; n++) {
    const call = `call-${n}`
    push({ type: 'message', id: `a-${n}`, message: { role: 'assistant', timestamp: startedAt + (n + 1) * 1000, content: [{ type: 'text', text: `step ${n}` }, { type: 'toolCall', id: call, name: 'Bash', arguments: { command: `cmd ${n}` } }] } })
    push({ type: 'message', id: `r-${n}`, message: { role: 'toolResult', toolCallId: call, toolName: 'Bash', timestamp: startedAt + (n + 1) * 1000 + 500, durationMs: 500, content: [{ type: 'text', text: `out ${n}` }] } })
  }
  appendFileSync(join(dir, 'events.jsonl'), entries.map(e => JSON.stringify(e)).join('\n') + '\n')
  config.activeLeafId = parent
  config.title = title
  writeFileSync(path, JSON.stringify(config))
  return { id, title, dir, parent }
}

/** Open the seeded compact session with a controllable run stream. Every plain
 * snapshot reports the session as running so the reclaim path stays live. */
async function open(page: Page, id: string, title: string, keep: string) {
  await page.addInitScript(({ id, keep }) => {
    localStorage.setItem('ki-message-view', 'compact')
    localStorage.setItem('ki-message-view-keep', keep)
    const scope = window as unknown as { streamSend: (v: unknown) => void }
    const original = window.fetch
    window.fetch = async (...args) => {
      if (String(args[0]).includes(`/v1/sessions/${id}/events`)) {
        const stream = new ReadableStream({ start(c) { scope.streamSend = v => c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(v)}\n\n`)) } })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return original(...args)
    }
  }, { id, keep })
  await page.route(url => url.pathname === `/v1/sessions/${id}` && !url.searchParams.has('turn') && !url.searchParams.has('before'), async route => {
    const response = await route.fetch()
    await route.fulfill({ response, json: { ...await response.json(), running: true } })
  })
  await page.goto('/')
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await expect(page.getByTestId('turn-divider').first()).toBeVisible()
  return (ev: unknown) => page.evaluate(ev => (window as unknown as { streamSend: (v: unknown) => void }).streamSend(ev), ev)
}

test('a stale running tool folds away, stays settled after a reclaim, and never doubles the divider', async ({ page }) => {
  test.setTimeout(60_000)
  const f = await seed(page, `reclaim-a-${Date.now()}`)
  const send = await open(page, f.id, f.title, '1')
  const now = Date.now()
  await send({ type: 'agent_start', runId: 'live', seq: 1 })
  // The tool's end is lost across the reconnect, then a newer reply lands.
  await send({ type: 'tool_execution_start', runId: 'live', seq: 2, toolCallId: 'tc-live', toolName: 'Bash', args: { command: 'stale' }, timestamp: now })
  const message = { role: 'assistant', timestamp: now + 1000, latencyMs: 100, ttftMs: 20, usage: { input: 5, output: 5 }, content: [{ type: 'text', text: 'newest reply' }] }
  // The real server persists both results before publishing the next model
  // reply. Only the browser lost the tool end; the recovery snapshot must be
  // newer than the initial snapshot, not an artificial rollback of the file.
  appendFileSync(join(f.dir, 'events.jsonl'), [
    { type: 'message', id: 'r-live', parentId: f.parent, message: { role: 'toolResult', toolCallId: 'tc-live', toolName: 'Bash', timestamp: now + 900, durationMs: 900, content: [{ type: 'text', text: 'done' }] } },
    { type: 'message', id: 'a-live', parentId: 'r-live', message },
  ].map(e => JSON.stringify(e)).join('\n') + '\n')
  const configPath = join(f.dir, 'config.json')
  const config = JSON.parse(readFileSync(configPath, 'utf8'))
  writeFileSync(configPath, JSON.stringify({ ...config, activeLeafId: 'a-live' }))
  await send({ type: 'message_end', runId: 'live', seq: 3, entryId: 'a-live', message })
  await page.waitForTimeout(400)

  // One divider, the stale tool folded (never a live/partial count), keep honored.
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-divider')).not.toHaveAttribute('data-live')
  await expect(page.getByTestId('fold-row').locator('.fold-row-count')).toHaveText('已折叠 81 条消息')
  await expect(page.getByTestId('tool-card')).toHaveCount(0)

  // Reclaim: the authoritative snapshot drops the stale live node, so nothing
  // is left spinning and the turn stays settled.
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await page.waitForTimeout(600)
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-divider')).not.toHaveAttribute('data-live')
  await expect(page.locator('[data-testid="tool-card"][data-state="running"]')).toHaveCount(0)
  // The count is the whole turn's, never the browser's partial view (the stale
  // tool must not be added twice either).
  await expect(page.getByTestId('turn-divider').getByTestId('turn-tools')).toContainText('0/41')
})

test('a turn whose newest reply arrived after the snapshot keeps a single divider', async ({ page }) => {
  test.setTimeout(60_000)
  const f = await seed(page, `reclaim-b-${Date.now()}`)
  const send = await open(page, f.id, f.title, '2')
  const now = Date.now()
  await send({ type: 'agent_start', runId: 'live', seq: 1 })
  await send({ type: 'message_end', runId: 'live', seq: 2, entryId: 'a-live', message: { role: 'assistant', timestamp: now + 1000, latencyMs: 100, ttftMs: 20, usage: { input: 5, output: 5 }, content: [{ type: 'text', text: 'newest reply' }] } })
  await page.waitForTimeout(400)
  // The client's newest node and the snapshot's last-visible node are both on
  // screen; the turn must still carry exactly one divider.
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
})


test('parallel tools remain live and compact counts do not jump on expansion or completion', async ({ page }) => {
  const f = await seed(page, `parallel-count-${Date.now()}`)
  const send = await open(page, f.id, f.title, '1')
  const now = Date.now()
  await send({ type: 'agent_start', runId: 'parallel', seq: 1 })
  await send({ type: 'message_end', runId: 'parallel', seq: 2, entryId: 'a-parallel', message: { role: 'assistant', timestamp: now, content: [
    { type: 'toolCall', id: 'slow', name: 'Read', arguments: {} },
    { type: 'toolCall', id: 'fast', name: 'Read', arguments: {} },
  ] } })
  await send({ type: 'tool_execution_start', runId: 'parallel', seq: 3, toolCallId: 'slow', toolName: 'Read', timestamp: now })
  await send({ type: 'tool_execution_start', runId: 'parallel', seq: 4, toolCallId: 'fast', toolName: 'Read', timestamp: now })
  await send({ type: 'tool_execution_end', runId: 'parallel', seq: 5, toolCallId: 'fast', toolName: 'Read', durationMs: 10, result: 'fast result' })
  await expect(page.getByTestId('turn-divider')).toHaveAttribute('data-live', 'true')
  await expect(page.locator('[data-testid="tool-card"][data-state="running"]')).toHaveCount(1)
  await expect(page.getByTestId('turn-tools')).toContainText('0/42')
  await page.getByTestId('fold-row-btn').click()
  // Hydration of hidden replies must not reset the pending sibling or count
  // the snapshot prefix a second time.
  await expect(page.getByTestId('turn-tools')).toContainText('0/42')
  await expect(page.getByTestId('turn-divider')).toHaveAttribute('data-live', 'true')
  await send({ type: 'tool_execution_end', runId: 'parallel', seq: 6, toolCallId: 'slow', toolName: 'Read', durationMs: 1000, isError: true, result: 'slow failed' })
  await expect(page.getByTestId('turn-tools')).toContainText('1/42')
  await expect(page.getByTestId('turn-divider')).not.toHaveAttribute('data-live')
  await expect(page.locator('[data-testid="tool-card"][data-state="running"]')).toHaveCount(0)
})


test('confirmed prompt keeps cumulative live elapsed aligned with its settled value', async ({ page }) => {
  const f = await seed(page, `clock-${Date.now()}`)
  const send = await open(page, f.id, f.title, '1')
  // Stop the injected initial run so its old wall span does not keep ticking.
  await send({ type: 'agent_end', runId: 'previous', seq: 1 })
  const seconds = (text: string) => {
    const match = text.match(/(?:(\d+)m)?([\d.]+)s/)
    return match ? Number(match[1] ?? 0) * 60 + Number(match[2]) : NaN
  }
  await expect(page.getByTestId('session-elapsed')).toHaveClass(/settled/)
  const previous = seconds(await page.getByTestId('session-elapsed').innerText())
  expect(previous).toBeGreaterThan(39)
  const startedAt = Date.now()
  const user = { role: 'user', timestamp: startedAt, content: [{ type: 'text', text: 'Second timed prompt' }] }
  await send({ type: 'agent_start', runId: 'clock', seq: 1 })
  await send({ type: 'message_start', runId: 'clock', seq: 2, message: user })
  await send({ type: 'message_end', runId: 'clock', seq: 3, entryId: 'u-clock', message: user })
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'Second timed prompt' })).toBeVisible()
  const live = seconds(await page.getByTestId('session-elapsed').innerText())
  expect(live).toBeGreaterThanOrEqual(previous)
  // Reusing the previous user start adds over two minutes here.
  expect(live - previous).toBeLessThan(5)
  await send({ type: 'message_end', runId: 'clock', seq: 4, entryId: 'a-clock', message: { role: 'assistant', timestamp: startedAt + 1000, latencyMs: 1000, content: [{ type: 'text', text: 'Timed answer' }] } })
  await send({ type: 'agent_end', runId: 'clock', seq: 5 })
  await expect(page.getByTestId('session-elapsed')).toHaveClass(/settled/)
  expect(seconds(await page.getByTestId('session-elapsed').innerText())).toBeCloseTo(previous + 1, 0)
})


test('keep zero retains one accurate divider through streaming, settlement and fold toggles', async ({ page, browserName }) => {
  const f = await seed(page, `zero-count-${Date.now()}`)
  const send = await open(page, f.id, f.title, '0')
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-tools')).toContainText('0/40')
  await expect(page.getByTestId('session-stats')).toContainText('1 轮 · 40 步')
  await send({ type: 'message_start', runId: 'zero', seq: 1, message: { role: 'assistant', timestamp: Date.now() } })
  await expect(page.getByTestId('turn-divider')).toHaveAttribute('data-live', 'true')
  await send({ type: 'message_end', runId: 'zero', seq: 2, entryId: 'a-zero', message: { role: 'assistant', timestamp: Date.now(), content: [{ type: 'text', text: 'new zero reply' }] } })
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-divider')).not.toHaveAttribute('data-live')
  await expect(page.getByTestId('turn-divider')).toContainText('41 步')
  await expect(page.getByTestId('session-stats')).toContainText('1 轮 · 41 步')
  await expect(page.getByTestId('turn-tools')).toContainText('0/40')
  await page.getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-tools')).toContainText('0/40')
  // Expanding downloads the forty old steps; the composer must not add that
  // already-counted snapshot prefix again on top of the one live completion.
  await expect(page.getByTestId('session-stats')).toContainText('1 轮 · 41 步')
  // Expanding the newest turn intentionally keeps its tail pinned. The fold
  // is now outside the virtual window; scroll to it as a reader would.
  const tailOffset = await page.getByTestId('chat-scroll').evaluate(el => el.scrollTop)
  await page.getByTestId('chat-scroll').hover()
  await page.mouse.wheel(0, -100_000)
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  await expect.poll(() => page.getByTestId('chat-scroll').evaluate(el => el.scrollTop)).toBeLessThan(tailOffset - 1)
  // Firefox caps even a -100000px native wheel to one page (456px in the
  // matching plain overflow control). Continue with bounded native gestures,
  // never a scrollTop assignment, and require progress from every gesture.
  if (browserName === 'firefox') {
    for (let i = 0; i < 24 && !await page.getByTestId('fold-row-btn').isVisible(); i++) {
      const before = await page.getByTestId('chat-scroll').evaluate(el => el.scrollTop)
      await page.mouse.wheel(0, -400)
      await expect.poll(() => page.getByTestId('chat-scroll').evaluate(el => el.scrollTop)).toBeLessThan(before - 1)
    }
  }
  await expect(page.getByTestId('fold-row-btn')).toBeVisible()
  await page.getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await expect(page.getByTestId('turn-divider')).toContainText('41 步')
  await expect(page.getByTestId('session-stats')).toContainText('1 轮 · 41 步')
})
