import { expect, test, type Page } from '@playwright/test'
import { expandWorkspaces } from './sidebar.ts'
import { randomUUID } from 'node:crypto'
import { openStream } from './stream-fixture'
import { focusedSessionReader } from './session.ts'

test.describe.configure({ mode: 'parallel' })

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test('offscreen stream blocks keep their source, format on visibility and finish without losing markers', async ({ page }) => {
  await openStream(page, 0)
  const text = Array.from({ length: 180 }, (_, i) => `Paragraph ${i} **formatted-marker** ${'body '.repeat(35)}\n\n`).join('')
  const send = (event: unknown) => page.evaluate(event => (window as unknown as { streamSend: (event: unknown) => void }).streamSend(event), event)
  await send({ type: 'message_update', runId: 'budget', seq: 1, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text: text.slice(0, text.indexOf('\n\n') + 2) }] } })
  const root = page.locator('[data-stream-seq]')
  await expect(root).toContainText('Paragraph 0')
  await page.evaluate(() => { (window as unknown as { promotedPiece: Element | null }).promotedPiece = document.querySelector('[data-md-block]') })
  await send({ type: 'message_update', runId: 'budget', seq: 2, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text }] } })
  await expect(root).toContainText('Paragraph 179')
  expect(await page.evaluate(() => (window as unknown as { promotedPiece: Element }).promotedPiece === document.querySelector('[data-md-block]'))).toBe(true)
  await page.evaluate(() => { (window as unknown as { markdownRoot: Element | null }).markdownRoot = document.querySelector('[data-stream-seq]') })
  const first = root.locator('[data-md-block]').first()
  await page.waitForTimeout(500)
  await expect(first).toHaveAttribute('data-md-pending', '1')
  await expect(first.locator('[data-md-tail]')).toContainText('Paragraph 0 **formatted-marker**')
  expect((await root.textContent())?.match(/Paragraph \d+/g)?.length).toBe(180)
  await page.getByTestId('chat-scroll').dispatchEvent('wheel', { deltaY: -1 })
  await page.getByTestId('chat-scroll').evaluate(el => { el.scrollTop = 0 })
  await expect(first.locator('strong').first()).toHaveText('formatted-marker')
  await send({ type: 'message_end', runId: 'budget', seq: 3, entryId: 'final', message: { role: 'assistant', content: [{ type: 'text', text }] } })
  await expect(root.locator('[data-md-pending]')).toHaveCount(0)
  await expect(root.locator('strong')).toHaveCount(180)
  expect((await root.textContent())?.match(/Paragraph \d+/g)?.length).toBe(180)
  expect(await page.evaluate(() => (window as unknown as { markdownRoot: Element }).markdownRoot === document.querySelector('[data-stream-seq]'))).toBe(true)
})

test('keeps the text moving while it streams', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, 'e2e-blocks-300')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  const lengths = await page.evaluate(async () => {
    const lengths = new Set<number>()
    for (let i = 0; i < 60; i++) {
      lengths.add(document.querySelector('[data-testid="assistant-message"]')?.textContent?.length ?? -1)
      await new Promise(resolve => setTimeout(resolve, 100))
    }
    return [...lengths]
  })
  const growing = lengths.filter(n => n > 0).sort((a, b) => a - b)
  expect(growing.length, `rendered lengths: ${growing.join(',')}`).toBeGreaterThan(4)
  expect(growing.at(-1), 'final length').toBeGreaterThan(30_000)
})

test('following the tail stays pinned while the newest message grows', async ({ page }) => {
  test.setTimeout(60_000)
  await openStream(page, 0)
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  const { gaps, heights, failures } = await page.evaluate(async () => {
    const send = (window as unknown as { streamSend: (event: unknown) => void }).streamSend
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const gaps: number[] = []
    const heights: number[] = []
    const failures: unknown[] = []
    const deadline = Date.now() + 15_000
    const frame = () => new Promise<{ height: number; overflowing: boolean }>(resolve => requestAnimationFrame(() => {
      const height = el.scrollHeight
      const gap = Math.round(Math.max(0, height - el.clientHeight - el.scrollTop))
      gaps.push(gap)
      if (gap > 8) failures.push({
        gap, height, viewport: el.clientHeight, offset: el.scrollTop,
        seq: el.querySelector<HTMLElement>('[data-stream-seq]')?.dataset.streamSeq,
        source: !!el.querySelector('[data-md-tail]'), running: !!el.querySelector('.status-line'),
        rows: [...el.querySelectorAll<HTMLElement>('[data-index]')].map(row => ({ id: row.dataset.itemKey, height: row.getBoundingClientRect().height, start: row.style.transform })),
      })
      resolve({ height, overflowing: height > el.clientHeight })
    }))
    const acknowledge = async (ready: () => boolean) => {
      do {
        await frame()
        if (Date.now() >= deadline) throw new Error('stream checkpoint did not render within 15s')
      } while (!ready())
      return frame()
    }
    let text = ''
    for (let checkpoint = 1; checkpoint <= 10; checkpoint++) {
      for (let i = (checkpoint - 1) * 20; i < checkpoint * 20; i++) {
        text += `- item ${i} ${'x'.repeat(110)}\n`
      }
      send({ type: 'message_update', runId: 'follow', seq: checkpoint, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text }] } })
      const geometry = await acknowledge(() => (el.querySelector('[data-stream-seq]')?.textContent ?? '').includes(`item ${checkpoint * 20 - 1} `))
      if (geometry.overflowing) heights.push(geometry.height)
    }
    send({ type: 'message_end', runId: 'follow', seq: 11, entryId: 'final', message: { role: 'assistant', content: [{ type: 'text', text }] } })
    await acknowledge(() => el.querySelectorAll('[data-stream-seq] li').length === 200)
    return { gaps, heights, failures }
  })
  expect(gaps.length, 'sampled the follow throughout growth').toBeGreaterThan(5)
  expect(new Set(heights).size, `distinct overflowing checkpoint heights: ${heights.join(',')}`).toBeGreaterThan(5)
  expect(Math.max(...gaps), `distance from the tail while growing: ${gaps.join(',')}; failures: ${JSON.stringify(failures)}`).toBeLessThanOrEqual(8)
})

