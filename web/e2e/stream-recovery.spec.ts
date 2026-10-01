import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'
import type { LoopEvent } from '../src/api/types'

test('evicted replay recovers the persisted terminal answer without submitting another prompt', async ({ page }) => {
  const f = await openStream(page, 0)
  let prompts = 0
  let snapshots = 0
  page.on('request', req => { if (req.url().endsWith('/prompt')) prompts++ })
  await page.route(`**/v1/sessions/${f.id}`, route => route.fulfill({ json: {
    id: f.id, title: f.title, running: ++snapshots === 1, leafId: 'durable',
    entries: [...f.entries, { type: 'message', id: 'durable', parentId: 'question',
      message: { role: 'assistant', content: [{ type: 'text', text: 'Recovered from durable history 中🙂' }] } }],
  } }))
  // The fixture supplies a fetch-backed stream. Override its next connection
  // with the server's actual HTTP contract, after one still-running snapshot.
  await page.evaluate(id => {
    const previous = window.fetch
    const scope = window as unknown as { streamClose: () => void; expiredReplays: number }
    scope.expiredReplays = 0
    window.fetch = async (...args) => {
      if (String(args[0]).includes(`/v1/sessions/${id}/events`)) {
        scope.expiredReplays++
        return new Response(JSON.stringify({ error: 'replay_unavailable' }), { status: 410 })
      }
      return previous(...args)
    }
    scope.streamClose()
  }, f.id)
  await expect(page.getByTestId('assistant-message')).toContainText('Recovered from durable history 中🙂')
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  expect(prompts).toBe(0)
  expect(snapshots).toBe(2)
  expect(await page.evaluate(() => (window as unknown as { expiredReplays: number }).expiredReplays)).toBe(1)
})

test('late reference definitions resolve across settled segments and completion keeps the mounted root', async ({ page }) => {
  const f = await openStream(page, 0)
  const source = '[reference][target]\n\n' + 'A settled paragraph with **formatting**.\n\n'.repeat(150)
  const send = (ev: LoopEvent) => page.evaluate(ev => (window as unknown as { streamSend: (ev: LoopEvent) => void }).streamSend(ev), ev)
  const message = (text: string) => ({ role: 'assistant', content: [{ type: 'text', text }] })
  await send({ type: 'message_update', runId: 'reference', seq: 1, messageStream: 1, message: message(source) })
  await expect(page.getByTestId('assistant-message').locator('.md-seg')).not.toHaveCount(0)
  const final = source + '[target]: https://example.com/reference\n'
  await send({ type: 'message_update', runId: 'reference', seq: 2, messageStream: 1, message: message(final) })
  await expect(page.getByTestId('assistant-message')).toContainText('[target]:')
  await page.evaluate(() => { (window as unknown as { oldRoot: Element | null }).oldRoot = document.querySelector('[data-stream-seq]') })
  await page.route(`**/v1/sessions/${f.id}`, route => route.fulfill({ json: {
    id: f.id, title: f.title, running: false, leafId: 'final',
    entries: [...f.entries, { type: 'message', id: 'final', parentId: 'question', message: message(final) }],
  } }))
  await send({ type: 'message_end', runId: 'reference', seq: 3, entryId: 'final', message: message(final) })
  await send({ type: 'agent_end', runId: 'reference', seq: 4 })
  await page.evaluate(() => (window as unknown as { streamClose: () => void }).streamClose())
  const link = page.getByTestId('assistant-message').getByRole('link', { name: 'reference', exact: true })
  await expect(link).toHaveAttribute('href', 'https://example.com/reference')
  await expect(page.getByTestId('assistant-message').locator('strong')).toHaveCount(150)
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { oldRoot: Element }).oldRoot.isConnected)).toBe(true)
})

test('failed reconciliation retains the partial and reconnects from its applied cursor without resending a prompt', async ({ page }) => {
  const f = await openStream(page, 0)
  let prompts = 0
  page.on('request', req => { if (req.url().endsWith('/prompt')) prompts++ })
  await page.evaluate(() => (window as unknown as { streamSend: (ev: unknown) => void }).streamSend({
    type: 'message_update', runId: 'recovery', seq: 7, messageStream: 7,
    message: { role: 'assistant', content: [{ type: 'text', text: 'Already visible 中🙂' }] },
  }))
  await expect(page.getByTestId('assistant-message')).toContainText('Already visible 中🙂')
  await page.route(`**/v1/sessions/${f.id}`, route => route.abort('failed'))
  const count = () => page.evaluate(() => (window as unknown as { streamURLs: string[] }).streamURLs.length)
  const before = await count()
  await page.evaluate(() => (window as unknown as { streamFail: () => void }).streamFail())
  await expect.poll(count).toBe(before + 1)
  const cursor = await page.evaluate(() => (window as unknown as { streamCursors: (string | null)[] }).streamCursors.at(-1))
  expect(cursor).toBe('recovery:7')
  await expect(page.getByTestId('assistant-message')).toContainText('Already visible 中🙂')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await page.evaluate(() => (window as unknown as { streamSend: (ev: unknown) => void }).streamSend({
    type: 'message_update', runId: 'recovery', seq: 10, messageStream: 10,
    message: { role: 'assistant', content: [{ type: 'text', text: 'Already visible 中🙂 recovered' }] },
  }))
  await expect(page.getByTestId('assistant-message')).toHaveCount(1)
  await expect(page.getByTestId('assistant-message')).toContainText('recovered')
  expect(prompts).toBe(0)
})
