import { expect, test, type Page, type Route } from '@playwright/test'
import type { Entry, LoopEvent, SessionDetail, SessionInfo } from '../src/api/types'
import { serverToken } from './global-setup'

// These are state-machine races, not a Cartesian product of device sizes.
// Exercise every lifecycle on a touch client and retain a desktop control.
const profiles = {
  desktop: { width: 1440, height: 900, touch: false },
  phone: { width: 390, height: 844, touch: true },
  tablet: { width: 834, height: 1112, touch: true },
}
type Profile = keyof typeof profiles
type Stream = { id: string; cursor: string | null; aborted: boolean; closed: boolean }
type HeldRead = { id: string; fields: string; released: boolean }
type Fixture = {
  details: Record<string, SessionDetail>
  sessions: SessionInfo[]
  streams: Stream[]
  reads: Array<{ id: string; fields: string }>
  holds: HeldRead[]
  nextHold?: { id: string; fields: string }
  passGet?: string
  projectionFailures: number
  projectionReads: number
  projection?: SessionDetail
  bodies?: Entry[]
  bodyFailures: number
  bodyReads: string[]
  accepted: boolean | string
  prompts: Array<{ id: string; clientRequestId?: string; queueId?: string; content?: unknown[] }>
  replay: Record<string, LoopEvent[]>
  send: (id: string, event: LoopEvent & { scope?: string }) => void
  close: (id: string) => void
  release: (index: number, detail: SessionDetail) => void
}
declare global {
  interface Window { sessionStateFixture: Fixture }
}

function profile(name: Profile) {
  const value = profiles[name]
  test.use({
    viewport: { width: value.width, height: value.height },
    hasTouch: value.touch,
    // Firefox has touch/coarse-pointer support but not mobile layout
    // emulation. The same assertions run; no engine or test is skipped.
    isMobile: async ({ browserName }, use) => { await use(value.touch && browserName !== 'firefox') },
  })
  test.beforeEach(async ({ browserName }, info) => {
    info.annotations.push({
      type: 'viewport-profile',
      description: `${name} ${value.width}×${value.height}; ${value.touch ? 'touch/coarse pointer' : 'mouse'}; isMobile=${value.touch && browserName !== 'firefox'}; default UA; not a physical device`,
    })
  })
}

function user(id: string, text: string, parentId?: string): Entry {
  return { type: 'message', id, parentId,
    message: { role: 'user', clientRequestId: `request-${id}`, content: [{ type: 'text', text }] } }
}

function snapshot(session: SessionInfo, text: string, running = false): SessionDetail {
  const entry = user(`${session.id}-input`, text)
  return { ...session, entries: [entry], leafId: entry.id, oldestId: entry.id,
    hasMore: false, running, runtime: { ready: true }, queued: [] }
}

async function createSession(page: Page, label: string): Promise<SessionInfo> {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(response.ok()).toBe(true)
  const session = await response.json() as SessionInfo
  const title = `session-state-${label}-${session.id}`
  const renamed = await page.request.patch(`/v1/sessions/${session.id}`, { headers, data: { title } })
  expect(renamed.ok()).toBe(true)
  return { ...session, title, running: false }
}

/**
 * Auth, the SPA, catalogs and workspace membership come from the isolated real
 * fake server. Only this test's session reads/acknowledgements and SSE transport
 * are deterministic. Held GETs intentionally ignore abort: cancellation alone
 * must not be the authority check for responses already delivered by a proxy.
 */
