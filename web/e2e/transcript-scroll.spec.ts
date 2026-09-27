import { expect, test, type Page } from '@playwright/test'
import { serverToken } from './global-setup'
import type { Entry, LoopEvent } from '../src/api/types'

test.describe.configure({ mode: 'parallel' })

function entries(count: number): Entry[] {
  return Array.from({ length: count }, (_, i) => ({
    type: 'message', id: `u${i}`, parentId: i ? `u${i - 1}` : '',
    message: { role: 'user', content: [{ type: 'text', text: `History row ${i}` }] },
  }))
}

async function fixture(page: Page, all: Entry[], tail: number, running = false) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id } = await created.json() as { id: string }
  const title = `scroll-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  let indexRequests = 0
  await page.route(`**/v1/sessions/${id}?*`, async route => {
    const url = new URL(route.request().url())
    if (url.searchParams.get('fields') === 'index') {
      indexRequests++
      return route.fulfill({ json: { id, index: all.map(e => ({ type: e.type, id: e.id, parentId: e.parentId, role: e.message?.role, preview: e.message?.content?.[0].text })) } })
    }
    return route.fallback()
  })
  await page.route(`**/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries: all.slice(-tail), leafId: all.at(-1)?.id, hasMore: all.length > tail, oldestId: all.at(-tail)?.id, running } })
    : route.fallback())
  return { id, indexRequests: () => indexRequests, open: async () => {
    await page.goto('/')
    await page.getByTestId('session-row').filter({ hasText: title }).click()
    await expect(page.getByTestId('chat')).toBeVisible()
    await page.setViewportSize({ width: 390, height: 844 })
    await expect.poll(() => page.getByTestId('chat-scroll').evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight)).toBeLessThan(9)
  } }
}

test('no-overflow history loads on upward intent, stops on error and retries at the top', async ({ page }) => {
  const all = entries(3)
  const f = await fixture(page, all, 1)
  let requests = 0
  await page.route(`**/v1/sessions/${f.id}?before=*`, async route => {
    requests++
    if (requests === 1) return route.fulfill({ status: 503, body: 'retry' })
    return route.fulfill({ json: { id: f.id, entries: all.slice(0, 2), oldestId: 'u0', hasMore: false } })
  })
  await f.open()
  await page.waitForTimeout(350)
  expect(requests).toBe(0)
  expect(f.indexRequests()).toBe(0)
  const scroll = page.getByTestId('chat-scroll')
  await scroll.hover()
  await page.mouse.wheel(0, -40)
  await expect(page.getByTestId('load-older')).toHaveText('加载失败，点击重试')
  await page.mouse.wheel(0, -40)
  await page.waitForTimeout(200)
  expect(requests).toBe(1)
  await page.getByTestId('load-older').click()
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'History row 0' })).toBeVisible()
  expect(requests).toBe(2)
  expect(f.indexRequests()).toBe(0)
})

test('a delayed prepend preserves the item being read just before the response', async ({ page }) => {
  const all = entries(200)
  const f = await fixture(page, all, 100)
  let release!: () => void
  const gate = new Promise<void>(done => { release = done })
  let requests = 0
  await page.route(`**/v1/sessions/${f.id}?before=*`, async route => {
    requests++
    await gate
    await route.fulfill({ json: { id: f.id, entries: all.slice(0, 100), oldestId: 'u0', hasMore: false } })
  })
  await f.open()
  const scroll = page.getByTestId('chat-scroll')
  await scroll.hover()
  await page.mouse.wheel(0, -10)
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  // Enter reading before accelerating to the page boundary. A scripted
  // scrollTop write alone is not user intent and can race viewport-follow
  // measurement after the fixture changes from desktop to mobile width.
  await scroll.evaluate(el => { el.scrollTop = 240 })
  await expect.poll(() => requests).toBe(1)
  // Continue reading during the slow response. The request-start offset is
  // deliberately wrong now; capture a stable key before releasing the page.
  await scroll.evaluate(el => { el.scrollTop = 900 })
  await page.waitForTimeout(800)
  const anchor = await scroll.evaluate(el => {
    const top = el.getBoundingClientRect().top
    const row = [...el.querySelectorAll<HTMLElement>('[data-item-key]')].find(row => row.getBoundingClientRect().bottom > top + 1)!
    return { key: row.dataset.itemKey!, offset: row.getBoundingClientRect().top - top }
  })
  release()
  await expect(page.getByTestId('load-older')).toHaveCount(0)
  const offset = () => scroll.evaluate((el, key) => el.querySelector(`[data-item-key="${key}"]`)!.getBoundingClientRect().top - el.getBoundingClientRect().top, anchor.key)
  await expect.poll(async () => Math.abs(await offset() - anchor.offset)).toBeLessThanOrEqual(2)
  await page.waitForTimeout(400)
  expect(Math.abs(await offset() - anchor.offset)).toBeLessThanOrEqual(2)
  expect(requests).toBe(1)
})

