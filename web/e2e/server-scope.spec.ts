import { expect, test, type Page } from '@playwright/test'
import type { Meta, ModelInfo, SessionDetail, SessionInfo } from '../src/api/types'
import { serverToken } from './global-setup'
import { newSession } from './session'

// Each case owns its browser state, fetch fixture and server mutations; there
// is no dependency on another case's login, sessions or model selection.
test.describe.configure({ mode: 'parallel' })

type ScopeFixture = {
  serverId: string
  switched: boolean
  unauthorized: boolean
  authChecks: number
  streams: number
  creates: Array<{ model?: string; thinkingEffort?: string }>
  switchServer: () => void
  rejectReconnect: () => void
}
declare global {
  interface Window { serverScopeFixture: ScopeFixture }
}

async function installTransport(page: Page, nextModel?: ModelInfo, previous?: SessionInfo, terminalStatus = 401) {
  await page.addInitScript(({ nextModel, previous, terminalStatus }) => {
    localStorage.setItem('ki-lang', 'en')
    let controller: ReadableStreamDefaultController<Uint8Array> | undefined
    let meta: Meta | undefined
    let backingServerId = ''
    const created: SessionInfo[] = []
    const state: ScopeFixture = window.serverScopeFixture = {
      serverId: '', switched: false, unauthorized: false, authChecks: 0, streams: 0, creates: [],
      switchServer() {
        state.switched = true
        state.serverId = 'server-scope-replacement'
        controller!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'ready', serverId: state.serverId })}\n\n`))
      },
      rejectReconnect() {
        state.unauthorized = true
        controller!.close()
      },
    }
    const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
    const original = window.fetch.bind(window)
    const passThrough: typeof window.fetch = (input, init) => {
      // The virtual backend has a different identity, but real API writes
      // still use the isolated fixture server. Translate only the transport
      // identity header; leave model/body/CSRF assertions and auth intact.
      const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined))
      if (backingServerId && headers.has('X-Ki-Server-ID')) headers.set('X-Ki-Server-ID', backingServerId)
      return original(input, { ...init, headers })
    }
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href)
      const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
      if (url.pathname === '/v1/auth/status') {
        state.authChecks++
        const body = await (await passThrough(input, init)).json()
        backingServerId = body.serverId
        state.serverId ||= body.serverId
        return json({ ...body, authenticated: !state.unauthorized, serverId: state.serverId })
      }
      if (url.pathname === '/v1/events') {
        state.streams++
        if (state.unauthorized) return new Response('identity or authentication expired', { status: terminalStatus })
        const body = new ReadableStream<Uint8Array>({
          start(current) {
            controller = current
            let closed = false
            init?.signal?.addEventListener('abort', () => {
              if (closed) return
              closed = true
              try { current.close() } catch { /* A test-triggered close can win. */ }
            }, { once: true })
            current.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'ready', serverId: state.serverId })}\n\n`))
          },
        })
        return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (url.pathname === '/v1/meta') {
        meta ??= await (await passThrough(input, init)).json() as Meta
        return json(state.switched && nextModel
          ? { ...meta, provider: nextModel.provider, model: nextModel.id, thinkingEffort: nextModel.defaultThinking }
          : meta)
      }
      if (state.switched && url.pathname === '/v1/models') return json([nextModel])
      if (url.pathname === '/v1/sessions' && method === 'GET' && (state.switched || previous)) {
        return json(state.switched ? created : [previous])
      }
      if (url.pathname === '/v1/sessions' && method === 'POST') {
        state.creates.push(JSON.parse(String(init?.body)))
        const response = await passThrough(input, init)
        if (response.ok && state.switched) created.push(await response.clone().json())
        return response
      }
      if (!state.switched && previous && url.pathname === `/v1/sessions/${previous.id}` && method === 'GET') {
        const detail: SessionDetail = {
          ...previous, runtime: { ready: true }, running: false, queued: [], hasMore: false,
          leafId: 'old-user', oldestId: 'old-user',
          entries: [{ type: 'message', id: 'old-user', message: { role: 'user', content: [{ type: 'text', text: 'Previous backend transcript' }] } }],
        }
        return json(detail)
      }
      return passThrough(input, init)
    }
  }, { nextModel, previous, terminalStatus })
}

test('legacy model and malformed workspace preferences cannot break a fresh session', async ({ page }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const meta = await (await page.request.get('/v1/meta', { headers })).json() as Meta
  await page.addInitScript(() => {
    localStorage.setItem('ki-last-model', JSON.stringify({ provider: 'openai-codex', model: 'gpt-5.4', thinkingEffort: 'high fast' }))
    localStorage.setItem('ki-ws-expanded', 'null')
  })
  await page.goto('/')
  await expect(page.getByTestId('composer-input')).toBeVisible()
  const response = page.waitForResponse(response => response.url().endsWith('/v1/sessions') && response.request().method() === 'POST')
  await newSession(page)
  const created = await response
  expect(created.ok()).toBe(true)
  const session = await created.json() as SessionInfo
  expect(session.provider).toBe(meta.provider)
  expect(session.model).toBe(meta.model)
  expect(created.request().postDataJSON().model ?? '').not.toContain('openai-codex')
})