async function install(page: Page, sessions: SessionInfo[], details: SessionDetail[], lang = 'en', mode = 'detailed') {
  await page.addInitScript(({ sessions, details, lang, mode }) => {
    localStorage.setItem('ki-lang', lang)
    localStorage.setItem('ki-message-view', mode)
    localStorage.setItem('ki-message-view-keep', '1')
    const pending = new Map<number, (detail: SessionDetail) => void>()
    const controllers = new Map<number, ReadableStreamDefaultController<Uint8Array>>()
    const state: Fixture = window.sessionStateFixture = {
      sessions, details: Object.fromEntries(details.map(detail => [detail.id, detail])),
      streams: [], reads: [], holds: [], prompts: [], accepted: true, replay: {},
      projectionFailures: 0, projectionReads: 0,
      bodyFailures: 0, bodyReads: [],
      send(id, event) {
        const index = state.streams.findLastIndex(stream => stream.id === id && !stream.aborted && !stream.closed)
        if (index < 0) throw new Error(`No live stream for ${id}`)
        controllers.get(index)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
      },
      close(id) {
        const index = state.streams.findLastIndex(stream => stream.id === id && !stream.aborted && !stream.closed)
        if (index < 0) throw new Error(`No live stream for ${id}`)
        state.streams[index].closed = true
        controllers.get(index)!.close()
      },
      release(index, detail) {
        const resolve = pending.get(index)
        if (!resolve) throw new Error(`No held GET ${index}`)
        state.holds[index].released = true
        pending.delete(index)
        resolve(detail)
      },
    }
    const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
    const original = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href)
      const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
      const match = url.pathname.match(/^\/v1\/sessions\/([^/]+)(?:\/(events|prompt))?$/)
      if (url.pathname === '/v1/events' || (match?.[2] === 'events' && state.details[match[1]])) {
        const id = match?.[1] ?? 'push'
        const cursor = new Headers(init?.headers).get('Last-Event-ID')
        const index = state.streams.length
        const record = { id, cursor, aborted: false, closed: false }
        state.streams.push(record)
        if (id !== 'push') sessionStorage.setItem('session-state-readers', String(Number(sessionStorage.getItem('session-state-readers') ?? 0) + 1))
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controllers.set(index, controller)
            const abort = () => {
              record.aborted = true
              if (!record.closed) { record.closed = true; controller.close() }
            }
            if (init?.signal?.aborted) abort()
            else init?.signal?.addEventListener('abort', abort, { once: true })
            // Model the server's ACK rule: an acknowledged event is not sent
            // again, even if a client discarded the state it acknowledged.
            for (const event of state.replay[id] ?? []) {
              const acknowledged = cursor?.startsWith(`${event.runId}:`)
                ? Number(cursor.slice(cursor.lastIndexOf(':') + 1)) : 0
              if (!record.closed && (event.seq ?? 0) > acknowledged) {
                controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
              }
            }
          },
        })
        return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (url.pathname === '/v1/sessions' && method === 'GET') return json(state.sessions)
      if (match && state.details[match[1]]) {
        const id = match[1]
        if (match[2] === 'prompt' && method === 'POST') {
          const request = JSON.parse(String(init?.body)) as Fixture['prompts'][number]
          state.prompts.push({ ...request, id })
          return json({ accepted: state.accepted, clientRequestId: request.clientRequestId })
        }
        if (!match[2] && method === 'GET' && state.passGet !== id) {
          const fields = url.searchParams.get('fields') ?? ''
          state.reads.push({ id, fields })
          if (url.searchParams.has('entries') && state.bodies) {
            state.bodyReads.push(url.searchParams.get('entries')!)
            if (state.bodyFailures-- > 0) return new Response('Temporary body error', { status: 503 })
            return json({ entries: state.bodies })
          }
          if (url.searchParams.has('turn') && url.searchParams.get('view') === 'compact') {
            state.projectionReads++
            if (state.projectionFailures > 0) {
              state.projectionFailures--
              return new Response('Temporary projection error', { status: 503 })
            }
            if (state.projection) return json(state.projection)
          }
          if (state.nextHold?.id === id && state.nextHold.fields === fields) {
            state.nextHold = undefined
            const index = state.holds.length
            state.holds.push({ id, fields, released: false })
            return json(await new Promise<SessionDetail>(resolve => pending.set(index, resolve)))
          }
          return json(state.details[id])
        }
      }
      return original(input, init)
    }
  }, { sessions, details, lang, mode })
}

async function sidebar(page: Page) {
  // isVisible() does not wait: during auth bootstrap the missing mobile
  // toggle otherwise looks like a desktop layout, leaving its rows hidden.
  await expect(page.locator('main.main')).toBeVisible()
  const toggle = page.getByTestId('mobile-nav-toggle')
  if (await toggle.isVisible() && await toggle.getAttribute('aria-expanded') !== 'true') await toggle.click()
}

