import { expect, test, type Page } from '@playwright/test'
import type { Entry, LoopEvent } from '../src/api/types'
import { serverToken } from './global-setup'
import { openStream } from './stream-fixture'
import { durationText } from '../src/lib/duration'

const layouts = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 834, height: 1112 },
  { name: 'mobile', width: 390, height: 744 },
  { name: 'narrow', width: 320, height: 720 },
]

function message(id: string, parentId: string, role: 'user' | 'assistant', text: string, origin?: string): Entry {
  return { type: 'message', id, parentId, message: { role, origin, timestamp: 1_800_000_000_000, content: [{ type: 'text', text }] } }
}

async function history(page: Page, entries: Entry[], compact = false) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `presentation-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, entries, running: false, leafId: entries.at(-1)?.id, oldestId: entries[0]?.id, hasMore: false } })
    : route.fallback())
  await page.addInitScript(({ compact }) => {
    localStorage.setItem('ki-message-view', compact ? 'compact' : 'detailed')
    localStorage.setItem('ki-message-view-keep', '1')
  }, { compact })
  return { id, open: async () => {
    await page.goto('/')
    await expect(page.locator('main.main')).toBeVisible()
    if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: title }).click()
    await expect(page.getByTestId('chat')).toBeVisible()
    await expect.poll(() => page.getByTestId('chat-scroll').evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(9)
  } }
}

/** Every animation frame, not just the settled screenshot. Only adjacent
 * mounted indexes may be compared; an unmounted overscan interval is not a gap. */
async function startGeometryAudit(page: Page) {
  await page.evaluate(() => {
    const scope = window as unknown as { presentationAudit: { frames: number; violations: string[]; stop: boolean } }
    const audit = scope.presentationAudit = { frames: 0, violations: [], stop: false }
    const frame = () => {
      if (audit.stop) return
      audit.frames++
      const scroll = document.querySelector<HTMLElement>('[data-testid="chat-scroll"]')!
      const viewport = scroll.getBoundingClientRect()
      const rows = [...scroll.querySelectorAll<HTMLElement>('.chat-virtual-item[data-index]')]
        .map(el => ({ el, rect: el.getBoundingClientRect(), index: Number(el.dataset.index) })).sort((a, b) => a.index - b.index)
      for (let i = 1; i < rows.length; i++) {
        const previous = rows[i - 1], row = rows[i]
        if (row.index !== previous.index + 1 || row.rect.top > viewport.bottom || previous.rect.bottom < viewport.top) continue
        const gap = row.rect.top - previous.rect.bottom
        if (Math.abs(gap) > 1) audit.violations.push(`${audit.frames}: ${previous.el.dataset.itemKey} → ${row.el.dataset.itemKey}: gap ${gap}`)
      }
      for (const row of rows) {
        if (row.rect.bottom <= viewport.top || row.rect.top >= viewport.bottom) continue
        const content = row.el.lastElementChild?.getBoundingClientRect()
        if (content && content.bottom > row.rect.bottom + 1) audit.violations.push(`${audit.frames}: content overflows ${row.el.dataset.itemKey}`)
        if (!row.el.querySelector('[data-md-pending]') && content && row.rect.bottom - content.bottom > 32) audit.violations.push(`${audit.frames}: empty row space ${row.el.dataset.itemKey}`)
      }
      if (scroll.scrollHeight > scroll.clientHeight && !rows.some(row => row.rect.bottom > viewport.top && row.rect.top < viewport.bottom)) {
        audit.violations.push(`${audit.frames}: blank viewport at ${scroll.scrollTop}; rows ${rows.map(row => `${row.index}:${Math.round(row.rect.top)}-${Math.round(row.rect.bottom)}`).join(',')}`)
      }
      requestAnimationFrame(frame)
    }
    requestAnimationFrame(frame)
  })
}

async function finishGeometryAudit(page: Page) {
  const result = await page.evaluate(() => {
    const audit = (window as unknown as { presentationAudit: { frames: number; violations: string[]; stop: boolean } }).presentationAudit
    audit.stop = true
    return audit
  })
  expect(result.frames).toBeGreaterThan(12)
  expect(result.violations).toEqual([])
}

for (const layout of layouts) test.describe(`transcript presentation ${layout.name}`, () => {
  const touch = layout.width < 1000
  test.use({
    viewport: { width: layout.width, height: layout.height },
    hasTouch: touch,
    // Firefox supports a small touch/coarse-pointer viewport, not Playwright's
    // mobile-layout emulation. Keep the complete geometry matrix on that
    // engine without claiming a mobile UA or physical-device verification.
    isMobile: async ({ browserName }, use) => { await use(touch && browserName !== 'firefox') },
  })
  test.beforeEach(async ({ browserName }, testInfo) => {
    testInfo.annotations.push({
      type: 'viewport-profile',
      description: `${layout.width}×${layout.height}; ${touch ? 'touch/coarse pointer' : 'desktop pointer'}; ${touch && browserName !== 'firefox' ? 'mobile layout emulated' : 'desktop layout engine'}; default browser UA; no physical device`,
    })
  })

  for (const lang of ['zh', 'en']) test(`timers keep adjacent text within 0.5px in ${lang}`, async ({ page }) => {
    await page.addInitScript(lang => localStorage.setItem('ki-lang', lang), lang)
    await openStream(page, 0)
    const now = Date.now()
    const send = (event: LoopEvent) => page.evaluate(event => (window as unknown as { streamSend: (event: LoopEvent) => void }).streamSend(event), event)
    await send({ type: 'turn_start', runId: 'timer', seq: 1, timestamp: now })
    await send({ type: 'message_end', runId: 'timer', seq: 2, entryId: 'timed', parentId: 'question', message: { role: 'user', timestamp: now, content: [{ type: 'text', text: 'Run delegated review' }] } })
    await send({ type: 'message_end', runId: 'timer', seq: 3, entryId: 'call', parentId: 'timed', message: { role: 'assistant', timestamp: now, content: [{ type: 'toolCall', id: 'tool', name: 'Bash', arguments: { command: 'sleep 30', description: 'Static neighboring preview' } }] } })
    await send({ type: 'tool_execution_start', runId: 'timer', seq: 4, toolCallId: 'tool', toolName: 'Bash', timestamp: now, args: { command: 'sleep 30', description: 'Static neighboring preview' } })
    await expect(page.getByTestId('tool-duration')).toBeVisible()
    const samples: number[][] = []
    for (const elapsed of [999, 1000, 9900, 10_000, 10_200, 11_000, 59_900, 60_000, 61_000, 3_599_000, 3_600_000, 3_601_000, 86_399_000, 86_400_000, 86_401_000, 864_000_000]) {
      await page.clock.setFixedTime(new Date(now + elapsed))
      await expect(page.getByTestId('tool-duration').locator('.duration-value')).toHaveText(durationText(elapsed))
      samples.push(await page.evaluate(() => {
        const selectors = ['.turn-end-label', '.session-stats-g', '.tool-name', '[data-testid="tool-preview"]', '[data-testid="tool-duration"]', '[data-testid="turn-elapsed"]', '[data-testid="session-elapsed"]']
        return selectors.flatMap(selector => {
          const el = document.querySelector<HTMLElement>(selector)!
          const box = el.getBoundingClientRect()
          if (box.left < -0.5 || box.right > innerWidth + 0.5) throw new Error(`Timer clipped: ${selector}`)
          return [box.left, box.top, box.width, box.height]
        })
      }))
    }
    for (const sample of samples.slice(1)) sample.forEach((value, i) => expect(Math.abs(value - samples[0][i])).toBeLessThanOrEqual(0.5))
  })

  test('long extension tool names truncate accessibly without covering live or settled timers', async ({ page }) => {
    await openStream(page, 0)
    const name = `multi_tool_use.parallel_extension_provider_${'descriptive_scope_'.repeat(12)}`
    const now = Date.now()
    const send = (event: LoopEvent) => page.evaluate(event => (window as unknown as { streamSend: (event: LoopEvent) => void }).streamSend(event), event)
    await send({ type: 'turn_start', runId: 'long-name', seq: 1, timestamp: now })
    await send({ type: 'message_end', runId: 'long-name', seq: 2, entryId: 'long-call', parentId: 'question', message: { role: 'assistant', timestamp: now, content: [{ type: 'toolCall', id: 'long-tool', name, arguments: { description: 'Accessible long tool name' } }] } })
    await send({ type: 'tool_execution_start', runId: 'long-name', seq: 3, toolCallId: 'long-tool', toolName: name, timestamp: now, args: { description: 'Accessible long tool name' } })
    const tool = page.getByTestId('tool-card')
    const toggle = tool.locator('.tool-row-toggle')
    await expect(toggle).toHaveAccessibleName(`展开 ${name}`)
    await expect(toggle).toHaveAttribute('title', name)
    const sample = () => tool.evaluate(el => {
      const header = el.querySelector<HTMLElement>('.tool-row-h')!
      const toggle = el.querySelector<HTMLElement>('.tool-row-toggle')!
      const name = el.querySelector<HTMLElement>('.tool-name')!
      const duration = el.querySelector<HTMLElement>('.tool-duration')!
      const h = header.getBoundingClientRect(), b = toggle.getBoundingClientRect(), n = name.getBoundingClientRect(), d = duration.getBoundingClientRect()
      const scroll = el.closest<HTMLElement>('[data-testid="chat-scroll"]')!
      return {
        buttonWidth: b.width, buttonHeight: b.height, truncated: name.scrollWidth > name.clientWidth,
        overflow: getComputedStyle(name).textOverflow,
        timerLeft: d.left, timerWidth: d.width, overlap: b.right - d.left,
        headerWidth: h.width, scrollClientWidth: scroll.clientWidth, scrollHeight: scroll.scrollHeight, viewportHeight: scroll.clientHeight,
        inside: d.left >= h.left - .5 && d.right <= h.right + .5 && n.right <= b.right + .5,
        horizontalOverflow: scroll.scrollWidth - scroll.clientWidth,
      }
    })
    let first: Awaited<ReturnType<typeof sample>> | undefined
    const assertGeometry = async () => {
      const box = await sample()
      expect(box.buttonWidth).toBeGreaterThanOrEqual(40)
      expect(box.buttonHeight).toBeGreaterThanOrEqual(40)
      expect(box.truncated).toBe(true)
      expect(box.overflow).toBe('ellipsis')
      expect(box.inside).toBe(true)
      expect(box.overlap).toBeLessThanOrEqual(0)
      expect(box.horizontalOverflow).toBeLessThanOrEqual(1)
      if (first) {
        expect(Math.abs(box.scrollClientWidth - first.scrollClientWidth)).toBeLessThanOrEqual(.5)
        expect(Math.abs(box.headerWidth - first.headerWidth)).toBeLessThanOrEqual(.5)
        expect(Math.abs(box.timerLeft - first.timerLeft), JSON.stringify({ first, current: box })).toBeLessThanOrEqual(.5)
        expect(Math.abs(box.timerWidth - first.timerWidth)).toBeLessThanOrEqual(.5)
      } else first = box
    }
    for (const elapsed of [9900, 10_000, 3_600_000, 3_601_000]) {
      await page.clock.setFixedTime(new Date(now + elapsed))
      await expect(tool.getByTestId('tool-duration')).toHaveText(durationText(elapsed))
      await assertGeometry()
    }
    await send({ type: 'tool_execution_end', runId: 'long-name', seq: 4, toolCallId: 'long-tool', toolName: name, durationMs: 3_601_000, result: 'Long named tool completed.' })
    await expect(tool).toHaveAttribute('data-state', 'ok')
    await assertGeometry()
    await toggle.click()
    await expect(toggle).toHaveAccessibleName(`收起 ${name}`)
    await expect(tool).toContainText('Long named tool completed.')
    await assertGeometry()
  })

  test('runtime cards expand, hydrate, rotate and resize without frame overlap or gaps', async ({ page }) => {
    const entries: Entry[] = [message('u', '', 'user', 'One human request')]
    const body = '<task-notification>\nTask delegated-review completed.\nResult:\n' +
      'Sanitized implementation report with mixed English 和中文 text, long-path/with-no-boundary/'.repeat(24) +
      '\n</task-notification>'
    for (let i = 0; i < 18; i++) entries.push(message(`n${i}`, entries.at(-1)!.id, 'user', body, `agent:delegated-${i}`))
    entries[18] = { ...entries[18], truncated: true, message: { ...entries[18].message!, content: [{ type: 'text', text: '<task-notification>\nLoading report…' }] } }
    entries.push(message('final', 'n17', 'assistant', 'Completed the same human turn.'))
    const f = await history(page, entries)
    await page.route(`**/v1/sessions/${f.id}?entries=*`, async route => {
      await new Promise(resolve => setTimeout(resolve, 100))
      await route.fulfill({ json: { id: f.id, entries: [message('n17', 'n16', 'user', body, 'agent:delegated-17')] } })
    })
    await f.open()
    await expect(page.getByTestId('turn-divider')).toHaveAttribute('data-turn', '1')
    await startGeometryAudit(page)
    const latest = page.locator('[data-item-key="n17"]')
    await latest.getByTestId('user-bubble-toggle').click()
    await expect(latest).toContainText('Sanitized implementation report')
    await expect(latest.getByTestId('edit-msg')).toHaveCount(0)
    await expect(latest.getByRole('note')).toBeVisible()
    await latest.getByTestId('user-bubble-toggle').click()
    // Simulate keyboard height reduction, then portrait/landscape rotation.
    for (const viewport of [
      { width: layout.width, height: Math.max(360, layout.height - 300) },
      { width: layout.height, height: layout.width },
      { width: layout.width, height: layout.height },
    ]) {
      await page.setViewportSize(viewport)
      await page.waitForTimeout(160)
    }
    await page.getByTestId('chat-scroll').dispatchEvent('wheel', { deltaY: -1 })
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
    for (const ratio of [.8, .6, .4, .2, 0, .25, .5, .75, 1]) {
      await page.getByTestId('chat-scroll').evaluate((el, ratio) => { el.scrollTop = (el.scrollHeight - el.clientHeight) * ratio }, ratio)
      await page.waitForTimeout(100)
    }
    await finishGeometryAudit(page)
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  })

  for (const runtime of [false, true]) test(`fold intent respects latest HUMAN turn (runtime=${runtime})`, async ({ page }) => {
    const entries: Entry[] = []
    for (const turn of ['earlier', 'latest']) {
      entries.push(message(turn, entries.at(-1)?.id ?? '', 'user', `${turn} human request\n${'Reading anchor context.\n'.repeat(8)}`))
      for (let i = 0; i < 3; i++) entries.push(message(`${turn}-a${i}`, entries.at(-1)!.id, 'assistant', `Review step ${i}`))
      if (runtime) entries.push(message(`${turn}-notice`, entries.at(-1)!.id, 'user', 'Delegated review completed.', 'agent:review'))
      entries.push(message(`${turn}-final`, entries.at(-1)!.id, 'assistant', 'Final answer'))
    }
    entries.push({ type: 'run_aborted', id: 'cancel', parentId: entries.at(-1)!.id, details: { reason: 'user_request', runId: 'cancelled' } })
    const f = await history(page, entries, true)
    await f.open()
    const latest = page.locator('[data-fold="latest"]').getByTestId('fold-row-btn')
    await latest.click()
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'following')
    await expect(page.getByTestId('cancel-row')).toBeVisible()
    await expect(page.getByTestId('cancel-row')).toHaveClass('notice')
    await expect(page.getByTestId('cancel-row')).toHaveCSS('border-left-width', '0px')
    await expect(page.getByTestId('cancel-row').locator('.cancel-mark')).toHaveCount(0)
    await latest.click()
    // Reading stays explicit even when a clamp/programmatic scroll reaches
    // the end. A runtime notice must not secretly create a newer turn.
    await page.getByTestId('chat-scroll').dispatchEvent('wheel', { deltaY: -1 })
    await page.getByTestId('chat-scroll').evaluate(el => { el.scrollTop = el.scrollHeight })
    await latest.click()
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
    await page.getByTestId('cancel-row').scrollIntoViewIfNeeded()
    await expect(page.getByTestId('cancel-row')).toBeVisible()
    await page.getByTestId('chat-scroll').evaluate(el => { el.scrollTop = Math.max(0, el.scrollTop - 100) })
    await page.getByTestId('to-bottom').click()
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'following')
    await latest.click()
    await expect(latest).toHaveAttribute('aria-expanded', 'false')
    await page.locator('[data-fold="earlier"]').getByTestId('fold-row-btn').click()
    await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  })
})

test('backward wheel or emulated touch keeps content anchored through a large late row resize', async ({ page, browserName, isMobile }, testInfo) => {
  const touch = browserName === 'webkit' && isMobile
  testInfo.annotations.push({
    type: 'input-profile',
    description: touch ? 'DOM touch events with controlled displacement; no native inertia or physical-device claim' : 'trusted native mouse wheel',
  })
  const entries: Entry[] = [message('resize-u', '', 'user', 'Read the delegated reports')]
  for (let i = 0; i < 20; i++) entries.push(message(`resize-${i}`, entries.at(-1)!.id, 'user', 'Long report line with stable text.\n'.repeat(40), `agent:resize-${i}`))
  entries.push(message('resize-final', entries.at(-1)!.id, 'assistant', 'All reports collected.'))
  const f = await history(page, entries)
  await f.open()
  const scroll = page.getByTestId('chat-scroll')
  if (!touch) await scroll.hover()
  await page.evaluate(touch => {
    const el = document.querySelector<HTMLElement>('[data-testid="chat-scroll"]')!
    const viewport = el.getBoundingClientRect()
    const rows = [...el.querySelectorAll<HTMLElement>('[data-item-key]')]
    const anchor = rows.find(row => row.getBoundingClientRect().bottom > viewport.top)!
    const above = rows.filter(row => row.getBoundingClientRect().bottom < viewport.top - 450).at(-1)
    if (!above) throw new Error('Fixture needs a mounted row above the complete input path')
    const audit = {
      anchor, above, initialOffset: anchor.getBoundingClientRect().top - viewport.top,
      initialScroll: el.scrollTop, writes: 0, worstResidual: 0, peakDebt: 0,
      frames: 0, stop: false, armed: false, missing: false, writeEvents: [] as unknown[],
      badFrames: [] as unknown[],
    }
    ;(window as unknown as { resizeWheelAudit: typeof audit }).resizeWheelAudit = audit
    const original = Element.prototype.scrollTo
    Element.prototype.scrollTo = function (...args: Parameters<Element['scrollTo']>) {
      const before = this.scrollTop
      original.apply(this, args)
      if (this === el) {
        audit.writes += this.scrollTop - before
        if (audit.writeEvents.length < 30) audit.writeEvents.push({ before, after: this.scrollTop, args, time: performance.now() })
      }
    }
    const sample = () => {
      if (audit.stop) { Element.prototype.scrollTo = original; return }
      if (audit.armed) {
        audit.frames++
        if (!anchor.isConnected) audit.missing = true
        const offset = anchor.getBoundingClientRect().top - el.getBoundingClientRect().top
        const nativeMotion = audit.initialScroll - (el.scrollTop - audit.writes)
        const residual = Math.abs(offset - audit.initialOffset - nativeMotion)
        const chat = el.querySelector<HTMLElement>('[data-testid="chat"]')!
        const debt = Number(chat.dataset.deferredOffset ?? 0)
        audit.worstResidual = Math.max(audit.worstResidual, residual)
        audit.peakDebt = Math.max(audit.peakDebt, Math.abs(debt))
        if (residual > 2 && audit.badFrames.length < 30) audit.badFrames.push({
          frame: audit.frames, time: performance.now(), offset, top: el.scrollTop,
          writes: audit.writes, nativeMotion, debt, logical: chat.dataset.scrollOffset,
          height: el.scrollHeight, initialOffset: audit.initialOffset, initialScroll: audit.initialScroll,
        })
      }
      requestAnimationFrame(sample)
    }
    // A late intrinsic layout change above the reader is independent of the
    // transcript reducer (as with an image/Markdown block finishing layout).
    el.addEventListener(touch ? 'touchmove' : 'wheel', () => el.addEventListener('scroll', () => requestAnimationFrame(() => {
      above.style.paddingTop = '1000px'
      requestAnimationFrame(() => { audit.armed = true })
    }), { once: true }), { once: true })
    requestAnimationFrame(sample)
  }, touch)
  if (touch) {
    await scroll.evaluate(el => {
      // Mobile WebKit has no Playwright wheel support or Touch constructor.
      // Keep the finger down through the resize: controlled displacement
      // exercises touch deferral without pretending to generate hardware inertia.
      for (const [type, clientY] of [['touchstart', 100], ['touchmove', 500]] as const) {
        const event = new Event(type, { bubbles: true })
        Object.defineProperty(event, 'touches', { value: [{ identifier: 1, target: el, clientX: 160, clientY }] })
        el.dispatchEvent(event)
      }
      el.scrollTop -= 400
    })
  } else await page.mouse.wheel(0, -400)
  await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'reading')
  await expect.poll(() => page.evaluate(() => (window as unknown as { resizeWheelAudit: { frames: number } }).resizeWheelAudit.frames)).toBeGreaterThan(2)
  if (touch) {
    const held = await page.evaluate(() => {
      const audit = (window as unknown as { resizeWheelAudit: { initialScroll: number; writes: number; worstResidual: number; badFrames: unknown[] } }).resizeWheelAudit
      const scroll = document.querySelector<HTMLElement>('[data-testid="chat-scroll"]')!
      return { displacement: audit.initialScroll - scroll.scrollTop, writes: audit.writes, residual: audit.worstResidual, badFrames: audit.badFrames }
    })
    // A touch correction must be visual while the finger is held, otherwise
    // a real iOS gesture would be canceled by the compensating scroll write.
    expect(held.writes, JSON.stringify(held)).toBe(0)
    expect(Math.abs(held.displacement - 400), JSON.stringify(held)).toBeLessThanOrEqual(2)
    expect(held.residual, JSON.stringify(held)).toBeLessThanOrEqual(2)
    await scroll.evaluate(el => {
      const event = new Event('touchend', { bubbles: true })
      Object.defineProperty(event, 'touches', { value: [] })
      el.dispatchEvent(event)
    })
  }
  await expect(page.getByTestId('chat')).not.toHaveAttribute('data-deferred-offset')
  await expect(page.getByTestId('chat')).toHaveAttribute('data-library-scrolling', 'false')
  const metrics = await page.evaluate(() => {
    const audit = (window as unknown as { resizeWheelAudit: { anchor: HTMLElement; initialOffset: number; worstResidual: number; peakDebt: number; frames: number; stop: boolean; missing: boolean; writeEvents: unknown[]; badFrames: unknown[] } }).resizeWheelAudit
    audit.stop = true
    const scroll = document.querySelector<HTMLElement>('[data-testid="chat-scroll"]')!
    return { movement: audit.anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top - audit.initialOffset, residual: audit.worstResidual, peakDebt: audit.peakDebt, frames: audit.frames, missing: audit.missing, writes: audit.writeEvents, badFrames: audit.badFrames }
  })
  console.log(`resize ${touch ? 'emulated touch' : 'native wheel'} ${browserName}: ${JSON.stringify(metrics)}`)
  expect(metrics.missing).toBe(false)
  expect(metrics.frames).toBeGreaterThan(1)
  expect(Math.abs(metrics.movement - 400)).toBeLessThanOrEqual(2)
  expect(metrics.residual).toBeLessThanOrEqual(2)
  if (browserName === 'webkit' && !touch) expect(metrics.peakDebt).toBeGreaterThanOrEqual(1000)
  if (touch) {
    for (const action of ['new gesture', 'root', 'read', 'latest']) {
      // Already-measured rows isolate positive debt from the negative estimate
      // corrections above. Each exit must transfer/retire that same debt once.
      if (await page.getByTestId('to-bottom').isVisible()) await page.getByTestId('to-bottom').click()
      await expect.poll(() => scroll.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(2)
      await scroll.dispatchEvent('wheel', { deltaY: -1 })
      await scroll.evaluate(el => { el.scrollTop -= 400 })
      await expect(page.getByTestId('chat')).toHaveAttribute('data-library-scrolling', 'false')
      await page.waitForTimeout(180)
      await scroll.evaluate(el => {
        const touch = (type: string, y?: number) => {
          const event = new Event(type, { bubbles: true })
          Object.defineProperty(event, 'touches', { value: y == null ? [] : [{ identifier: 1, target: el, clientX: 160, clientY: y }] })
          el.dispatchEvent(event)
        }
        touch('touchstart', 100)
        touch('touchmove', 140)
        const viewport = el.getBoundingClientRect()
        const rows = [...el.querySelectorAll<HTMLElement>('[data-item-key]')]
        const anchor = rows.find(row => row.getBoundingClientRect().bottom > viewport.top)!
        const above = rows.filter(row => row.getBoundingClientRect().bottom < viewport.top).at(-1)!
        const audit = { anchor, offset: anchor.getBoundingClientRect().top - viewport.top, top: el.scrollTop, drift: 0, frames: 0, stop: false }
        ;(window as unknown as { heldResizeAudit: typeof audit }).heldResizeAudit = audit
        const sample = () => {
          if (audit.stop) return
          audit.frames++
          audit.drift = Math.max(audit.drift, Math.abs(anchor.getBoundingClientRect().top - el.getBoundingClientRect().top - audit.offset))
          requestAnimationFrame(sample)
        }
        requestAnimationFrame(sample)
        above.style.paddingTop = `${parseFloat(above.style.paddingTop || '0') + 1000}px`
      })
      await expect(page.getByTestId('chat')).toHaveAttribute('data-deferred-offset', '1000')
      await expect.poll(() => page.evaluate(() => (window as unknown as { heldResizeAudit: { frames: number } }).heldResizeAudit.frames)).toBeGreaterThan(2)
      const held = await scroll.evaluate(el => {
        const audit = (window as unknown as { heldResizeAudit: { top: number; drift: number; stop: boolean } }).heldResizeAudit
        audit.stop = true
        return { drift: audit.drift, displacement: el.scrollTop - audit.top }
      })
      expect(held.drift, action).toBeLessThanOrEqual(2)
      expect(held.displacement, action).toBe(0)
      // Audit the debt transfer after ResizeObserver has consumed our raw
      // style injection. rAF precedes RO in a rendering step, so inspecting
      // that unmeasured mutation is not evidence of a painted overlap.
      await startGeometryAudit(page)
      if (action === 'new gesture' || action === 'root') {
        await scroll.evaluate((el, action) => {
          const touch = (type: string, y?: number) => {
            const event = new Event(type, { bubbles: true })
            Object.defineProperty(event, 'touches', { value: y == null ? [] : [{ identifier: 1, target: el, clientX: 160, clientY: y }] })
            el.dispatchEvent(event)
          }
          if (action === 'root') el.scrollTop = 0
          touch('touchend')
          if (action === 'new gesture') { touch('touchstart', 100); touch('touchend') }
        }, action)
        if (action === 'root') await expect(page.locator('[data-item-key="resize-u"]')).toBeVisible()
        else {
          const drift = await scroll.evaluate(el => {
            const audit = (window as unknown as { heldResizeAudit: { anchor: HTMLElement; offset: number } }).heldResizeAudit
            return Math.abs(audit.anchor.getBoundingClientRect().top - el.getBoundingClientRect().top - audit.offset)
          })
          expect(drift).toBeLessThanOrEqual(2)
        }
      } else if (action === 'read') {
        await page.getByTestId('request-nav-toggle').click()
        await page.getByTestId('request-nav-item').filter({ hasText: 'Read the delegated reports' }).click()
        await expect(page.locator('[data-item-key="resize-u"]')).toBeVisible()
      } else {
        await page.getByTestId('to-bottom').click()
        await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', 'following')
      }
      await expect(page.getByTestId('chat')).not.toHaveAttribute('data-deferred-offset')
      if (action !== 'new gesture' && action !== 'root') await scroll.dispatchEvent('touchend', { touches: [] })
      await page.waitForTimeout(220)
      await expect(page.getByTestId('chat')).not.toHaveAttribute('data-deferred-offset')
      await finishGeometryAudit(page)
    }
  }
})
