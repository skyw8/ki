import { expect, type Page } from '@playwright/test'
import { serverToken } from './global-setup'

export async function openStream(page: Page, history: number, emptySnapshot = false) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `stream-budget-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  const entries = Array.from({ length: history }, (_, i) => ({ type: 'message', id: `h${i}`, parentId: i ? `h${i - 1}` : '', message: {
    role: i % 2 ? 'assistant' : 'user', content: [{ type: 'text', text: `History ${i}` }],
  } }))
  entries.push({ type: 'message', id: 'question', parentId: entries.at(-1)?.id ?? '', message: { role: 'user', content: [{ type: 'text', text: 'Streaming render budget' }] } })
  await page.route(`**/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries: emptySnapshot ? [] : entries, running: true, leafId: emptySnapshot ? undefined : 'question', hasMore: false } }) : route.fallback())
  await page.addInitScript(({ id }) => {
    localStorage.setItem('ki-stream-metrics', '1')
    const scope = window as unknown as { streamSend: (value: unknown) => void; streamFail: () => void; streamClose: () => void; streamURLs: string[]; streamCursors: (string | null)[] }
    scope.streamURLs = []
    scope.streamCursors = []
    const original = window.fetch
    window.fetch = async (...args) => {
      if (String(args[0]).includes(`/v1/sessions/${id}/events`)) {
        scope.streamURLs.push(String(args[0]))
        scope.streamCursors.push(new Headers(args[1]?.headers).get('Last-Event-ID'))
        const stream = new ReadableStream({ start(controller) {
          scope.streamFail = () => controller.error(new TypeError("test disconnect"))
          scope.streamClose = () => controller.close()
          scope.streamSend = value => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`))
        } })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return original(...args)
    }
  }, { id })
  await page.goto('/')
  // The responsive toggle does not exist during the initial auth check.
  // Inspecting visibility before the shell mounts misclassifies tablet/mobile.
  await expect(page.locator('main.main')).toBeVisible()
  if (await page.getByTestId('mobile-nav-toggle').isVisible()) {
    await page.getByTestId('mobile-nav-toggle').click()
  }
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await expect.poll(() => page.evaluate(() => typeof (window as unknown as { streamSend?: unknown }).streamSend)).toBe('function')
  return { id, title, entries }
}