async function open(page: Page, session: SessionInfo) {
  await sidebar(page)
  await page.getByTestId('session-row').filter({ hasText: session.title }).locator('.session-main').click()
  const toggle = page.getByTestId('mobile-nav-toggle')
  if (await toggle.isVisible()) await expect(toggle).toHaveAttribute('aria-expanded', 'false')
}

async function hold(page: Page, id: string, fields = '') {
  const index = await page.evaluate(({ id, fields }) => {
    const fixture = window.sessionStateFixture
    fixture.nextHold = { id, fields }
    return fixture.holds.length
  }, { id, fields })
  return {
    started: () => expect.poll(() => page.evaluate(index => !!window.sessionStateFixture.holds[index], index)).toBe(true),
    release: (detail: SessionDetail) => page.evaluate(({ index, detail }) => window.sessionStateFixture.release(index, detail), { index, detail }),
  }
}

async function drain(page: Page) {
  // Let fetch/json promise continuations, the stream batch deadline and React
  // paint all run before negative assertions; this is not network polling.
  await page.waitForTimeout(200)
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
}

async function runStreams(page: Page, id: string) {
  return page.evaluate(id => window.sessionStateFixture.streams.filter(stream => stream.id === id), id)
}

for (const [layout, lang] of [['desktop', 'en'], ['phone', 'zh']] as const) {
  test.describe(`compaction body recovery ${layout}`, () => {
    profile(layout)
    test('an index-only outcome can be loaded and retried without duplicating its status', async ({ page }) => {
      const session = await createSession(page, 'compact-body')
      const u = user('u', 'Before compaction')
      const start: Entry = { type: 'compaction_start', id: 'start', parentId: u.id, details: { reason: 'manual', ok: false } }
      const end: Entry = { type: 'compaction_end', id: 'end', parentId: start.id, details: { reason: 'manual', status: 'failed', ok: false } }
      await install(page, [session], [{ ...session, leafId: end.id, oldestId: u.id, hasMore: false,
        runtime: { ready: true }, entries: [u, start], index: [u, start, end].map(({ id, parentId, type, message }) => ({ id, parentId, type, role: message?.role })) }], lang)
      await page.goto('/')
      await page.evaluate(end => {
        window.sessionStateFixture.bodies = [end]
        window.sessionStateFixture.bodyFailures = 1
      }, end)
      await open(page, session)
      const button = page.getByTestId('compact-btn')
      await expect(button).toContainText(lang === 'zh' ? '压缩状态待加载' : 'Compaction status not loaded')
      await button.click()
      await expect(button).toContainText(/重试|Retry/)
      expect(await page.evaluate(() => window.sessionStateFixture.bodyReads)).toEqual(['end'])
      await button.click()
      await expect(button).toHaveClass(/failed/)
      await expect(button).toContainText(lang === 'zh' ? '上下文压缩失败' : 'Context compaction failed')
      expect(await page.evaluate(() => window.sessionStateFixture.bodyReads)).toEqual(['end', 'end'])
      await expect(page.getByTestId('compact-row')).toHaveCount(1)
    })
  })
}

