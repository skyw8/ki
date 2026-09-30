import { expect, test, type Page } from '@playwright/test'
import { serverToken } from './global-setup'
import type { CompactTurn, Entry, LoopEvent } from '../src/api/types'

// Each test owns its session, browser routes and stream; no shared fixture
// state is mutated when the parallel runner gives them separate servers.
test.describe.configure({ mode: 'parallel' })

function deferred() {
  let release!: () => void
  const promise = new Promise<void>(done => { release = done })
  return { promise, release }
}

type StreamControls = {
  historySend: (event: LoopEvent) => void
  historyClose: () => void
}

async function fixture(page: Page, tailGate = Promise.resolve()) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id } = await created.json() as { id: string }
  const title = `history-recovery-${id}`
  const renamed = await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  expect(renamed.ok()).toBeTruthy()
  const all: Entry[] = []
  for (let turn = 1; turn <= 3; turn++) {
    all.push({ type: 'message', id: `u${turn}`, parentId: turn === 1 ? '' : `a${turn - 1}`,
      message: { role: 'user', content: [{ type: 'text', text: `Recovery request ${turn}` }] } })
    all.push({ type: 'message', id: `a${turn}`, parentId: `u${turn}`,
      message: { role: 'assistant', content: [{ type: 'text', text: `Recovery answer ${turn}` }] } })
  }
  let recovered = false
  const tailViews: Array<string | null> = []
  await page.route(url => url.pathname === `/v1/sessions/${id}`, async route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const params = new URL(route.request().url()).searchParams
    if (params.get('fields') === 'index') {
      return route.fulfill({ json: { id, index: (recovered ? all : all.slice(0, 2)).map(e => ({
        type: e.type, id: e.id, parentId: e.parentId, role: e.message?.role, preview: e.message?.content?.[0].text,
      })) } })
    }
    if (params.has('before') || params.has('turn')) return route.fallback()
    if (recovered) {
      tailViews.push(params.get('view'))
      await tailGate
    }
    return route.fulfill({ json: { id, title, entries: recovered ? all.slice(4) : all.slice(0, 2),
      leafId: recovered ? 'a3' : 'a1', oldestId: recovered ? 'u3' : 'u1', hasMore: recovered, running: !recovered } })
  })
  await page.addInitScript(({ id }) => {
    localStorage.setItem('ki-message-view', 'detailed')
    localStorage.setItem('ki-message-view-keep', '1')
    const scope = window as unknown as StreamControls
    const original = window.fetch
    window.fetch = async (...args) => {
      if (String(args[0]).includes(`/v1/sessions/${id}/events`)) {
        const stream = new ReadableStream({ start(controller) {
          scope.historySend = event => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
          scope.historyClose = () => controller.close()
        } })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return original(...args)
    }
  }, { id })
  const open = async () => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    await page.getByTestId('session-row').filter({ hasText: title }).click()
    await expect(page.getByTestId('user-bubble')).toHaveText('Recovery request 1')
    await expect(page.locator('.history-control')).toHaveText('已到最早消息')
    await expect.poll(() => page.evaluate(() => typeof (window as unknown as Partial<StreamControls>).historySend)).toBe('function')
  }
  const finishStream = async () => {
    await page.evaluate(event => (window as unknown as StreamControls).historySend(event), {
      type: 'message_end', runId: 'recovery-run', seq: 1, entryId: 'a3', parentId: 'u3', message: all[5].message,
    } satisfies LoopEvent)
    // Observe the SSE completion before closing: the terminal GET must see
    // the same latest leaf, rather than merely introducing a new frontier.
    await expect(page.getByTestId('assistant-message').filter({ hasText: 'Recovery answer 3' })).toBeVisible()
    recovered = true
    await page.evaluate(() => (window as unknown as StreamControls).historyClose())
    await expect.poll(() => tailViews.length).toBe(1)
  }
  return { id, all, tailViews, open, finishStream }
}

async function expectOpenGap(page: Page) {
  await expect(page.getByTestId('user-bubble')).toHaveText('Recovery request 3')
  await expect(page.getByTestId('load-older')).toHaveText('加载更早的消息')
  await expect(page.getByTestId('load-older')).toBeEnabled()
  await expect(page.locator('.history-control')).not.toContainText('已到最早消息')
}

