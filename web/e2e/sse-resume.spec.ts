import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'

// Mobile tabs are suspended while backgrounded and the OS drops the socket, but
// a resume arrives long before the read-idle timeout (45s) elapses. The guard
// therefore has a separate resume rule: a page that was hidden and read nothing
// since must reconnect the push stream as soon as it is visible again, so the
// open session catches up without a manual refresh. This drives that resume
// signal directly (the real browser fires it on focus; a test must dispatch it
// after overriding the visibility getters).
test.describe.configure({ mode: 'parallel' })

test('a resumed tab reconnects the push stream before the read-idle timeout', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __kiHidden: boolean }
    w.__kiHidden = false
    Object.defineProperty(Document.prototype, 'visibilityState', { configurable: true, get: () => (w.__kiHidden ? 'hidden' : 'visible') })
    Object.defineProperty(Document.prototype, 'hidden', { configurable: true, get: () => w.__kiHidden })
  })
  const events: number[] = []
  page.on('request', req => { if (req.url().endsWith('/v1/events')) events.push(Date.now()) })
  await page.goto('/')
  await expect.poll(() => events.length).toBeGreaterThanOrEqual(1)
  const before = events.length

  // Hide for seconds, far under the 45s idle timeout: the stream must stay open
  // (no reconnect while hidden), because a frozen page cannot read a new one.
  await page.evaluate(() => {
    const w = window as unknown as { __kiHidden: boolean }
    w.__kiHidden = true
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await page.waitForTimeout(2500)
  expect(events.length).toBe(before)

  // Resume: reconnect immediately.
  await page.evaluate(() => {
    const w = window as unknown as { __kiHidden: boolean }
    w.__kiHidden = false
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await expect.poll(() => events.length, { timeout: 5000 }).toBeGreaterThan(before)
})

for (const scenario of ['visibilitychange', 'pageshow', 'online', 'first-turn']) test(`resume via ${scenario} reconciles the completed transcript even with a live socket`, async ({ page }) => {
  const f = await openStream(page, 0, scenario === 'first-turn')
  const signal = scenario === 'first-turn' ? 'visibilitychange' : scenario
  await page.evaluate(() => (window as unknown as { streamSend: (ev: unknown) => void }).streamSend({
    type: 'message_update', runId: 'resume', seq: 1, messageStream: 1,
    message: { role: 'assistant', content: [{ type: 'text', text: 'Partial before sleep' }] },
  }))
  await expect(page.getByTestId('assistant-message')).toContainText('Partial before sleep')
  let reconciled = 0
  await page.route(`**/v1/sessions/${f.id}`, route => {
    reconciled++
    return route.fulfill({ json: {
      id: f.id, title: f.title, running: false, leafId: 'done', entries: [...f.entries,
        { type: 'message', id: 'done', parentId: 'question', message: { role: 'assistant', content: [{ type: 'text', text: 'Completed while away' }] } },
      ],
    } })
  })
  // No idle deadline, socket error or missing heartbeat: lifecycle recovery
  // must reconcile data even if a proxy kept the connection apparently alive.
  await page.evaluate(signal => {
    if (signal === 'visibilitychange') document.dispatchEvent(new Event(signal))
    else if (signal === 'pageshow') window.dispatchEvent(new PageTransitionEvent(signal, { persisted: true }))
    else window.dispatchEvent(new Event(signal))
  }, signal)
  await expect.poll(() => reconciled).toBeGreaterThan(0)
  await expect(page.getByTestId('assistant-message')).toHaveCount(1)
  await expect(page.getByTestId('assistant-message')).toHaveText('Completed while away')
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
})

test('pagehide closes subscriptions until pageshow and a running resume keeps its cursor', async ({ page }) => {
  const f = await openStream(page, 0)
  await page.evaluate(() => (window as unknown as { streamSend: (ev: unknown) => void }).streamSend({
    type: 'message_update', runId: 'still-running', seq: 7, messageStream: 7,
    message: { role: 'assistant', content: [{ type: 'text', text: 'Before hiding' }] },
  }))
  await expect(page.getByTestId('assistant-message')).toContainText('Before hiding')
  let pushConnections = 0
  let prompts = 0
  page.on('request', req => {
    if (req.url().endsWith('/v1/events')) pushConnections++
    if (req.url().endsWith('/prompt')) prompts++
  })
  const count = () => page.evaluate(() => (window as unknown as { streamURLs: string[] }).streamURLs.length)
  const before = await count()
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
  await page.waitForTimeout(800)
  expect(pushConnections).toBe(0)
  expect(await count()).toBe(before)
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
  await expect.poll(count).toBeGreaterThan(before)
  await expect.poll(() => pushConnections).toBeGreaterThan(0)
  expect(await page.evaluate(() => (window as unknown as { streamCursors: string[] }).streamCursors.at(-1))).toBe('still-running:7')
  await page.evaluate(() => (window as unknown as { streamSend: (ev: unknown) => void }).streamSend({
    type: 'message_update', runId: 'still-running', seq: 10, messageStream: 10,
    message: { role: 'assistant', content: [{ type: 'text', text: 'Live after recovery' }] },
  }))
  await expect(page.getByTestId('assistant-message')).toHaveCount(1)
  await expect(page.getByTestId('assistant-message')).toContainText('Live after recovery')
  expect(prompts).toBe(0)
  expect(f.id).toBeTruthy()
})