for (const action of ['queued-send', 'queued-steer'] as const) {
  test.describe(`late ${action} metadata`, () => {
    profile(action === 'queued-send' ? 'desktop' : 'phone')
    for (const target of ['different-session', 'same-ID-reopened-branch'] as const) {
      test(`cannot mutate ${target}`, async ({ page }) => {
        const a = await createSession(page, 'source')
        const b = target === 'different-session' ? await createSession(page, 'destination') : a
        const original = snapshot(a, 'Original branch', true)
        if (action === 'queued-steer') {
          original.queued = [{ id: 'queued-original', clientRequestId: 'queued-request-original',
            content: [{ type: 'text', text: 'Pending original input' }] }]
        }
        const destination = snapshot(b, 'Destination branch is idle')
        if (b.id === a.id) {
          destination.entries = [user(`${a.id}-selected-branch`, 'Destination branch is idle')]
          destination.oldestId = destination.leafId = destination.entries[0].id
        }
        await install(page, b.id === a.id ? [a] : [a, b], b.id === a.id ? [original] : [original, destination])
        await page.goto('/')
        await open(page, a)
        await expect(page.getByTestId('composer-stop')).toBeVisible()
        await expect.poll(() => runStreams(page, a.id).then(streams => streams.length)).toBe(1)
        const delayed = await hold(page, a.id, 'runtime')
        await page.evaluate(() => { window.sessionStateFixture.accepted = 'queued' })
        if (action === 'queued-send') {
          await page.getByTestId('composer-input').fill('A newly queued input')
          await page.getByTestId('composer-send').click()
        } else {
          await page.getByTestId('queued-steer').click()
        }
        await delayed.started()
        expect(await page.evaluate(() => window.sessionStateFixture.prompts[0].clientRequestId)).toBeTruthy()
        await page.evaluate(detail => { window.sessionStateFixture.details[detail.id] = detail }, destination)
        await open(page, b)
        await expect(page.getByTestId('user-bubble')).toHaveText('Destination branch is idle')
        const count = (await runStreams(page, b.id)).length
        await delayed.release({ ...original, running: true, queued: [{ id: 'poison', clientRequestId: 'poison-request',
          content: [{ type: 'text', text: 'STALE QUEUE MUST NEVER ARRIVE' }] }] })
        await drain(page)
        await expect(page.getByTestId('user-bubble')).toHaveText('Destination branch is idle')
        await expect(page.getByTestId('queued-list')).toHaveCount(0)
        await expect(page.getByTestId('composer-stop')).toHaveCount(0)
        await page.getByTestId('composer-input').fill('Still available')
        await expect(page.getByTestId('composer-send')).toBeEnabled()
        expect((await runStreams(page, b.id)).length).toBe(count)
      })
    }
  })
}

test.describe('opening lifecycle', () => {
  profile('phone')

  test('a delayed initial snapshot cannot reopen a reader after pagehide', async ({ page }) => {
    const session = await createSession(page, 'suspended-opening')
    const initial = snapshot(session, 'Opening snapshot', true)
    await install(page, [session], [initial])
    await page.goto('/')
    const delayed = await hold(page, session.id)
    await open(page, session)
    await delayed.started()
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
    await delayed.release(initial)
    await drain(page)
    await expect(page.getByTestId('user-bubble')).toHaveCount(0)
    expect(await runStreams(page, session.id)).toHaveLength(0)
    expect(await page.evaluate(() => window.sessionStateFixture.streams.filter(stream => stream.id === 'push' && !stream.aborted))).toHaveLength(0)
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
    await expect.poll(() => runStreams(page, session.id).then(streams => streams.length)).toBe(1)
    await expect(page.getByTestId('user-bubble')).toHaveText('Opening snapshot')
    await expect(page.getByTestId('composer-stop')).toBeVisible()
    expect(await page.evaluate(() => window.sessionStateFixture.prompts)).toEqual([])
  })

  test('unloading a document with an initial GET pending cannot create a late reader', async ({ page }) => {
    const session = await createSession(page, 'unloaded-opening')
    const initial = snapshot(session, 'Must not open after unload', true)
    await install(page, [session], [initial])
    let opening: Route | undefined
    await page.route(url => url.pathname === `/v1/sessions/${session.id}` && !url.search, route => { opening = route })
    await page.goto('/')
    await page.evaluate(id => { window.sessionStateFixture.passGet = id }, session.id)
    await open(page, session)
    await expect.poll(() => !!opening).toBe(true)
    expect(await runStreams(page, session.id)).toHaveLength(0)
    // A real document unload, not a Fiber/devtools hook. React-only dispose
    // authority is separately covered by the controller's unit regressions.
    await page.goto('/v1/health')
    await opening!.fulfill({ json: initial }).catch(() => { /* The browser may already have canceled the old transport. */ })
    await drain(page)
    expect(await page.evaluate(() => Number(sessionStorage.getItem('session-state-readers') ?? 0))).toBe(0)
    await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  })
})

