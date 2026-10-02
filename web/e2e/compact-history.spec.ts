import { expect, test, type Page } from '@playwright/test'
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken } from './global-setup'
import type { Entry, SessionDetail } from '../src/api/types'

test.describe.configure({ mode: 'parallel' })

test('runtime notifications stay outside folds and never hide the final assistant reply after reload', async ({ page }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id, dir } = await created.json() as { id: string; dir: string }
  const title = `runtime-fold-${id}`
  const entries: Entry[] = [
    { type: 'message', id: 'u', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'Human prompt' }] } },
    { type: 'message', id: 'early', parentId: 'u', message: { role: 'assistant', content: [{ type: 'text', text: 'Earlier reply' }] } },
    { type: 'message', id: 'middle', parentId: 'early', message: { role: 'user', origin: 'agent:child', content: [{ type: 'text', text: 'Middle runtime notice' }] } },
    { type: 'message', id: 'final', parentId: 'middle', message: { role: 'assistant', content: [{ type: 'text', text: 'Final agent reply stays visible' }] } },
    // Real mailbox delivery can persist after the terminal assistant. Keep its
    // chronological position, but never let it consume the final reply slot.
    { type: 'message', id: 'trailing', parentId: 'final', message: { role: 'user', origin: 'agent:child', content: [{ type: 'text', text: 'Trailing runtime notice' }] } },
  ]
  appendFileSync(join(dir, 'events.jsonl'), entries.map(e => JSON.stringify(e)).join('\n') + '\n')
  const path = join(dir, 'config.json')
  writeFileSync(path, JSON.stringify({ ...JSON.parse(readFileSync(path, 'utf8')), activeLeafId: 'trailing', title }))
  await page.addInitScript(() => {
    localStorage.setItem('ki-message-view', 'compact')
    if (localStorage.getItem('ki-message-view-keep') == null) localStorage.setItem('ki-message-view-keep', '1')
  })
  const detail = await page.request.get(`/v1/sessions/${id}?view=compact&keep=1`, { headers })
  expect(detail.ok()).toBeTruthy()
  const snapshot = await detail.json() as SessionDetail
  expect(snapshot.compactTurns?.[0].hiddenCount).toBe(1)
  expect(snapshot.compactTurns?.[0].visibleNodeIds).toEqual(['u', 'middle', 'final', 'trailing'])
  const assertVisible = async () => {
    await expect(page.getByTestId('assistant-message')).toHaveText(/Final agent reply stays visible/)
    await expect(page.getByRole('note')).toHaveCount(2)
    await expect(page.getByRole('note').first()).toContainText('Middle runtime notice')
    await expect(page.getByRole('note').last()).toContainText('Trailing runtime notice')
    await expect(page.getByTestId('fold-row')).toContainText('已折叠 1 条消息')
  }
  await page.goto('/')
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await assertVisible()
  await page.reload()
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await assertVisible()
  await page.getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('assistant-message')).toHaveCount(2)
  await expect(page.getByRole('note')).toHaveCount(2)
  await page.getByTestId('fold-row-btn').click()
  await assertVisible()
  await page.evaluate(() => localStorage.setItem('ki-message-view-keep', '0'))
  await page.reload()
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await expect(page.getByTestId('assistant-message')).toHaveCount(0)
  await expect(page.getByRole('note')).toHaveCount(2)
  await expect(page.getByTestId('fold-row')).toContainText('已折叠 2 条消息')
})

async function seed(page: Page) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id, dir } = await created.json() as { id: string; dir: string }
  const path = join(dir, 'config.json')
  const config = JSON.parse(readFileSync(path, 'utf8'))
  const entries: Entry[] = []
  let parent = ''
  const push = (entry: Omit<Entry, 'parentId'>) => { entries.push({ ...entry, parentId: parent }); parent = entry.id }
  for (let t = 0; t < 9; t++) {
    push({ type: 'message', id: `u${t}`, message: { role: 'user', content: [{ type: 'text', text: `Input turn ${t}` }] } })
    for (let n = 0; n < 260; n++) {
      const call = `call-${t}-${n}`
      push({ type: 'message', id: `a-${t}-${n}`, message: { role: 'assistant', content: [{ type: 'text', text: `HIDDEN_BODY_${t}_${n}` }, { type: 'toolCall', id: call, name: 'exec_command', arguments: { cmd: `HIDDEN_ARGUMENT_${t}_${n}` } }] } })
      push({ type: 'message', id: `r-${t}-${n}`, message: { role: 'toolResult', toolCallId: call, toolName: 'exec_command', content: [{ type: 'text', text: `HIDDEN_RESULT_${t}_${n}` }] } })
    }
    push({ type: 'message', id: `final${t}`, message: { role: 'assistant', content: [{ type: 'text', text: `## Final ${t}\n\n**Complete turn** with stable reading content.\n\n- First result\n- Second result\n\n\`\`\`ts\nconst turn = ${t}\n\`\`\`` }] } })
  }
  appendFileSync(join(dir, 'events.jsonl'), entries.map(e => JSON.stringify(e)).join('\n') + '\n')
  config.activeLeafId = parent
  config.title = `compact-${id}`
  writeFileSync(path, JSON.stringify(config))
  await page.addInitScript(() => {
    localStorage.setItem('ki-message-view', 'compact')
    localStorage.setItem('ki-message-view-keep', '1')
  })
  await page.setViewportSize({ width: 390, height: 844 })
  return { id, open: async () => {
    await page.goto('/')
    await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: config.title }).click()
    await expect(page.locator('[data-item-key="final8"]')).toBeVisible()
    await expect(page.locator('[data-md-pending]')).toHaveCount(0)
    await expect(page.getByTestId('session-stats')).toContainText('9 轮 · 2349 步')
  } }
}

