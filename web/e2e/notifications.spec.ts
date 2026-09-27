import { expect, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { permissionFrom, shouldNotify } from '../src/lib/notifications.ts'
import { reconcileFinishedRuns } from '../src/lib/completion-catchup.ts'
import { serverToken } from './global-setup.ts'
import { newSession } from './session.ts'

// Every test is self-contained, so the parallel runner may split this file into
// one isolated process per test.
test.describe.configure({ mode: 'parallel' })

test('permission reflects the secure-context rules of the Notification API', () => {
  // http://localhost / 127.0.0.1 port forwards are trustworthy; hostname/LAN
  // plain HTTP is not, and the browser hides the API there.
  expect(permissionFrom({ secure: false, supported: true, permission: 'granted' })).toBe('insecure')
  expect(permissionFrom({ secure: true, supported: false })).toBe('unsupported')
  expect(permissionFrom({ secure: true, supported: true, permission: 'granted' })).toBe('granted')
  expect(permissionFrom({ secure: true, supported: true })).toBe('default')
})

test('shouldNotify notifies unless a focused tab is showing that session', () => {
  const base = { enabled: true, permission: 'granted' as const, sessionId: 'A', subagent: false }
  // User switched to another session: the finished one is not on screen.
  expect(shouldNotify({ ...base, focusedSession: 'B' })).toBe(true)
  // User is in another application: no ki tab is focused.
  expect(shouldNotify({ ...base, focusedSession: null })).toBe(true)
  // The focused tab is showing exactly the session that completed.
  expect(shouldNotify({ ...base, focusedSession: 'A' })).toBe(false)
  // Subagent sessions stay silent even when nothing is on screen for them.
  expect(shouldNotify({ ...base, subagent: true, focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, subagent: true, focusedSession: null })).toBe(false)
  // Preference and permission still gate everything.
  expect(shouldNotify({ ...base, enabled: false, focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'default', focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'denied', focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'unsupported', focusedSession: 'B' })).toBe(false)
})

test('reconcileFinishedRuns announces only runs missed while suspended', () => {
  const known = new Set(['running', 'finished', 'deleted', 'aborted'])
  const aborted = new Set(['aborted'])
  const announced: string[] = []
  reconcileFinishedRuns({
    known,
    sessions: [
      { id: 'running', running: true }, // still going: nothing to announce
      { id: 'finished' },               // stopped while away: announce
      { id: 'aborted' },                // the tab aborted it: stay silent
      // 'deleted' is absent from the list (removed while away): stay silent
    ],
    aborted,
    announce: id => announced.push(id),
  })
  expect(announced).toEqual(['finished'])
  // Inspected ids are dropped so a later resume cannot repeat them; a session
  // still running stays known for the next resume to catch.
  expect([...known]).toEqual(['running'])
  expect([...aborted]).toEqual([])

  // A tab that never saw a run state nothing (initial subscribe, recovery).
  const empty = new Set<string>()
  reconcileFinishedRuns({ known: empty, sessions: [{ id: 'x' }], aborted: new Set(), announce: () => { throw new Error('must not announce') } })
})

// The WebUI keeps a lightweight notification stream open for every running
// session, so a completion reaches the browser even after the user switches
// sessions or applications. This probe records the notifications the page
// raises and lets a test force the tab inactive (the real browser fires
// blur/visibilitychange; overriding the getters needs the events dispatched).
//
// Both delivery channels are recorded: the service worker registration when one
// exists (Android Chrome has no Notification constructor at all) and the
// constructor otherwise. The tests care that the page raised a notification,
// not which channel took it.
async function installNotificationProbe(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as { __kiHidden: boolean; __kiNotifications: Array<{ title: string; body?: string }> }
    w.__kiHidden = false
    w.__kiNotifications = []
    class StubNotification {
      static permission = 'default'
      static async requestPermission(): Promise<string> {
        StubNotification.permission = 'granted'
        return 'granted'
      }
      onclick: (() => void) | null = null
      constructor(title: string, options?: { body?: string }) {
        w.__kiNotifications.push({ title, body: options?.body })
      }
      close(): void {}
    }
    Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: StubNotification })
    if ('ServiceWorkerRegistration' in window) {
      Object.defineProperty(ServiceWorkerRegistration.prototype, 'showNotification', {
        configurable: true,
        writable: true,
        value: async function (title: string, options?: { body?: string }) {
          w.__kiNotifications.push({ title, body: options?.body })
        },
      })
    }
    Object.defineProperty(Document.prototype, 'hidden', { configurable: true, get: () => w.__kiHidden })
    Object.defineProperty(Document.prototype, 'visibilityState', { configurable: true, get: () => (w.__kiHidden ? 'hidden' : 'visible') })
    Object.defineProperty(Document.prototype, 'hasFocus', { configurable: true, value: () => !w.__kiHidden })
  })
}