test('same-origin server replacement retires the session, draft and old catalog', async ({ page }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const meta = await (await page.request.get('/v1/meta', { headers })).json() as Meta
  const models = await (await page.request.get('/v1/models', { headers })).json() as ModelInfo[]
  const nextModel = models.find(model => model.provider !== meta.provider)!
  expect(nextModel).toBeTruthy()
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(response.ok()).toBe(true)
  const previous = await response.json() as SessionInfo
  await installTransport(page, nextModel, previous)
  await page.goto('/')
  await expect(page.getByTestId('session-row')).toHaveCount(1)
  await page.locator('.session-main').click()
  await expect(page.getByTestId('user-bubble').getByText('Previous backend transcript', { exact: true })).toBeVisible()
  await page.getByTestId('composer-input').fill('Unsent previous backend draft')
  await expect.poll(() => page.evaluate(() => window.serverScopeFixture.streams)).toBeGreaterThan(0)
  await page.evaluate(() => window.serverScopeFixture.switchServer())
  await expect(page.getByTestId('hero')).toBeVisible()
  await expect(page.getByTestId('composer-input')).toHaveValue('')
  await expect(page.getByText('Previous backend transcript', { exact: true })).toHaveCount(0)
  await expect(page.getByTestId('session-row')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => window.serverScopeFixture.authChecks)).toBeGreaterThan(1)
  await newSession(page)
  const request = await page.evaluate(() => window.serverScopeFixture.creates.at(-1))
  expect(request?.model).toBe(nextModel.spec)
  await expect(page.locator('.session-row.active')).toContainText(nextModel.id)
})

for (const status of [401, 421]) {
  test(`a ${status} push reconnect rechecks auth and returns to login instead of retrying forever`, async ({ page }) => {
    await installTransport(page, undefined, undefined, status)
    await page.goto('/')
    await expect(page.getByTestId('composer-input')).toBeVisible()
    await expect.poll(() => page.evaluate(() => window.serverScopeFixture.streams)).toBeGreaterThan(0)
    await page.getByTestId('composer-input').fill('Draft owned by expired login')
    await page.evaluate(() => window.serverScopeFixture.rejectReconnect())
    await expect(page.getByTestId('auth-login')).toBeVisible()
    await expect(page.getByTestId('composer-input')).toHaveCount(0)
    await expect.poll(() => page.evaluate(() => window.serverScopeFixture.authChecks)).toBeGreaterThan(1)
  })
}

test('a stale login form rediscovers the replacement without resubmitting its token', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('ki-lang', 'en')
    const original = window.fetch.bind(window)
    let replaced = false
    const state = { authChecks: 0, loginRequests: [] as Array<{ serverId: string | null; token: string }> }
    Object.assign(window, { loginScopeFixture: state })
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href)
      if (url.pathname === '/v1/auth/status') {
        state.authChecks++
        const body = await (await original(input, init)).json()
        return new Response(JSON.stringify({
          ...body, authenticated: false, serverId: replaced ? 'login-replacement' : 'login-original',
        }), { headers: { 'Content-Type': 'application/json' } })
      }
      if (url.pathname === '/v1/auth/login') {
        state.loginRequests.push({
          serverId: new Headers(init?.headers).get('X-Ki-Server-ID'),
          token: JSON.parse(String(init?.body)).token,
        })
        replaced = true
        return new Response('server instance changed', { status: 421 })
      }
      return original(input, init)
    }
  })
  await page.goto('/')
  await expect(page.getByTestId('auth-login')).toBeVisible()
  await page.getByTestId('auth-token').fill('token-for-original-backend')
  await page.getByTestId('auth-submit').click()
  await expect.poll(() => page.evaluate(() => (window as unknown as {
    loginScopeFixture: { authChecks: number }
  }).loginScopeFixture.authChecks)).toBe(2)
  await expect(page.getByTestId('auth-token')).toHaveValue('')
  await expect(page.getByTestId('auth-submit')).toBeDisabled()
  const requests = await page.evaluate(() => (window as unknown as {
    loginScopeFixture: { loginRequests: Array<{ serverId: string; token: string }> }
  }).loginScopeFixture.loginRequests)
  expect(requests).toEqual([{ serverId: 'login-original', token: 'token-for-original-backend' }])
})
