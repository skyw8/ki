import { expect, test, type Page } from '@playwright/test'
import { loadHistory, requestTitle, userRequests } from '../src/lib/model.ts'
import type { ChatNode, Entry, IndexEntry } from '../src/api/types.ts'
import { serverToken } from './global-setup'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test('requestTitle prefers the first line then attachment names', () => {
  expect(requestTitle('  hello world  \nsecond')).toBe('hello world')
  expect(requestTitle('', [{ type: 'image', name: 'shot.png' }])).toBe('shot.png')
  expect(requestTitle('', [{ type: 'file', path: '/tmp/notes.md' }])).toBe('/tmp/notes.md')
  expect(requestTitle('   ')).toBe('')
})

test('userRequests walks the active path and drops optimistic duplicates', () => {
  const nodes: ChatNode[] = [
    { kind: 'user', id: 'opt-user-1', text: 'same turn', content: [] },
    { kind: 'user', id: 'u1', text: 'same turn', content: [] },
    { kind: 'assistant', id: 'a1', text: 'ok' },
    { kind: 'user', id: 'u2', text: 'next\nline', content: [] },
    { kind: 'tool', id: 't1', name: 'Bash' },
  ]
  const entries: Entry[] = [
    { type: 'message', id: 'u1', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'same turn' }] } },
    { type: 'message', id: 'a1', parentId: 'u1', message: { role: 'assistant', content: [{ type: 'text', text: 'ok' }] } },
    { type: 'message', id: 'u2', parentId: 'a1', message: { role: 'user', content: [{ type: 'text', text: 'next\nline' }] } },
  ]
  expect(userRequests(entries, 'u2', nodes)).toEqual([
    { id: 'u1', title: 'same turn' },
    { id: 'u2', title: 'next' },
  ])
})

test('userRequests covers the whole branch from index rows and keeps a pending prompt', () => {
  // The conversation loaded only the newest prompt; the two older ones are
  // body-less index rows (a preview, no message body).
  const index: IndexEntry[] = [
    { type: 'message', id: 'u1', parentId: '', role: 'user', preview: 'first ask' },
    { type: 'message', id: 'a1', parentId: 'u1', role: 'assistant', preview: 'ok' },
    { type: 'message', id: 'u2', parentId: 'a1', role: 'user', preview: 'second ask' },
    { type: 'message', id: 'a2', parentId: 'u2', role: 'assistant', preview: 'ok' },
  ]
  const entries: Entry[] = [
    { type: 'message', id: 'u3', parentId: 'a2', message: { role: 'user', content: [{ type: 'text', text: 'third ask' }] } },
  ]
  const view = loadHistory({
    id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId: 'u3', entries, index,
  })
  const pending: ChatNode[] = [{ kind: 'user', id: 'opt-user-9', text: 'just typed', content: [] }]

  expect(userRequests(view.allEntries, view.leafId, pending)).toEqual([
    { id: 'u1', title: 'first ask' },
    { id: 'u2', title: 'second ask' },
    { id: 'u3', title: 'third ask' },
    { id: 'opt-user-9', title: 'just typed' },
  ])
})

test('userRequests lists human prompts only', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'typed here' }] } },
    {
      type: 'message', id: 'u2', parentId: 'u1',
      // The Agent tool's completion notification: a user turn the runtime wrote.
      message: { role: 'user', origin: 'agent:task-1', content: [{ type: 'text', text: '<task-notification>…' }] },
    },
    {
      type: 'message', id: 'u3', parentId: 'u2',
      // A subagent's own directive, again runtime-written.
      message: { role: 'user', origin: 'agent', content: [{ type: 'text', text: 'You are a subagent…' }] },
    },
    {
      type: 'message', id: 'u4', parentId: 'u3',
      // Extension-relayed input is still a person's turn.
      message: { role: 'user', origin: 'extension:telegram-bot', content: [{ type: 'text', text: '[Telegram] hi' }] },
    },
  ]
  expect(userRequests(entries, 'u4').map(item => item.id)).toEqual(['u1', 'u4'])
})