async function readTop(page: Page) {
  const scroll = page.getByTestId('chat-scroll')
  const touch = await page.evaluate(() => /iPhone/.test(navigator.userAgent))
  if (touch) {
    await scroll.evaluate(el => {
      // Browser emulation may not expose a Touch constructor.
      // Exercise touch intent/deferral with DOM events; motion is controlled
      // below, so this is not a claim of native inertial-gesture coverage.
      const touch = (type: string, y?: number) => {
        const event = new Event(type, { bubbles: true })
        Object.defineProperty(event, 'touches', { value: y == null ? [] : [{ identifier: 1, target: el, clientX: 160, clientY: y }] })
        el.dispatchEvent(event)
      }
      touch('touchstart', 300)
      touch('touchmove', 340)
      el.scrollTop = 0
      touch('touchend')
    })
  } else {
    await scroll.hover()
    await scroll.evaluate(el => {
      // mouse.wheel resolves before the browser necessarily delivers the
      // event. An already-reading intent cannot acknowledge this new input:
      // releasing a held page first would legitimately arm a second page.
      // The bubble listener runs after the scroll owner's capture listener.
      ;(window as unknown as { compactWheelReceipt: Promise<void> }).compactWheelReceipt = new Promise(resolve => {
        const received = (event: Event) => {
          if (!(event instanceof WheelEvent) || !event.isTrusted || event.deltaY !== -5) return
          el.removeEventListener('wheel', received)
          resolve()
        }
        el.addEventListener('wheel', received, { passive: true })
      })
    })
    await page.mouse.wheel(0, -5)
    await page.evaluate(() => (window as unknown as { compactWheelReceipt: Promise<void> }).compactWheelReceipt)
  }
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  await scroll.evaluate(el => { el.scrollTop = 0 })
}

test('request navigation supersedes a slow history jump and anchors the requested compact turn', async ({ page }) => {
  const f = await seed(page)
  let release!: () => void
  const gate = new Promise<void>(done => { release = done })
  let pages = 0
  await page.route(`**/v1/sessions/${f.id}?before=*`, async route => {
    pages++
    if (pages === 1) await gate
    return route.continue()
  })
  await f.open()
  const toggle = page.getByTestId('request-nav-toggle')
  await toggle.click()
  await page.getByTestId('request-nav-item').filter({ hasText: 'Input turn 0' }).click()
  await expect.poll(() => pages).toBe(1)
  if (!await page.getByTestId('request-nav-panel').isVisible()) await toggle.click()
  await page.getByTestId('request-nav-item').filter({ hasText: 'Input turn 3' }).click()
  release()
  const offset = () => page.locator('[data-item-key="u3"]').evaluate(el => el.getBoundingClientRect().top - el.closest('[data-testid="chat-scroll"]')!.getBoundingClientRect().top)
  await expect.poll(async () => Math.abs(await offset())).toBeLessThanOrEqual(2)
  await expect(page.getByTestId('cancel-jump')).toHaveCount(0)
  if (!await page.getByTestId('request-nav-panel').isVisible()) await toggle.click()
  await expect(page.locator('[data-request-id="u3"]')).toHaveAttribute('aria-selected', 'true')
  await page.waitForTimeout(300)
  expect(Math.abs(await offset())).toBeLessThanOrEqual(2)
  expect(pages).toBeLessThanOrEqual(3)
})

