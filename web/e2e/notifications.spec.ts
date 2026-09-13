import { expect, test, type Page } from '@playwright/test'
import { permissionFrom, shouldNotify } from '../src/lib/notifications.ts'

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
  const base = { enabled: true, permission: 'granted' as const, sessionId: 'A' }
  // User switched to another session: the finished one is not on screen.
  expect(shouldNotify({ ...base, focusedSession: 'B' })).toBe(true)
  // User is in another application: no ki tab is focused.
  expect(shouldNotify({ ...base, focusedSession: null })).toBe(true)
  // The focused tab is showing exactly the session that completed.
  expect(shouldNotify({ ...base, focusedSession: 'A' })).toBe(false)
  // Preference and permission still gate everything.
  expect(shouldNotify({ ...base, enabled: false, focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'default', focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'denied', focusedSession: 'B' })).toBe(false)
  expect(shouldNotify({ ...base, permission: 'unsupported', focusedSession: 'B' })).toBe(false)
})

// The WebUI keeps a lightweight notification stream open for every running
// session, so a completion reaches the browser even after the user switches
// sessions or applications. This probe records the notifications the page
// raises and lets a test force the tab inactive (the real browser fires
// blur/visibilitychange; overriding the getters needs the events dispatched).
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
  // The test button proves the granted path wires through to `new Notification`.
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
  await page.getByTestId('new-session').click()
  await sendPrompt(page, 'e2e-delay-300 foreground-run')
  await waitIdle(page)
  expect(await notifications(page)).toHaveLength(1)

  // Run a session, switch to another session before it finishes: still notify.
  await sendPrompt(page, 'e2e-delay-1200 switched-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await page.getByTestId('new-session').click()
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

test('aborting a run does not notify', async ({ page }) => {
  await installNotificationProbe(page)
  await page.goto('/')
  await enableNotifications(page)

  await page.getByTestId('new-session').click()
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
  await page.getByTestId('new-session').click()
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