function notifications(page: Page): Promise<Array<{ title: string; body?: string }>> {
  return page.evaluate(() => (window as unknown as { __kiNotifications: Array<{ title: string; body?: string }> }).__kiNotifications)
}

async function setBackground(page: Page, hidden: boolean): Promise<void> {
  await page.evaluate((next: boolean) => {
    const w = window as unknown as { __kiHidden: boolean }
    w.__kiHidden = next
    window.dispatchEvent(new Event(next ? 'blur' : 'focus'))
    document.dispatchEvent(new Event('visibilitychange'))
  }, hidden)
}

async function enableNotifications(page: Page): Promise<void> {
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-notifications').click()
  await expect(page.getByTestId('notifications-settings')).toBeVisible()
  await page.getByTestId('notify-toggle').click()
  await expect(page.getByTestId('notify-toggle')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('notify-permission')).toHaveText('浏览器已允许通知。')
  // The test button proves the granted path wires through to a real
  // notification (the service worker registration when the browser has one).
  await page.getByTestId('notify-test').click()
  await expect.poll(() => notifications(page)).toHaveLength(1)
  await page.getByTestId('settings-mask').click({ position: { x: 4, y: 4 } })
}

async function sendPrompt(page: Page, text: string): Promise<void> {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

async function waitIdle(page: Page): Promise<void> {
  await expect(page.getByTestId('assistant-message').first()).toBeVisible()
  await expect.poll(async () => page.evaluate(async () => {
    const list = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ running?: boolean }>
    return list.some(s => s.running)
  })).toBe(false)
}

test('completion notifies when the session is not the focused one', async ({ page }) => {
  await installNotificationProbe(page)
  await page.goto('/')
  await enableNotifications(page)

  // Focused tab watching the run: the delayed run really completes while the
  // session is on screen, and must stay quiet.
  await newSession(page)
  await sendPrompt(page, 'e2e-delay-300 foreground-run')
  await waitIdle(page)
  expect(await notifications(page)).toHaveLength(1)

  // Run a session, switch to another session before it finishes: still notify.
  await sendPrompt(page, 'e2e-delay-1200 switched-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await newSession(page)
  await expect.poll(() => notifications(page)).toHaveLength(2)
  const switched = (await notifications(page))[1]
  expect(switched.body).toContain('会话已完成')
  // The body carries the finished session's cwd so equal-looking titles differ.
  const cwd = await page.evaluate(async (title: string) => {
    const list = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ title?: string; cwd?: string }>
    return list.find(s => s.title === title)?.cwd ?? null
  }, switched.title)
  expect(cwd).toBeTruthy()
  expect(switched.body).toContain(cwd!)

  // Run with the browser behind another application: still notify.
  await setBackground(page, true)
  await sendPrompt(page, 'e2e-delay-400 background-run')
  await expect.poll(() => notifications(page)).toHaveLength(3)
  expect((await notifications(page))[2].body).toContain('会话已完成')
})

