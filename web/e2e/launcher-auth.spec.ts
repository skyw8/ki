import { expect, test, type Page } from '@playwright/test'
import { serverToken } from './global-setup'

// Each case starts signed out with its own cookies, storage and transport.
test.describe.configure({ mode: 'parallel' })
test.use({ storageState: { cookies: [], origins: [] } })

type LauncherRequest = { path: string; method: string; body: string; authorization: string | null; serverId: string | null; hash: string }
type LauncherFixture = { requests: LauncherRequest[]; replaceCalls: number; switchServer: () => void }
declare global {
  interface Window { launcherAuthFixture: LauncherFixture }
}

async function observeLauncher(page: Page, switchable = false) {
  await page.addInitScript(({ switchable }) => {
    let switched = false
    let controller: ReadableStreamDefaultController<Uint8Array> | undefined
    const state: LauncherFixture = window.launcherAuthFixture = {
      requests: [], replaceCalls: 0,
      switchServer() {
        switched = true
        controller!.enqueue(new TextEncoder().encode('data: {"type":"ready","serverId":"launcher-replacement"}\n\n'))
      },
    }
    const replace = history.replaceState.bind(history)
    history.replaceState = (...args) => {
      state.replaceCalls++
      replace(...args)
    }
    const original = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href)
      const headers = new Headers(init?.headers)
      state.requests.push({
        path: url.pathname, method: init?.method ?? 'GET', body: String(init?.body ?? ''),
        authorization: headers.get('Authorization'), serverId: headers.get('X-Ki-Server-ID'), hash: location.hash,
      })
      if (switched && url.pathname === '/v1/auth/status') {
        return new Response(JSON.stringify({ authenticated: false, serverId: 'launcher-replacement', csrfCookieName: 'ki_csrf_replacement' }))
      }
      if (switchable && url.pathname === '/v1/events') {
        const stream = new ReadableStream<Uint8Array>({
          start(current) {
            controller = current
            init?.signal?.addEventListener('abort', () => { try { current.close() } catch { /* Already closed. */ } }, { once: true })
          },
        })
        return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return original(input, init)
    }
  }, { switchable })
}

const requests = (page: Page) => page.evaluate(() => window.launcherAuthFixture.requests)

test('launcher exchanges the actual bearer once, scrubs before requests, and reuses the cookie on reload', async ({ page }) => {
  const token = serverToken()
  await observeLauncher(page)
  await page.goto(`/?launcher=1#token=${encodeURIComponent(token)}`)
  await expect(page.getByTestId('composer-input')).toBeVisible()
  expect(new URL(page.url()).hash).toBe('')
  expect(new URL(page.url()).search).toBe('?launcher=1')
  const initial = await requests(page)
  expect(initial[0].path).toBe('/v1/auth/status')
  const logins = initial.filter(request => request.path === '/v1/auth/login')
  expect(logins).toHaveLength(1)
  expect(logins[0].method).toBe('POST')
  expect(JSON.parse(logins[0].body)).toEqual({ token })
  expect(logins[0].serverId).toBeTruthy()
  for (const request of initial) {
    expect(request.hash).toBe('')
    expect(request.authorization).toBeNull()
    if (request.path !== '/v1/auth/login') expect(request.body).not.toContain(token)
  }
  expect(await page.evaluate(() => window.launcherAuthFixture.replaceCalls)).toBe(1)
  const storage = await page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } }))
  expect(JSON.stringify(storage)).not.toContain(token)

  await page.reload()
  await expect(page.getByTestId('composer-input')).toBeVisible()
  expect((await requests(page)).filter(request => request.path === '/v1/auth/login')).toHaveLength(0)
  // An already valid cookie must win even over a new, invalid launcher token.
  await page.goto('/#token=unused-invalid-token')
  await expect(page.getByTestId('composer-input')).toBeVisible()
  expect(new URL(page.url()).hash).toBe('')
  expect((await requests(page)).filter(request => request.path === '/v1/auth/login')).toHaveLength(0)
})

test('invalid encoded bearer returns to an empty manual login and can sign in normally', async ({ page }) => {
  await observeLauncher(page)
  const invalid = 'invalid+/=& \u2603'
  await page.goto(`/#token=${encodeURIComponent(invalid)}`)
  await expect(page.getByTestId('auth-login')).toBeVisible()
  await expect(page.getByTestId('auth-token')).toHaveValue('')
  expect(new URL(page.url()).hash).toBe('')
  const initial = await requests(page)
  expect(initial.map(request => request.path)).toEqual(['/v1/auth/status', '/v1/auth/login'])
  expect(JSON.parse(initial[1].body)).toEqual({ token: invalid })
  await page.getByTestId('auth-token').fill(serverToken())
  await page.getByTestId('auth-submit').click()
  await expect(page.getByTestId('composer-input')).toBeVisible()
})

for (const stage of ['status', 'login'] as const) {
  test(`launcher ${stage} network failure returns to manual login without retries`, async ({ page }) => {
    await observeLauncher(page)
    await page.route(`/v1/auth/${stage}`, route => route.abort())
    await page.goto(`/#token=${encodeURIComponent(serverToken())}`)
    await expect(page.getByTestId('auth-login')).toBeVisible()
    await expect(page.getByTestId('auth-token')).toHaveValue('')
    expect(new URL(page.url()).hash).toBe('')
    const initial = await requests(page)
    expect(initial.map(request => request.path)).toEqual(stage === 'status' ? ['/v1/auth/status'] : ['/v1/auth/status', '/v1/auth/login'])
    await page.unroute(`/v1/auth/${stage}`)
    await page.getByTestId('auth-token').fill(serverToken())
    await page.getByTestId('auth-submit').click()
    await expect(page.getByTestId('composer-input')).toBeVisible()
  })
}

test('server generation changes recheck auth without replaying the consumed launcher bearer', async ({ page }) => {
  await observeLauncher(page, true)
  await page.goto(`/#token=${encodeURIComponent(serverToken())}`)
  await expect(page.getByTestId('composer-input')).toBeVisible()
  await expect.poll(async () => (await requests(page)).some(request => request.path === '/v1/events')).toBe(true)
  await page.evaluate(() => window.launcherAuthFixture.switchServer())
  await expect(page.getByTestId('auth-login')).toBeVisible()
  await expect(page.getByTestId('auth-token')).toHaveValue('')
  const all = await requests(page)
  expect(all.filter(request => request.path === '/v1/auth/status')).toHaveLength(2)
  expect(all.filter(request => request.path === '/v1/auth/login')).toHaveLength(1)
})

test('a launcher exchange fenced by a replaced server rediscovers it without resending the bearer', async ({ page }) => {
  await observeLauncher(page)
  await page.route('/v1/auth/login', route => route.fulfill({ status: 421, body: 'server instance changed' }))
  await page.goto(`/#token=${encodeURIComponent(serverToken())}`)
  await expect(page.getByTestId('auth-login')).toBeVisible()
  await expect(page.getByTestId('auth-token')).toHaveValue('')
  const all = await requests(page)
  expect(all.filter(request => request.path === '/v1/auth/status')).toHaveLength(2)
  expect(all.filter(request => request.path === '/v1/auth/login')).toHaveLength(1)
})