test('compact paging adds one complete turn and keeps the same content through successive prepends', async ({ page }) => {
  const f = await seed(page)
  const pages: SessionDetail[] = []
  const gates: Array<() => void> = []
  let bodies = 0
  await page.route(`**/v1/sessions/${f.id}?*`, async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.has('turn') || params.has('entries')) bodies++
    if (!params.has('before')) return route.fallback()
    expect(params.get('view')).toBe('compact')
    const response = await route.fetch()
    const data = await response.json() as SessionDetail
    pages.push(data)
    await new Promise<void>(done => gates.push(done))
    await route.fulfill({ response })
  })
  await f.open()
  expect(pages).toHaveLength(0)
  for (let i = 0; i < 3; i++) {
    await readTop(page)
    await expect.poll(() => gates.length).toBe(i + 1)
    await expect(page.locator('[data-md-pending]')).toHaveCount(0)
    const key = `u${5 - i}`
    await expect(page.locator(`[data-item-key="${key}"]`)).toBeVisible()
    const probe = () => page.evaluate(key => {
      const el = document.querySelector('[data-testid="chat-scroll"]')!
      const row = el.querySelector(`[data-item-key="${key}"]`)!
      return row.getBoundingClientRect().top - el.getBoundingClientRect().top
    }, key)
    const before = await probe()
    const fold = page.locator(`[data-fold="${key}"] .fold-row-count`)
    await expect(fold).toHaveText('已折叠 520 条消息')
    // More input events during the same pending load must not queue a second
    // turn merely because its measured height leaves us near the boundary.
    await readTop(page)
    await page.evaluate(({ key, before }) => {
      const state = { active: true, worst: 0, missing: false, trace: [] as unknown[] }
      ;(window as unknown as { anchorProbe: typeof state }).anchorProbe = state
      const sample = () => {
        if (!state.active) return
        const el = document.querySelector('[data-testid="chat-scroll"]')!
        const row = el.querySelector(`[data-item-key="${key}"]`)
        if (!row) state.missing = true
        else {
          const offset = row.getBoundingClientRect().top - el.getBoundingClientRect().top
          if (state.trace.length < 50) state.trace.push({ offset, scroll: el.scrollTop, logical: (el.firstElementChild as HTMLElement)?.dataset.scrollOffset, scrolling: (el.firstElementChild as HTMLElement)?.dataset.libraryScrolling })
          state.worst = Math.max(state.worst, Math.abs(offset - before))
        }
        requestAnimationFrame(sample)
      }
      requestAnimationFrame(sample)
    }, { key, before })
    gates[i]()
    await expect(page.getByTestId('older-loading')).toHaveCount(0)
    await expect.poll(async () => Math.abs(await probe() - before)).toBeLessThanOrEqual(2)
    await page.waitForTimeout(400)
    expect(Math.abs(await probe() - before)).toBeLessThanOrEqual(2)
    const frames = await page.evaluate(() => {
      const state = (window as unknown as { anchorProbe: { active: boolean; worst: number; missing: boolean } }).anchorProbe
      state.active = false
      return state
    })
    expect(frames.missing).toBe(false)
    expect(frames.worst, `the anchor must also stay put between commit and final measurements: ${JSON.stringify(frames)}`).toBeLessThanOrEqual(2)
    await expect(fold).toHaveText('已折叠 520 条消息')
    expect(pages[i].compactTurns?.map(t => t.id)).toEqual([`u${4 - i}`])
    expect(pages[i].entries?.map(e => e.id)).toEqual([`u${4 - i}`, `final${4 - i}`])
    expect(JSON.stringify(pages[i])).not.toContain('HIDDEN_')
    expect(pages).toHaveLength(i + 1)
  }
  expect(bodies).toBe(0)
})

test('compact expands hidden contents only on demand and preserves its paging cursor', async ({ page }) => {
  const f = await seed(page)
  const expansions: string[] = []
  let pages = 0
  await page.route(`**/v1/sessions/${f.id}?*`, async route => {
    const p = new URL(route.request().url()).searchParams
    if (p.has('turn')) expansions.push(p.get('turn')!)
    else if (p.has('before')) pages++
    return route.fallback()
  })
  await f.open()
  expect(expansions).toHaveLength(0)
  // The newest turn's fold follows the tail: expanding fetches the hidden
  // replies on demand and pins the newest revealed node to the bottom, so the
  // fold row itself scrolls out of the virtualized window. The proof it opened
  // is the fetched reply, not the row's own attribute.
  // Following the newest message must not jump while the revealed rows are
  // measured over the next frames: sample the distance from the tail across the
  // expansion. A follow that only pins once drifts, which is the visible jitter.
  const shiftProbe = page.evaluate(async () => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const gaps: number[] = []
    for (let i = 0; i < 40; i++) {
      gaps.push(Math.round(Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop)))
      await new Promise(r => requestAnimationFrame(r))
    }
    return gaps
  })
  await page.locator('[data-fold="u8"]').getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('tool-card').first()).toBeVisible()
  const gaps = await shiftProbe
  expect(Math.max(...gaps), `distance from the tail across the expansion: ${gaps.join(',')}`).toBeLessThanOrEqual(8)
  expect(expansions.length).toBeGreaterThan(1)
  expect(expansions.every(id => id === 'u8')).toBe(true)
  // Expanding must not page: the history cursor only moves when the reader
  // scrolls up, not when a fold grows in place.
  expect(pages).toBe(0)
  await readTop(page)
  await expect.poll(() => pages).toBe(1)
  // An earlier fold is something the reader opened to read: it anchors in place
  // and toggles closed on the same row, keeping its count.
  const older = page.locator('[data-fold="u5"]')
  await expect(older.locator('.fold-row-count')).toHaveText('已折叠 520 条消息')
  await older.getByTestId('fold-row-btn').click()
  await expect(older.getByTestId('fold-row-btn')).toHaveAttribute('aria-expanded', 'true')
  await older.getByTestId('fold-row-btn').click()
  await expect(older.locator('.fold-row-count')).toHaveText('已折叠 520 条消息')
})