test.describe('reader authority', () => {
  profile('tablet')

  test('an exhausted root still offers retry after compact conversion fails', async ({ page }) => {
    const session = await createSession(page, 'root-conversion')
    const initial = snapshot(session, 'Root input')
    initial.entries!.push({ type: 'message', id: 'root-answer', parentId: initial.leafId,
      message: { role: 'assistant', content: [{ type: 'text', text: 'Root answer survives failed projection' }] } })
    initial.leafId = 'root-answer'
    await install(page, [session], [initial], 'en', 'compact')
    await page.goto('/')
    await page.evaluate(({ initial }) => {
      const f = window.sessionStateFixture
      f.projectionFailures = 1
      f.projection = { ...initial, compactTurns: [{
        id: initial.oldestId!, tailId: initial.leafId!, entryIds: initial.entries!.map(entry => entry.id),
        visibleNodeIds: initial.entries!.map(entry => entry.id), hiddenCount: 0, stepCount: 1,
        stats: { turn: 1, steps: 1, tools: 0, toolFailures: 0, elapsedMs: 0, durationMs: 0, input: 0, output: 0,
          cacheRead: 0, cacheWrite: 0, cost: 0, cacheMisses: 0, hasCost: false, ttftMs: 0, tps: null, live: false },
      }] }
    }, { initial })
    await open(page, session)
    await expect(page.getByTestId('assistant-message')).toHaveText('Root answer survives failed projection')
    await expect(page.getByTestId('load-older')).toBeVisible()
    await expect(page.getByTestId('load-older')).toContainText('Retry')
    expect(await page.evaluate(() => window.sessionStateFixture.projectionReads)).toBe(1)
    await page.getByTestId('load-older').click()
    await expect.poll(() => page.evaluate(() => window.sessionStateFixture.projectionReads)).toBe(2)
    await expect(page.getByTestId('load-older')).toHaveCount(0)
    await expect(page.getByTestId('assistant-message')).toHaveText('Root answer survives failed projection')
    expect(await page.evaluate(() => window.sessionStateFixture.prompts)).toEqual([])
  })

  test('same-ID opening revokes the ACK for discarded partial state and replays it', async ({ page }) => {
    const session = await createSession(page, 'replayed-partial')
    const initial = snapshot(session, 'One durable human input', true)
    await install(page, [session], [initial])
    await page.goto('/')
    await open(page, session)
    await expect.poll(() => runStreams(page, session.id).then(streams => streams.length)).toBe(1)
    const partial: LoopEvent = { type: 'message_update', runId: 'same-active-run', seq: 10, messageStream: 1,
      message: { role: 'assistant', content: [{ type: 'text', text: 'Retained partial from a stalled provider' }] } }
    await page.evaluate(({ id, partial }) => {
      window.sessionStateFixture.replay[id] = [partial]
      window.sessionStateFixture.send(id, partial)
    }, { id: session.id, partial })
    await expect(page.getByTestId('assistant-message')).toHaveText('Retained partial from a stalled provider')
    // First verify this is an applied ACK, not an event still in a frame queue.
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await expect.poll(() => runStreams(page, session.id).then(streams => streams.length)).toBe(2)
    expect((await runStreams(page, session.id))[1].cursor).toBe('same-active-run:10')
    await open(page, session)
    await expect.poll(() => runStreams(page, session.id).then(streams => streams.length)).toBe(3)
    const streams = await runStreams(page, session.id)
    expect(streams[2].cursor).toBeNull()
    expect(streams.filter(stream => !stream.aborted && !stream.closed)).toHaveLength(1)
    await expect(page.getByTestId('assistant-message')).toHaveCount(1)
    await expect(page.getByTestId('assistant-message')).toHaveText('Retained partial from a stalled provider')
    await expect(page.getByTestId('user-bubble')).toHaveCount(1)
    expect(await page.evaluate(() => window.sessionStateFixture.prompts)).toEqual([])
  })
})