test('request navigator jumps to an earlier user turn', async ({ page }) => {
  test.setTimeout(45_000)
  const prompts = ['nav-alpha-unique', 'nav-beta-unique', 'nav-gamma-unique', 'nav-delta-unique', 'nav-epsilon-unique']
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  for (const prompt of prompts) {
    await sendPrompt(page, prompt)
    await expect(page.getByTestId('assistant-message').last()).toContainText('ok')
  }

  const toggle = page.getByTestId('request-nav-toggle')
  await expect(toggle).toBeVisible()
  await toggle.click()
  const panel = page.getByTestId('request-nav-panel')
  await expect(panel).toBeVisible()
  await expect(panel.getByTestId('request-nav-item')).toHaveCount(prompts.length)
  for (const prompt of prompts) {
    await expect(panel.getByTestId('request-nav-item').filter({ hasText: prompt })).toBeVisible()
  }

  const firstBubble = page.getByTestId('user-bubble').first()
  const before = await firstBubble.evaluate(el => {
    const scroll = el.closest('[data-testid="chat-scroll"]') as HTMLElement
    return el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })
  expect(before).toBeLessThan(-8)

  await panel.getByTestId('request-nav-item').filter({ hasText: prompts[0] }).click()
  await expect.poll(async () => firstBubble.evaluate(el => {
    const scroll = el.closest('[data-testid="chat-scroll"]') as HTMLElement
    return el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })).toBeLessThan(96)
  await expect.poll(async () => firstBubble.evaluate(el => {
    const scroll = el.closest('[data-testid="chat-scroll"]') as HTMLElement
    return el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })).toBeGreaterThanOrEqual(-1)
  await expect(page.getByTestId('request-nav-item').filter({ hasText: prompts[0] })).toHaveClass(/active/)
})

for (const mobile of [false, true]) test(`request navigation lands once and tracks actual reading on ${mobile ? 'touch' : 'desktop'}`, async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, storageState: test.info().project.use.storageState,
    viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 900 }, isMobile: mobile && test.info().project.use.browserName !== 'firefox', hasTouch: mobile,
  })
  const page = await context.newPage()
  try {
    const headers = { Authorization: `Bearer ${serverToken()}` }
    const response = await page.request.post('/v1/sessions', { headers, data: {} })
    const { id } = await response.json()
    const title = `navigation-${id}`
    await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
    const entries: Entry[] = Array.from({ length: 45 }, (_, i) => ({ type: 'message', id: `u${i}`, parentId: i ? `u${i - 1}` : '',
      message: { role: 'user', content: [{ type: 'text', text: `Request ${String(i).padStart(2, '0')}\n` + (i < 38 ? 'Variable height content wrapping on a narrow phone. '.repeat(i % 7 + 1) : 'Short') }] },
    }))
    await page.route(`**/v1/sessions/${id}**`, route => {
      const params = new URL(route.request().url()).searchParams
      if (params.get('fields') === 'index') return route.fulfill({ json: { id, index: entries.map(e => ({ id: e.id, type: e.type, parentId: e.parentId, role: 'user', preview: e.message?.content?.[0].text })) } })
      if (route.request().method() !== 'GET') return route.fallback()
      return route.fulfill({ json: { id, title, leafId: 'u44', entries, running: false } })
    })
    await page.goto('/')
    if (mobile) await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: title }).click()
    const toggle = page.getByTestId('request-nav-toggle')
    const select = async (n: number) => {
      if (!await page.getByTestId('request-nav-panel').isVisible()) await toggle.click()
      await page.getByTestId('request-nav-filter').fill(`Request ${String(n).padStart(2, '0')}`)
      await page.locator(`[data-request-id="u${n}"]`).click()
    }
    const offset = (n: number) => page.locator(`[data-item-key="u${n}"]`).evaluate(el => el.getBoundingClientRect().top - el.closest('[data-testid="chat-scroll"]')!.getBoundingClientRect().top)
    const landingError = (n: number) => page.locator(`[data-item-key="u${n}"]`).evaluate(el => {
      const scroll = el.closest('[data-testid="chat-scroll"]')!
      const offset = el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
      // A short final turn cannot reach the top; it must be fully visible at
      // the reachable end and remain selected, not select a later sibling.
      return Math.abs(Math.min(offset, scroll.scrollHeight - scroll.clientHeight - scroll.scrollTop))
    })
    for (const n of [8, 31, 0, 40]) {
      await select(n)
      await expect.poll(() => landingError(n)).toBeLessThanOrEqual(2)
      expect(await offset(n)).toBeGreaterThanOrEqual(-2)
      if (mobile) await toggle.click()
      await expect(page.locator(`[data-request-id="u${n}"]`)).toHaveAttribute('aria-selected', 'true')
      // A late measurement/selection must not steal the landing after one tap.
      await page.waitForTimeout(250)
      expect(await landingError(n)).toBeLessThanOrEqual(2)
    }
    await page.keyboard.press('Escape')
    const scroll = page.getByTestId('chat-scroll')
    await scroll.evaluate(el => {
      el.dispatchEvent(new WheelEvent('wheel', { deltaY: -1, bubbles: true }))
      const row = el.querySelector('[data-item-key="u37"]')!
      el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top + 1
    })
    await toggle.click()
    await page.getByTestId('request-nav-filter').fill('Request 37')
    await expect(page.locator('[data-request-id="u37"]')).toHaveAttribute('aria-selected', 'true')
  } finally { await context.close() }
})