test('a page arriving during an active touch waits to commit without moving the reader', async ({ page }) => {
  const f = await seed(page)
  let release!: () => void
  const gate = new Promise<void>(done => { release = done })
  let ready = false
  await page.route(`**/v1/sessions/${f.id}?before=*`, async route => {
    const response = await route.fetch()
    ready = true
    await gate
    await route.fulfill({ response })
  })
  await f.open()
  const scroll = page.getByTestId('chat-scroll')
  await scroll.evaluate(el => {
    const dispatch = (type: string, y: number) => {
      const event = new Event(type, { bubbles: true })
      Object.defineProperty(event, 'touches', { value: [{ clientY: y }] })
      el.dispatchEvent(event)
    }
    dispatch('touchstart', 300)
    dispatch('touchmove', 360)
    el.scrollTop = 0
  })
  await expect.poll(() => ready).toBe(true)
  await expect(page.locator('[data-md-pending]')).toHaveCount(0)
  const offset = () => scroll.evaluate(el => el.querySelector('[data-item-key="u5"]')!.getBoundingClientRect().top - el.getBoundingClientRect().top)
  const before = await offset()
  release()
  await page.waitForTimeout(250)
  await expect(page.getByTestId('older-loading')).toHaveCount(1)
  expect(Math.abs(await offset() - before)).toBeLessThanOrEqual(2)
  await scroll.evaluate(el => {
    const event = new Event('touchend', { bubbles: true })
    Object.defineProperty(event, 'touches', { value: [] })
    el.dispatchEvent(event)
  })
  await expect(page.getByTestId('older-loading')).toHaveCount(0)
  await expect.poll(async () => Math.abs(await offset() - before)).toBeLessThanOrEqual(2)
})

test('compact reload and expansion count replies without visible compaction metadata', async ({ page }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id, dir } = await created.json() as { id: string; dir: string }
  const path = join(dir, 'config.json')
  const config = JSON.parse(readFileSync(path, 'utf8'))
  const title = `metadata-count-${id}`
  const entries: Entry[] = [
    { type: 'message', id: 'input', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'Inspect compact count' }] } },
    { type: 'message', id: 'hidden', parentId: 'input', message: { role: 'assistant', content: [{ type: 'text', text: 'Earlier reply' }] } },
    { type: 'message', id: 'final', parentId: 'hidden', message: { role: 'assistant', content: [{ type: 'text', text: 'Final reply' }] } },
    { type: 'compaction', id: 'checkpoint', parentId: 'final', summary: 'Visible checkpoint', tokensBefore: 1000 },
  ]
  appendFileSync(join(dir, 'events.jsonl'), entries.map(e => JSON.stringify(e)).join('\n') + '\n')
  writeFileSync(path, JSON.stringify({ ...config, title, activeLeafId: 'checkpoint' }))
  await page.addInitScript(() => {
    localStorage.setItem('ki-message-view', 'compact')
    localStorage.setItem('ki-message-view-keep', '1')
  })
  const open = async () => {
    if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: title }).click()
    await expect(page.locator('[data-fold="input"] .fold-row-count')).toHaveText('已折叠 1 条消息')
    await expect(page.locator('[data-item-key="checkpoint"]')).toBeVisible()
    await expect(page.getByTestId('assistant-message')).toHaveCount(1)
  }
  await page.goto('/')
  await open()
  await page.reload()
  await open()
  await page.locator('[data-fold="input"]').getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('assistant-message')).toHaveCount(2)
  await expect(page.locator('[data-fold="input"] .fold-row-count')).toHaveText('已折叠 1 条消息')
  await expect(page.locator('[data-item-key="checkpoint"]')).toBeVisible()
})