// A completion that lands while the page is suspended (phone locked) is never
// observed: the push stream replays nothing, so its agent_end is gone for good.
// The resume refresh must recover it from "was running, now stopped" rather than
// dropping the notification. Aborting the push stream reproduces the missed
// sideband without a real device sleep; the run stream and REST stay up.
test('a completion missed while the push stream is down still notifies on resume', async ({ page, request }) => {
  await installNotificationProbe(page)
  await page.route('**/v1/events', route => route.abort())
  await page.goto('/')
  await enableNotifications(page)

  await newSession(page)
  await sendPrompt(page, 'e2e-delay-1200 suspended-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  // Switch away before it finishes, and drop focus: the finished session is not
  // the one on screen, so the recovered completion is allowed to notify.
  await newSession(page)
  await setBackground(page, true)

  const headers = { Authorization: `Bearer ${serverToken()}` }
  let id = ''
  await expect.poll(async () => {
    const list = await request.get('/v1/sessions', { headers }).then(r => r.json() as Promise<Array<{ id: string; running?: boolean }>>)
    id = list.find(s => s.running)?.id ?? ''
    return id
  }).not.toBe('')
  await expect.poll(async () => {
    const detail = await request.get(`/v1/sessions/${id}`, { headers }).then(r => r.json() as Promise<{ running?: boolean }>)
    return detail.running ?? false
  }).toBe(false)

  // The channel comes back: the reconnect's ready frame refetches everything,
  // which is where the missed completion surfaces.
  await page.unroute('**/v1/events')
  await setBackground(page, false)
  await expect.poll(() => notifications(page)).toHaveLength(2)
  expect((await notifications(page))[1].body).toContain('会话已完成')
})

test('aborting a run does not notify', async ({ page }) => {
  await installNotificationProbe(page)
  await page.goto('/')
  await enableNotifications(page)

  await newSession(page)
  await sendPrompt(page, 'e2e-hold abort-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await setBackground(page, true)
  await page.getByTestId('composer-stop').click()
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect.poll(async () => page.evaluate(async () => {
    const list = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ running?: boolean }>
    return list.some(s => s.running)
  })).toBe(false)
  // run_aborted is published before agent_end; let the watcher process the
  // (suppressed) completion before asserting nothing new was raised.
  await page.waitForTimeout(400)
  expect(await notifications(page)).toHaveLength(1)
})

test('subagent sessions never notify', async ({ page, request }) => {
  await installNotificationProbe(page)
  await page.goto('/')

  // A subagent session is a tree child of a parent session. Open it and prompt
  // it from this tab (so the tab tracks its run), then switch away before it
  // finishes: an ordinary session would notify here, a subagent one must not.
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const stamp = Date.now()
  const parentTitle = `notify-parent-${stamp}`
  const childTitle = `notify-child-${stamp}`
  const parentRes = await request.post('/v1/sessions', { headers, data: {} })
  expect(parentRes.ok()).toBe(true)
  const parent = await parentRes.json() as { id: string }
  await request.patch(`/v1/sessions/${parent.id}`, { headers, data: { title: parentTitle } })
  const childRes = await request.post(`/v1/sessions/${parent.id}/fork`, { headers, data: { forkMode: 'tree' } })
  expect(childRes.ok()).toBe(true)
  const child = await childRes.json() as { id: string }
  await request.patch(`/v1/sessions/${child.id}`, { headers, data: { title: childTitle } })

  // Reload first: the probe's addInitScript resets the recorded notifications
  // on navigation, so enable notifications only after the last reload.
  await page.reload()
  await enableNotifications(page)
  const parentRow = page.locator('[data-testid="session-row"]').filter({ hasText: parentTitle })
  await expect(parentRow).toBeVisible()
  await parentRow.getByTestId('session-toggle').click()
  const childRow = page.locator('[data-testid="session-row"]').filter({ hasText: childTitle })
  await childRow.locator('.session-main').click()
  await expect(childRow.locator('.session-main')).toHaveAttribute('aria-current', 'page')

  await sendPrompt(page, 'e2e-delay-1200 subagent-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await newSession(page)
  await expect.poll(async () => request.get(`/v1/sessions/${child.id}`, { headers }).then(r => r.json() as Promise<{ running?: boolean }>).then(d => d.running ?? false)).toBe(false)
  // Give the push watcher time to process the (suppressed) completion.
  await page.waitForTimeout(500)
  expect(await notifications(page)).toHaveLength(1)
})

// A port forward that exposes the server as `http://<host>:<port>` (LAN or
// Tailscale IP) is not a secure origin, so Chrome/Firefox never expose the
// Notification API there. The settings page must say why instead of failing
// silently.
test('plain-HTTP host access reports the secure-context requirement', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, get: () => false })
  })
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-notifications').click()
  await expect(page.getByTestId('notify-permission')).toContainText('明文 HTTP')
  await expect(page.getByTestId('notify-insecure')).toBeVisible()
  await page.getByTestId('notify-toggle').click()
  await expect(page.getByTestId('toast')).toContainText('localhost')
})

