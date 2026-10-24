import { expect, type Page } from '@playwright/test'
import { expandWorkspaces } from './sidebar.ts'

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

export async function newSession(page: Page): Promise<void> {
  await expandWorkspaces(page)
  const rows = page.locator('.session-row')
  const before = await rows.count()
  await page.getByTestId('new-session').click()
  await expect(rows).toHaveCount(before + 1)
  await expect(page.locator('.session-row.active')).toHaveCount(1)
}