for (const method of ['load earlier', 'request navigation'] as const) {
  test(`an SSE completion at the recovered leaf reopens missing history through ${method}`, async ({ page }) => {
    const f = await fixture(page)
    const cursors: string[] = []
    await page.route(url => url.pathname === `/v1/sessions/${f.id}` && url.searchParams.has('before'), async route => {
      const cursor = new URL(route.request().url()).searchParams.get('before')!
      cursors.push(cursor)
      expect(cursor).toBe('u3')
      return route.fulfill({ json: { id: f.id, entries: f.all.slice(0, 4), oldestId: 'u1', hasMore: false } })
    })
    await f.open()
    if (method === 'request navigation') {
      await page.getByTestId('request-nav-toggle').focus()
      await page.keyboard.press('Enter')
      await expect(page.getByTestId('request-nav-item')).toHaveCount(1)
      await page.keyboard.press('Escape')
    }
    await f.finishStream()
    await expectOpenGap(page)
    expect(cursors).toEqual([])
    if (method === 'load earlier') {
      await page.getByTestId('load-older').click()
    } else {
      await page.getByTestId('request-nav-toggle').focus()
      await page.keyboard.press('Enter')
      await expect(page.getByTestId('request-nav-item')).toHaveCount(3)
      // u1's body is cached but disconnected from the active window. Jumping
      // to it must page the missing range, not stop at that cached identity.
      await page.locator('[data-request-id="u1"]').click()
      await expect(page.getByTestId('cancel-jump')).toHaveCount(0)
      await expect(page.getByTestId('retry-jump')).toHaveCount(0)
    }
    await expect.poll(() => cursors).toEqual(['u3'])
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'Recovery request 2' })).toBeVisible()
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'Recovery request 1' })).toBeVisible()
    await expect(page.getByTestId('load-older')).toHaveCount(0)
    await expect(page.locator('.history-control')).toHaveText('已到最早消息')
  })
}

function compactTurn(turn: number): CompactTurn {
  return {
    id: `u${turn}`, parentId: turn === 1 ? '' : `a${turn - 1}`, tailId: `a${turn}`,
    entryIds: [`u${turn}`, `a${turn}`], visibleNodeIds: [`a${turn}`], hiddenCount: 0, entryCount: 2, stepCount: 0,
    stats: { turn, steps: 0, elapsedMs: 0, durationMs: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
      tools: 0, toolFailures: 0, cacheMisses: 0, hasCost: false, cost: 0, ttftMs: 0, tps: null, live: false },
  }
}

test('a delayed old-root compact projection retries the recovered boundary instead of exhausting it', async ({ page }) => {
  const tail = deferred()
  const oldProjection = deferred()
  const f = await fixture(page, tail.promise)
  const projections: string[] = []
  const cursors: string[] = []
  await page.route(url => url.pathname === `/v1/sessions/${f.id}` && url.searchParams.has('turn'), async route => {
    const params = new URL(route.request().url()).searchParams
    expect(params.get('view')).toBe('compact')
    const turn = params.get('turn')!
    projections.push(turn)
    if (turn === 'u1') await oldProjection.promise
    else expect(turn).toBe('u3')
    const n = turn === 'u1' ? 1 : 3
    return route.fulfill({ json: { id: f.id, entries: f.all.slice((n - 1) * 2, n * 2),
      compactTurns: [compactTurn(n)], oldestId: turn, hasMore: n === 3 } })
  })
  await page.route(url => url.pathname === `/v1/sessions/${f.id}` && url.searchParams.has('before'), async route => {
    const params = new URL(route.request().url()).searchParams
    cursors.push(params.get('before')!)
    expect(params.get('before')).toBe('u3')
    expect(params.get('view')).toBe('compact')
    return route.fulfill({ json: { id: f.id, entries: f.all.slice(0, 4),
      compactTurns: [compactTurn(1), compactTurn(2)], oldestId: 'u1', hasMore: false } })
  })
  try {
    await f.open()
    await f.finishStream()
    // Recovery captured a detailed GET before the preference changed. Its
    // delayed response legitimately lacks compact summaries for the new tail.
    expect(f.tailViews).toEqual([null])
    await page.getByTestId('open-settings').click()
    await page.getByTestId('settings-tab-message').click()
    await page.getByTestId('view-compact').click()
    await expect.poll(() => projections).toEqual(['u1'])
    await page.getByTestId('settings').getByRole('button', { name: '关闭对话框' }).click()
    tail.release()
    await expect(page.getByTestId('user-bubble')).toHaveText('Recovery request 3')
    await expect(page.getByTestId('load-older')).toBeDisabled()
    oldProjection.release()
    await expect.poll(() => projections).toEqual(['u1', 'u3'])
    await expectOpenGap(page)
    await page.getByTestId('load-older').click()
    await expect.poll(() => cursors).toEqual(['u3'])
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'Recovery request 2' })).toBeVisible()
    await expect(page.getByTestId('load-older')).toHaveCount(0)
    await expect(page.locator('.history-control')).toHaveText('已到最早消息')
  } finally {
    tail.release()
    oldProjection.release()
  }
})