async function navigationFixture(page: Page) {
  await page.addInitScript(() => localStorage.setItem("ki-message-view", "detailed"))
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post("/v1/sessions", { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `navigation-recovery-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  const entries: Entry[] = []
  for (let n = 0; n < 4; n++) {
    entries.push({ type: "message", id: `u${n}`, parentId: n ? `a${n - 1}` : "", message: { role: "user", content: [{ type: "text", text: `Recovery request ${n}` }] } })
    entries.push({ type: "message", id: `a${n}`, parentId: `u${n}`, message: { role: "assistant", content: [{ type: "text", text: `Recovery response ${n}` }] } })
  }
  const index = (rows: Entry[]) => rows.map(e => ({ type: e.type, id: e.id, parentId: e.parentId, role: e.message?.role, preview: e.message?.content?.[0].text }))
  const open = async () => {
    await page.goto("/")
    await page.getByTestId("session-row").filter({ hasText: title }).click()
    await expect(page.getByTestId("assistant-message").last()).toBeVisible()
  }
  const navigator = async () => {
    if (!await page.getByTestId("request-nav-panel").isVisible()) {
      // Keyboard activation avoids a hover-open issuing an extra request before
      // the test can observe the first failed metadata response.
      await page.getByTestId("request-nav-toggle").focus()
      await page.keyboard.press("Enter")
    }
  }
  const jump = async (n: number) => {
    await navigator()
    await page.locator(`[data-request-id="u${n}"]`).click()
  }
  const landed = async (n: number) => {
    const row = page.locator(`[data-item-key="u${n}"]`)
    await expect(row).toBeVisible()
    await expect.poll(() => row.evaluate(el => {
      const scroll = el.closest("[data-testid=chat-scroll]")!
      const top = el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
      return Math.abs(Math.min(top, scroll.scrollHeight - scroll.clientHeight - scroll.scrollTop))
    })).toBeLessThanOrEqual(2)
  }
  return { id, title, entries, index, open, navigator, jump, landed }
}

test("reply-only history exposes lazy navigation and explicit index and jump retries", async ({ page }) => {
  const f = await navigationFixture(page)
  let indexes = 0
  let pages = 0
  await page.route(url => url.pathname === `/v1/sessions/${f.id}`, async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.get("fields") === "index") {
      if (++indexes === 1) return route.fulfill({ status: 503, body: "temporary index failure" })
      return route.fulfill({ json: { id: f.id, index: f.index(f.entries.slice(0, 4)) } })
    }
    if (params.has("before")) {
      expect(params.get("before")).toBe("a1")
      if (++pages === 1) return route.fulfill({ status: 503, body: "temporary paging failure" })
      return route.fulfill({ json: { entries: f.entries.slice(0, 3), oldestId: "u0", hasMore: false } })
    }
    return route.fulfill({ json: { id: f.id, title: f.title, leafId: "a1", entries: [f.entries[3]], oldestId: "a1", hasMore: true } })
  })
  await f.open()
  await expect(page.getByTestId("user-bubble")).toHaveCount(0)
  await expect(page.getByTestId("request-nav-toggle")).toBeVisible()
  await f.navigator()
  await expect(page.getByTestId("request-nav-retry")).toBeVisible()
  await page.getByTestId("request-nav-retry").click()
  await expect(page.getByTestId("request-nav-item")).toHaveCount(2)
  await f.jump(0)
  await expect(page.getByTestId("retry-jump")).toBeVisible()
  await page.getByTestId("retry-jump").click()
  await f.landed(0)
  await expect(page.getByTestId("retry-jump")).toHaveCount(0)
  expect(pages).toBe(2)
})

for (const delayedPage of [false, true]) test(`resume refreshes the full request index and ${delayedPage ? "rejects an old page completion" : "reopens an exhausted paging boundary"}`, async ({ page }) => {
  const f = await navigationFixture(page)
  let resumed = false
  let indexCalls = 0
  const cursors: string[] = []
  let releaseOld!: () => void
  const oldPage = new Promise<void>(resolve => { releaseOld = resolve })
  await page.route(url => url.pathname === `/v1/sessions/${f.id}`, async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.get("fields") === "index") {
      indexCalls++
      return route.fulfill({ json: { id: f.id, index: f.index(resumed ? f.entries : f.entries.slice(0, 4)) } })
    }
    const before = params.get("before")
    if (before) {
      cursors.push(before)
      if (before === "u1") {
        if (delayedPage) await oldPage
        return route.fulfill({ json: { entries: f.entries.slice(0, 2), oldestId: "u0", hasMore: false } })
      }
      if (before === "u3") return route.fulfill({ json: { entries: f.entries.slice(4, 6), oldestId: "u2", hasMore: true } })
      if (before === "u2") return route.fulfill({ json: { entries: f.entries.slice(0, 4), oldestId: "u0", hasMore: false } })
      throw new Error(`Unexpected cursor: ${before}`)
    }
    return route.fulfill({ json: { id: f.id, title: f.title, leafId: resumed ? "a3" : "a1", entries: f.entries.slice(resumed ? 6 : 2, resumed ? 8 : 4), oldestId: resumed ? "u3" : "u1", hasMore: true } })
  })
  await f.open()
  await f.navigator()
  await expect(page.getByTestId("request-nav-item")).toHaveCount(2)
  await f.jump(0)
  await expect.poll(() => cursors).toContain("u1")
  if (!delayedPage) await f.landed(0)
  const indexesBeforeResume = indexCalls
  resumed = true
  await page.keyboard.press("Escape")
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
  await expect(page.getByTestId("assistant-message").last()).toContainText("Recovery response 3")
  await expect.poll(() => indexCalls).toBeGreaterThan(indexesBeforeResume)
  if (delayedPage) {
    // Recovery must retire the old request even while its remote reply hangs.
    await expect(page.getByTestId("cancel-jump")).toHaveCount(0)
    releaseOld()
  }
  await f.navigator()
  await expect(page.getByTestId("request-nav-item")).toHaveCount(4)
  await f.jump(2)
  await f.landed(2)
  expect(cursors).toContain("u3")
  await f.jump(0)
  await f.landed(0)
  expect(cursors).toContain("u2")
})


test("a rejected history cursor refreshes its boundary and offers a working retry", async ({ page }) => {
  const f = await navigationFixture(page)
  let rejected = false
  const cursors: string[] = []
  await page.route(url => url.pathname === `/v1/sessions/${f.id}`, route => {
    const params = new URL(route.request().url()).searchParams
    if (params.get("fields") === "index") return route.fulfill({ json: { id: f.id, index: f.index(f.entries.slice(0, 4)) } })
    const before = params.get("before")
    if (before) {
      cursors.push(before)
      if (before === "obsolete") {
        rejected = true
        return route.fulfill({ status: 409, body: "history cursor is not on the current branch" })
      }
      expect(before).toBe("u1")
      return route.fulfill({ json: { entries: f.entries.slice(0, 2), oldestId: "u0", hasMore: false } })
    }
    return route.fulfill({ json: { id: f.id, title: f.title, leafId: "a1", entries: f.entries.slice(2, 4), oldestId: rejected ? "u1" : "obsolete", hasMore: true } })
  })
  await f.open()
  await f.navigator()
  await expect(page.getByTestId("request-nav-item")).toHaveCount(2)
  await f.jump(0)
  await expect(page.getByTestId("retry-jump")).toBeVisible()
  await page.getByTestId("retry-jump").click()
  await f.landed(0)
  expect(cursors).toEqual(["obsolete", "u1"])
})

test("resume supersedes a request index snapshot still in flight", async ({ page }) => {
  const f = await navigationFixture(page)
  let resumed = false
  let indexCalls = 0
  let releaseIndex!: () => void
  const firstIndex = new Promise<void>(resolve => { releaseIndex = resolve })
  await page.route(url => url.pathname === `/v1/sessions/${f.id}`, async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.get("fields") === "index") {
      const rows = resumed ? f.entries : f.entries.slice(0, 4)
      if (++indexCalls === 1) await firstIndex
      return route.fulfill({ json: { id: f.id, index: f.index(rows) } })
    }
    return route.fulfill({ json: { id: f.id, title: f.title, leafId: resumed ? "a3" : "a1", entries: f.entries.slice(resumed ? 6 : 2, resumed ? 8 : 4), oldestId: resumed ? "u3" : "u1", hasMore: true } })
  })
  await f.open()
  await f.navigator()
  await expect(page.getByTestId("request-nav-loading")).toBeVisible()
  resumed = true
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")))
  await expect(page.getByTestId("assistant-message").last()).toContainText("Recovery response 3")
  releaseIndex()
  await expect(page.getByTestId("request-nav-item")).toHaveCount(4)
  await expect(page.getByTestId("request-nav-loading")).toHaveCount(0)
  expect(indexCalls).toBe(2)
})
