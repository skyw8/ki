import { expect, test, type Page } from '@playwright/test'
import { serverToken } from './global-setup.ts'

// This case owns its isolated server/home and mutates no shared fixtures.
test.describe.configure({ mode: 'parallel' })

async function openTools(page: Page): Promise<void> {
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-tools').click()
  await expect(page.getByTestId('tool-on-grep')).toBeEnabled()
}

test('Tools settings persist toggles without a Code Mode setting', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const initial = await request.get('/v1/tools', { headers })
  expect(initial.ok()).toBe(true)
  expect(await initial.json()).not.toHaveProperty('codeMode')
  await openTools(page)
  await expect(page.getByRole('combobox', { name: 'Code Mode', exact: true })).toHaveCount(0)
  await expect(page.getByTestId('code-mode')).toHaveCount(0)

  const grep = page.getByTestId('tool-on-grep')
  const saved = page.waitForRequest(req => req.url().includes('/v1/tools') && req.method() === 'PATCH')
  await grep.click()
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await expect(grep).toBeEnabled()
  expect((await saved).postDataJSON()).toEqual({ disabled: ['grep'] })

  await page.reload()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-tools').click()
  await expect(grep).toBeEnabled()
  await expect(grep).toHaveAttribute('aria-checked', 'false')

  const read = page.getByTestId('tool-on-read')
  await read.click()
  await expect(read).toHaveAttribute('aria-checked', 'false')
  await expect(read).toBeEnabled()
  await page.getByTestId('tools-reload').click()
  await expect(grep).toBeEnabled()
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await expect(read).toHaveAttribute('aria-checked', 'false')

  // Failure rolls back the toggle and shows a retryable error without changing saved settings.
  await page.route('**/v1/tools*', async route => {
    if (route.request().method() !== 'PATCH') return route.continue()
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'settings-save-unavailable' }) })
  })
  await grep.click()
  await expect(page.getByTestId('tools-settings-error')).toContainText('settings-save-unavailable')
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await expect(grep).toBeEnabled()
  await expect(read).toHaveAttribute('aria-checked', 'false')
  await page.unroute('**/v1/tools*')

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(grep).toBeVisible()
  const box = (await grep.boundingBox())!
  expect(box.height).toBeGreaterThanOrEqual(40)
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  const final = await request.get('/v1/tools', { headers })
  const settings = await final.json()
  expect(settings).not.toHaveProperty('codeMode')
  expect(settings.items.filter((item: { enabled: boolean }) => !item.enabled).map((item: { name: string }) => item.name)).toEqual(expect.arrayContaining(['grep', 'read']))
})