for (const distance of [10, 30, 60]) test(`upward intent ${distance}px from the end stays in reading during streaming`, async ({ page }) => {
  const f = await fixture(page, entries(30), 30, true)
  await page.addInitScript(({ id }) => {
    const original = window.fetch
    window.fetch = async (...args) => {
      if (String(args[0]).includes(`/v1/sessions/${id}/events`)) {
        const stream = new ReadableStream({ start(controller) {
          ;(window as unknown as { sendFrame: (event: unknown) => void }).sendFrame = event => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
        } })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return original(...args)
    }
  }, { id: f.id })
  await f.open()
  const send = (event: LoopEvent) => page.evaluate(event => (window as unknown as { sendFrame: (event: unknown) => void }).sendFrame(event), event)
  await expect.poll(() => page.evaluate(() => typeof (window as unknown as { sendFrame?: unknown }).sendFrame)).toBe('function')
  await send({ type: 'message_update', seq: 1, message: { role: 'assistant', content: [{ type: 'text', text: 'First line' }] } })
  await expect(page.getByTestId('assistant-message')).toContainText('First line')
  await page.waitForTimeout(200)
  const scroll = page.getByTestId('chat-scroll')
  await scroll.hover()
  await page.mouse.wheel(0, -distance)
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  await page.waitForTimeout(120)
  const top = await scroll.evaluate(el => el.scrollTop)
  for (let i = 2; i <= 5; i++) {
    await send({ type: 'message_update', seq: i, message: { role: 'assistant', content: [{ type: 'text', text: 'First line' + '\n\nNew line'.repeat(i) }] } })
    await page.waitForTimeout(120)
  }
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  expect(Math.abs(await scroll.evaluate(el => el.scrollTop) - top)).toBeLessThanOrEqual(2)
  await page.getByTestId('to-bottom').click()
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'following')
})

test('switching sessions cancels late paging, hydration and index responses', async ({ page }) => {
  const all = entries(100)
  all.push({ type: 'message', id: 'a100', parentId: 'u99', truncated: true, message: { role: 'assistant', content: [{ type: 'text', text: 'A preview' }] } })
  const a = await fixture(page, all, 40)
  const b = await fixture(page, [{ type: 'message', id: 'b1', message: { role: 'user', content: [{ type: 'text', text: 'B only' }] } }], 1)
  let release!: () => void
  const gate = new Promise<void>(done => { release = done })
  const started = new Set<string>()
  await page.route(`**/v1/sessions/${a.id}?*`, async route => {
    const params = new URL(route.request().url()).searchParams
    const kind = params.has('before') ? 'page' : params.has('entries') ? 'body' : params.get('fields') === 'index' ? 'index' : ''
    if (!kind) return route.fallback()
    started.add(kind)
    await gate
    await route.fulfill({ json: { id: a.id, entries: all, index: all.map(e => ({ id: e.id, type: e.type, parentId: e.parentId, role: e.message?.role })), oldestId: 'u0', hasMore: false } }).catch(() => {})
  })
  await a.open()
  await expect.poll(() => started.has('body')).toBe(true)
  await page.getByTestId('request-nav-toggle').click()
  await expect.poll(() => started.has('index')).toBe(true)
  await page.keyboard.press('Escape')
  const scroll = page.getByTestId('chat-scroll')
  await scroll.hover()
  await page.mouse.wheel(0, -20)
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  await scroll.evaluate(el => { el.scrollTop = 100 })
  await expect.poll(() => started.has('page')).toBe(true)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.getByTestId('session-row').filter({ hasText: `scroll-${b.id}` }).click()
  await expect(page.getByTestId('user-bubble')).toHaveText('B only')
  release()
  await page.waitForTimeout(350)
  await expect(page.getByTestId('user-bubble')).toHaveText('B only')
  await expect(page.getByTestId('assistant-message')).toHaveCount(0)
  await expect(page.getByTestId('older-loading')).toHaveCount(0)
  expect(b.indexRequests()).toBe(0)
})

test('tool expansion hydrates entry ids, exposes failures and retries the full result', async ({ page }) => {
  const all: Entry[] = [
    { type: 'message', id: 'u', message: { role: 'user', content: [{ type: 'text', text: 'Inspect the result' }] } },
    { type: 'message', id: 'call-entry', parentId: 'u', message: { role: 'assistant', content: [{ type: 'toolCall', id: 'call-id', name: 'Bash', arguments: { command: 'echo result' } }] } },
    { type: 'message', id: 'result-entry', parentId: 'call-entry', truncated: true, message: { role: 'toolResult', toolCallId: 'call-id', toolName: 'Bash', content: [{ type: 'text', text: 'preview' }] } },
  ]
  const f = await fixture(page, all, 3)
  const requested: string[] = []
  await page.route(`**/v1/sessions/${f.id}?entries=*`, async route => {
    const ids = new URL(route.request().url()).searchParams.get('entries')!
    requested.push(ids)
    if (requested.length === 1) return route.fulfill({ status: 503, body: 'retry' })
    return route.fulfill({ json: { id: f.id, entries: [{ ...all[2], truncated: false, message: { ...all[2].message, content: [{ type: 'text', text: 'preview\nfull result marker' }] } }] } })
  })
  await f.open()
  const tool = page.getByTestId('tool-card')
  await tool.locator('.tool-row-toggle').click()
  await tool.getByText('加载失败，点击重试').click()
  await expect(tool).toContainText('full result marker')
  expect(requested).toEqual(['result-entry', 'result-entry'])
})

test('an open image preview survives its transcript row being virtualized away', async ({ page }) => {
  const all = entries(50)
  all[49].message!.content!.push({ type: 'image', name: 'pixel.png', mimeType: 'image/png', data: 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=' })
  const f = await fixture(page, all, 50)
  await f.open()
  await page.getByRole('button', { name: '放大查看图片' }).click()
  const preview = page.getByRole('dialog', { name: '图片预览' })
  await expect(preview).toBeVisible()
  await page.getByTestId('chat-scroll').evaluate(el => { el.scrollTop = 0 })
  await expect(page.locator('[data-item-key="u49"]')).toHaveCount(0)
  await expect(preview).toBeVisible()
  expect(await preview.locator('img').evaluate(img => img.naturalWidth)).toBe(1)
  await page.getByRole('button', { name: '关闭图片预览' }).click()
  await expect(preview).toHaveCount(0)
})