test.describe('prompt and idle recovery', () => {
  profile('desktop')

  test('a prompt accepted while an old idle GET is pending still acquires a reader', async ({ page }) => {
    const session = await createSession(page, 'prompt-recovery')
    const idle = snapshot(session, 'Previous completed input')
    await install(page, [session], [idle])
    await page.goto('/')
    await open(page, session)
    await expect(page.getByTestId('user-bubble')).toHaveText('Previous completed input')
    const delayed = await hold(page, session.id)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await delayed.started()
    await page.getByTestId('composer-input').fill('Next accepted request')
    await page.getByTestId('composer-send').click()
    await expect.poll(() => page.evaluate(() => window.sessionStateFixture.prompts.length)).toBe(1)
    const requestId = await page.evaluate(() => window.sessionStateFixture.prompts[0].clientRequestId)
    expect(requestId).toBeTruthy()
    const accepted: Entry = { ...user('accepted-input', 'Next accepted request', idle.leafId),
      message: { role: 'user', clientRequestId: requestId, content: [{ type: 'text', text: 'Next accepted request' }] } }
    const running: SessionDetail = { ...idle, running: true, entries: [...idle.entries!, accepted], leafId: accepted.id }
    await page.evaluate(detail => { window.sessionStateFixture.details[detail.id] = detail }, running)
    await delayed.release(idle)
    await expect.poll(() => runStreams(page, session.id).then(streams => streams.filter(stream => !stream.aborted && !stream.closed).length)).toBe(1)
    await page.evaluate(({ id, accepted }) => {
      const f = window.sessionStateFixture
      f.send(id, { type: 'agent_start', runId: 'next-run', seq: 1 })
      f.send(id, { type: 'message_end', runId: 'next-run', seq: 2, entryId: accepted.id, parentId: accepted.parentId, message: accepted.message })
      f.send(id, { type: 'message_update', runId: 'next-run', seq: 3, messageStream: 1,
        message: { role: 'assistant', content: [{ type: 'text', text: 'The new reader is delivering work' }] } })
    }, { id: session.id, accepted })
    await expect(page.getByTestId('assistant-message')).toHaveText('The new reader is delivering work')
    await expect(page.getByTestId('composer-stop')).toBeVisible()
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'Next accepted request' })).toHaveCount(1)
    expect(await page.evaluate(() => window.sessionStateFixture.prompts.length)).toBe(1)
  })
})

test.describe('delegated activity', () => {
  profile('tablet')
  for (const lang of ['zh', 'en']) {
    test(`idle parents show localized descendant activity without owning a run (${lang})`, async ({ page }) => {
      const parent = { ...await createSession(page, `delegated-${lang}`), activeDescendantCount: 2 }
      const idle = snapshot(parent, 'Parent completed its own run')
      await install(page, [parent], [idle], lang)
      await page.goto('/')
      await open(page, parent)
      await expect(page.getByTestId('composer-stop')).toHaveCount(0)
      await page.getByTestId('composer-input').fill('A parent can accept another request')
      await expect(page.getByTestId('composer-send')).toBeEnabled()
      await sidebar(page)
      const row = page.getByTestId('session-row').filter({ hasText: parent.title })
      const text = (count: number) => lang === 'zh'
        ? `等待子任务 · ${count}（本会话空闲）`
        : `Waiting for ${count} delegated tasks · own run idle`
      await expect(row.getByTestId('session-child-activity')).toHaveText(text(2))
      await expect(row.getByTestId('session-child-activity')).toHaveAttribute('title', text(2))
      const before = await page.evaluate(() => window.sessionStateFixture.sessions[0].activeDescendantCount)
      expect(before).toBe(2)
      await page.evaluate(id => {
        const f = window.sessionStateFixture
        f.sessions = f.sessions.map(session => session.id === id ? { ...session, activeDescendantCount: 1 } : session)
        f.send('push', { type: 'invalidate', scope: 'sessions' })
      }, parent.id)
      await expect(row.getByTestId('session-child-activity')).toHaveText(text(1))
      await expect(page.getByTestId('composer-stop')).toHaveCount(0)
      await page.evaluate(id => {
        const f = window.sessionStateFixture
        f.sessions = f.sessions.map(session => session.id === id ? { ...session, activeDescendantCount: 0 } : session)
        f.send('push', { type: 'invalidate', scope: 'sessions' })
      }, parent.id)
      await expect(row.getByTestId('session-child-activity')).toHaveCount(0)
      await expect(page.getByTestId('composer-stop')).toHaveCount(0)
      expect(await runStreams(page, parent.id)).toHaveLength(0)
      expect(await page.evaluate(() => window.sessionStateFixture.prompts)).toEqual([])
    })
  }
})