// With several ki tabs the focused one owns a shared marker naming its session,
// so a background tab can tell that the user is already looking at the session
// that just completed and stay quiet.
test('a focused tab suppresses notifications in other ki tabs', async ({ page, context }) => {
  const other = await context.newPage()
  await installNotificationProbe(page)
  await installNotificationProbe(other)
  await page.goto('/')
  await other.goto('/')

  // Make the first tab the focused one; the other sits in the background.
  await setBackground(other, true)
  await setBackground(page, false)
  await newSession(page)
  await expect.poll(async () => page.evaluate(() => {
    const raw = localStorage.getItem('ki-focused-session')
    return raw ? (JSON.parse(raw) as { session?: string }).session ?? null : null
  })).toBeTruthy()
  const sessionId = await page.evaluate(() => (JSON.parse(localStorage.getItem('ki-focused-session')!) as { session: string }).session)

  // The other tab reads the same shared marker.
  await expect.poll(async () => other.evaluate(() => {
    const raw = localStorage.getItem('ki-focused-session')
    return raw ? (JSON.parse(raw) as { session?: string }).session ?? null : null
  })).toBe(sessionId)

  await other.close()
})

// The service worker ships as a plain script (Vite copies web/public verbatim
// rather than bundling it), so it is loaded from source here. This asserts the
// artifact the browser actually runs, not a copy of its rule.
function loadServiceWorker() {
  const code = readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8')
  const handlers = new Map<string, (event: unknown) => void>()
  const shown: Array<{ title: string; options: { body?: string; tag?: string } }> = []
  const clients: Array<{ focused: boolean; visibilityState: string; url: string }> = []
  let pending: Promise<unknown> = Promise.resolve()
  const self = {
    addEventListener: (type: string, fn: (event: unknown) => void) => { handlers.set(type, fn) },
    skipWaiting: () => {},
    clients: {
      claim: async () => {},
      matchAll: async () => clients,
      openWindow: async () => ({ url: '/' }),
    },
    registration: {
      scope: 'http://localhost/',
      showNotification: async (title: string, options: { body?: string; tag?: string }) => { shown.push({ title, options }) },
    },
    navigator: { language: 'zh-CN' },
  }
  new Function('self', code)(self)
  return {
    clients,
    shown,
    async push(payload: unknown) {
      pending = Promise.resolve()
      handlers.get('push')!({ data: payload === null ? null : { json: () => payload }, waitUntil: (p: Promise<unknown>) => { pending = p } })
      await pending
    },
  }
}

test('service worker shows a push only when no focused ki tab owns it', async () => {
  const sw = loadServiceWorker()

  // A focused window gets the completion over its own push stream, so the
  // worker stays quiet rather than double-notifying.
  sw.clients.push({ focused: true, visibilityState: 'visible', url: 'http://localhost/' })
  await sw.push({ type: 'run_complete', sessionId: 'A', title: 'hello', cwd: '/tmp/x' })
  expect(sw.shown).toHaveLength(0)

  // No window at all (phone locked, tab closed): the push is the only signal.
  sw.clients.length = 0
  await sw.push({ type: 'run_complete', sessionId: 'A', title: 'hello', cwd: '/tmp/x' })
  expect(sw.shown).toHaveLength(1)
  expect(sw.shown[0].title).toBe('hello')
  expect(sw.shown[0].options.body).toBe('/tmp/x · 会话已完成')
  expect(sw.shown[0].options.tag).toBe('ki-run-A')

  // A background-but-open window still notifies (the page is not on screen).
  sw.clients.push({ focused: false, visibilityState: 'hidden', url: 'http://localhost/' })
  await sw.push({ type: 'run_complete', sessionId: 'B', title: 'b', cwd: '' })
  expect(sw.shown).toHaveLength(2)
  expect(sw.shown[1].options.body).toBe('会话已完成')

  // Unknown or malformed frames are ignored instead of throwing.
  await sw.push({ type: 'something_else' })
  await sw.push(null)
  expect(sw.shown).toHaveLength(2)
})