test('stopping generation retains already visible text and its mounted root', async ({ page }) => {
  await page.goto('/')
  const prompt = `e2e-blocks-400 stop-${randomUUID()}`
  await sendPrompt(page, prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('Paragraph 20')
  await page.evaluate(() => { (window as unknown as { stoppedRoot: Element | null }).stoppedRoot = document.querySelector('[data-stream-seq]') })
  await page.getByTestId('composer-stop').click()
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect(page.getByTestId('assistant-message')).toContainText('Paragraph 20')
  await expect(page.getByTestId('cancel-row')).toHaveText(/Stopped by user|已由用户停止/)
  await expect(page.getByTestId('assistant-message')).not.toContainText('context canceled')
  expect(await page.evaluate(() => (window as unknown as { stoppedRoot: Element }).stoppedRoot.isConnected)).toBe(true)
  const readFocusedSession = await focusedSessionReader(page)
  const sessionId = await readFocusedSession()
  expect(sessionId).toBeTruthy()
  await expect.poll(() => page.evaluate(async id => {
    const detail = await fetch(`/v1/sessions/${id}`).then(response => response.json()) as { running?: boolean }
    return detail.running ?? false
  }, sessionId)).toBe(false)
  const persisted = await page.evaluate(async id => fetch(`/v1/sessions/${id}`).then(response => response.json()), sessionId)
  expect(JSON.stringify(persisted)).toContain('"type":"run_aborted"')
  await page.reload()
  await expandWorkspaces(page)
  await page.getByTestId('session-title').filter({ hasText: prompt }).click()
  await expect(page.getByTestId('cancel-row')).toHaveText(/Stopped by user|已由用户停止/)
  await expect(page.getByTestId('assistant-message')).not.toContainText('context canceled')
})

test('paragraph-shaped streams parse each block once, not per delta', async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, 'e2e-blocks-400')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  await expect(page.getByTestId('assistant-message').first()).toContainText('Paragraph 120', { timeout: 60_000 })
  const mid = await page.evaluate(() => {
    const nodes = [...document.querySelectorAll('[data-testid="assistant-message"] .md')] as HTMLElement[]
    return nodes.map(n => n.textContent?.length ?? 0).sort((a, b) => b - a)
  })
  console.log(`paragraph pieces mid-stream (longest first): ${mid.slice(0, 8).join(',')} of ${mid.length}`)
  expect(mid.length, 'sealed segments mid-stream').toBeGreaterThan(2)
  expect(Math.max(...mid), `longest piece mid-stream: ${Math.max(...mid)}`).toBeLessThan(12_000)
  await expect(page.getByTestId('assistant-message').first()).toContainText('Paragraph 399', { timeout: 60_000 })
  await expect(page.getByTestId('assistant-message').first().locator('p')).toHaveCount(400)
  await expect(page.locator('[data-md-tail]')).toHaveCount(0)
  await expect.poll(async () => {
    const text = (await page.getByTestId('assistant-message').first().textContent()) ?? ''
    return text.match(/Paragraph \d+:/g)?.length
  }).toBe(400)
})

test('an unbroken block is painted until it closes, then parsed', async ({ page }) => {
  test.setTimeout(120_000)
  await openStream(page, 0)
  const lines = Array.from({ length: 200 }, (_, i) => `- item ${i} ${'x'.repeat(110)}\n`)
  const send = (event: unknown) => page.evaluate(event => (window as unknown as { streamSend: (event: unknown) => void }).streamSend(event), event)
  await send({ type: 'message_update', runId: 'unbroken', seq: 1, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text: lines.slice(0, 61).join('') }] } })
  await expect(page.getByTestId('assistant-message').first()).toContainText('item 60', { timeout: 60_000 })
  await expect(page.locator('[data-md-tail]')).toHaveCount(1)
  await expect(page.locator('[data-md-tail]')).toContainText('- item 60')
  await expect(page.getByTestId('assistant-message').first().locator('li')).toHaveCount(0)
  const text = lines.join('')
  await send({ type: 'message_update', runId: 'unbroken', seq: 2, messageStream: 1, message: { role: 'assistant', content: [{ type: 'text', text }] } })
  await expect(page.getByTestId('assistant-message').first()).toContainText('item 199', { timeout: 60_000 })
  await expect(page.locator('[data-md-tail]')).toHaveCount(1)
  await expect(page.locator('[data-md-tail]')).toContainText('- item 199')
  await expect(page.getByTestId('assistant-message').first().locator('li')).toHaveCount(0)
  await send({ type: 'message_end', runId: 'unbroken', seq: 3, entryId: 'final', message: { role: 'assistant', content: [{ type: 'text', text }] } })
  await expect(page.locator('[data-md-tail]')).toHaveCount(0)
  await expect(page.getByTestId('assistant-message').first().locator('li')).toHaveCount(200)
  const rendered = (await page.getByTestId('assistant-message').first().textContent()) ?? ''
  expect(rendered.match(/item \d+ /g)?.length).toBe(200)
})
