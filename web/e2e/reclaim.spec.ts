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
  push({ type: 'message', id: 'u0', message: { role: 'user', content: [{ type: 'text', text: 'Long running turn' }] } })
  for (let n = 0; n < 40; n++) {
    const call = `call-${n}`
    push({ type: 'message', id: `a-${n}`, message: { role: 'assistant', content: [{ type: 'text', text: `step ${n}` }, { type: 'toolCall', id: call, name: 'Bash', arguments: { command: `cmd ${n}` } }] } })
    push({ type: 'message', id: `r-${n}`, message: { role: 'toolResult', toolCallId: call, toolName: 'Bash', content: [{ type: 'text', text: `out ${n}` }] } })
  }
  appendFileSync(join(dir, 'events.jsonl'), entries.map(e => JSON.stringify(e)).join('\n') + '\n')
  config.activeLeafId = parent
  config.title = title
  writeFileSync(path, JSON.stringify(config))
  return { id, title }
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
  await send({ type: 'message_end', runId: 'live', seq: 3, entryId: 'a-live', message: { role: 'assistant', timestamp: now + 1000, latencyMs: 100, ttftMs: 20, usage: { input: 5, output: 5 }, content: [{ type: 'text', text: 'newest reply' }] } })
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
  await expect(page.getByTestId('turn-divider').getByTestId('turn-tools')).toContainText('0/40')
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
