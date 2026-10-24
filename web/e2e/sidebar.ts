import { expect, type Page } from '@playwright/test'

async function openSidebar(page: Page): Promise<void> {
  const nav = page.getByTestId('mobile-nav-toggle')
  for (let attempt = 0; attempt < 40; attempt++) {
    if (!(await nav.count())) return
    if ((await nav.getAttribute('aria-expanded')) === 'true') return
    try {
      await nav.click({ timeout: 2_000 })
    } catch {
    }
  }
  throw new Error('mobile sidebar did not open')
}

export async function expandWorkspaces(page: Page): Promise<void> {
  await expect(page.locator('main.main')).toBeVisible()
  await openSidebar(page)
  await expect(page.getByTestId('session-list')).toHaveAttribute('data-ready', 'true')
  const collapsed = page.locator('.ws-toggle[aria-expanded="false"]')
  for (let guard = 0; guard < 64; guard++) {
    const count = await collapsed.count()
    if (!count) return
    await collapsed.first().click()
    await expect(collapsed).toHaveCount(count - 1)
  }
  throw new Error('workspace toggles stayed collapsed after repeated clicks')
}

export async function openSessionRow(page: Page, title?: string): Promise<void> {
  await expandWorkspaces(page)
  const rows = page.getByTestId('session-row')
  const row = title === undefined ? rows.first() : rows.filter({ hasText: title }).first()
  await expect(row).toBeVisible()
  await row.locator('.session-main').click()
}
