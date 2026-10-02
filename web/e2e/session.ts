import { expect, type Page } from '@playwright/test'

// Resolve the server namespace once, not on every polling iteration. The
// returned reader may also inspect another tab sharing this browser context.
export async function focusedSessionReader(page: Page): Promise<(target?: Page) => Promise<string | null>> {
  const response = await page.request.get('/v1/auth/status')
  expect(response.ok()).toBe(true)
  const { serverId } = await response.json() as { serverId: string }
  expect(serverId).toBeTruthy()
  const key = `ki-focused-session:${serverId}`
  return (target = page) => target.evaluate(key => {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as { session?: string }).session ?? null : null
  }, key)
}

// newSession clicks the sidebar's new-session button and waits for the created
// session to become current.
//
// Why: the sidebar only switches once the create call returns, so a prompt sent
// right after the click can run in the previous session — the view then shows a
// different session than the one holding the run, and assertions about that run
// (stop button, streaming) never see it.
export async function newSession(page: Page): Promise<void> {
  const rows = page.locator('.session-row')
  const before = await rows.count()
  await page.getByTestId('new-session').click()
  await expect(rows).toHaveCount(before + 1)
  await expect(page.locator('.session-row.active')).toHaveCount(1)
}