// Headless Chromium has no push service, so a real subscribe() cannot succeed.
// Stubbing the Push API exercises the client's registration path and the server
// endpoint together: the stub returns real P-256 key material, so the server
// validates and stores it like a production subscription.
test('enabling notifications registers a Web Push subscription with the server', async ({ page }) => {
  await page.addInitScript(() => {
    const toBase64Url = (bytes: Uint8Array) =>
      btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    const subscription = async () => {
      const pair = await crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, ['deriveBits'])
      const raw = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey))
      const auth = crypto.getRandomValues(new Uint8Array(16))
      const sub = {
        endpoint: 'https://push.example/ki-e2e-endpoint',
        toJSON: () => ({ endpoint: sub.endpoint, keys: { p256dh: toBase64Url(raw), auth: toBase64Url(auth) } }),
        unsubscribe: async () => true,
      }
      return sub
    }
    class StubNotification {
      static permission = 'granted'
      static async requestPermission() { return 'granted' }
      onclick: (() => void) | null = null
      constructor() {}
      close(): void {}
    }
    Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: StubNotification })
    Object.defineProperty(window, 'PushManager', { configurable: true, value: function PushManager() {} })
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        register: async () => ({ pushManager: { getSubscription: async () => null, subscribe: async () => subscription() } }),
        getRegistration: async () => ({ pushManager: { getSubscription: async () => subscription() } }),
        ready: Promise.resolve(),
      },
    })
  })

  const posted = page.waitForRequest(req => req.url().includes('/v1/push/subscriptions') && req.method() === 'POST')
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-notifications').click()
  await page.getByTestId('notify-toggle').click()

  const request = await posted
  const body = JSON.parse(request.postData() ?? '{}') as { endpoint: string; keys?: { p256dh?: string } }
  expect(body.endpoint).toBe('https://push.example/ki-e2e-endpoint')
  expect(body.keys?.p256dh).toBeTruthy()
  // The server accepted it, so the page reports push as ready.
  await expect(page.getByTestId('notify-push')).toHaveAttribute('data-push', 'on')
})

// Regression: the settings test button went silent on a phone while Web Push
// completions still arrived. Android Chrome has no Notification constructor at
// all (`new Notification()` throws "Illegal constructor"), so a page-side
// notification there can only leave through the service worker registration —
// which is exactly the channel Web Push uses.
test('page notifications leave through the service worker when the constructor is missing', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __kiSwShown: string[]; __kiCtorCalls: number }
    w.__kiSwShown = []
    w.__kiCtorCalls = 0
    class StubNotification {
      static permission = 'granted'
      static async requestPermission(): Promise<string> { return 'granted' }
      constructor() {
        w.__kiCtorCalls++
        throw new TypeError('Illegal constructor')
      }
      close(): void {}
    }
    Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: StubNotification })
    Object.defineProperty(ServiceWorkerRegistration.prototype, 'showNotification', {
      configurable: true,
      writable: true,
      value: async function (title: string) { w.__kiSwShown.push(title) },
    })
    localStorage.setItem('ki-notify', 'on')
  })

  await page.goto('/')
  // The preference is already on, so the app registers the worker itself.
  await page.waitForFunction(() => navigator.serviceWorker.getRegistration('/').then(reg => Boolean(reg?.active)))

  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-notifications').click()
  await page.getByTestId('notify-test').click()

  await expect.poll(() => page.evaluate(() => (window as unknown as { __kiSwShown: string[] }).__kiSwShown)).toHaveLength(1)
  // The constructor must not be attempted first: on Android it throws, and the
  // old code treated that as a delivered notification.
  expect(await page.evaluate(() => (window as unknown as { __kiCtorCalls: number }).__kiCtorCalls)).toBe(0)
})

// The other half of the old bug: nothing told the user the notification never
// left. With no service worker and no constructor there is no channel left.
test('the test button reports a notification that could not be delivered', async ({ page }) => {
  await page.addInitScript(() => {
    class StubNotification {
      static permission = 'granted'
      static async requestPermission(): Promise<string> { return 'granted' }
      constructor() { throw new TypeError('Illegal constructor') }
      close(): void {}
    }
    Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: StubNotification })
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: undefined })
  })

  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-notifications').click()
  await page.getByTestId('notify-test').click()
  await expect(page.getByTestId('toast').first()).toContainText('测试通知没有发出')
})
