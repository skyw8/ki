import { expect, test, type Page } from '@playwright/test'
import { serverToken } from './global-setup.ts'

// This case owns its isolated server/home and mutates no shared fixtures.
test.describe.configure({ mode: 'parallel' })

async function openTools(page: Page): Promise<void> {
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-tools').click()
  await expect(page.getByTestId('code-mode')).toBeEnabled()
}

test('Code Mode settings persist globally without replacing tool toggles', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const initial = await request.get('/v1/tools', { headers })
  expect(initial.ok()).toBe(true)
  expect((await initial.json()).codeMode).toBe('mixed')
  await openTools(page)
  const mode = page.getByRole('combobox', { name: 'Code Mode', exact: true })
  await expect(mode).toHaveValue('mixed')
  await expect(page.getByTestId('code-mode-description')).toContainText(/直接调用工具|call tools directly/)
  expect((await mode.boundingBox())!.height).toBeGreaterThanOrEqual(40)

  const grep = page.getByTestId('tool-on-grep')
  await grep.click()
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await expect(grep).toBeEnabled()

  const changeMode = async (value: string) => {
    const saved = page.waitForResponse(response => response.url().includes('/v1/tools') && response.request().method() === 'PATCH')
    await mode.selectOption(value)
    expect((await saved).ok()).toBe(true)
    await expect(mode).toBeEnabled()
    await expect(mode).toHaveValue(value)
    await expect(grep).toHaveAttribute('aria-checked', 'false')
  }
  await changeMode('only')
  await page.reload()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-tools').click()
  await expect(mode).toHaveValue('only')
  await expect(mode).toBeEnabled()
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await changeMode('off')

  // A disabled-only update must preserve the selected mode in the other direction.
  const read = page.getByTestId('tool-on-read')
  await read.click()
  await expect(read).toHaveAttribute('aria-checked', 'false')
  await expect(read).toBeEnabled()
  await expect(mode).toHaveValue('off')
  await page.getByTestId('tools-reload').click()
  await expect(mode).toBeEnabled()
  await expect(mode).toHaveValue('off')
  await expect(grep).toHaveAttribute('aria-checked', 'false')
  await expect(read).toHaveAttribute('aria-checked', 'false')

  // Failure rolls back the selector, shows a retryable error, and changes no toggles.
  await page.route('**/v1/tools*', async route => {
    if (route.request().method() !== 'PATCH') return route.continue()
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'settings-save-unavailable' }) })
  })
  await mode.selectOption('mixed')
  await expect(page.getByTestId('tools-settings-error')).toContainText('settings-save-unavailable')
  await expect(mode).toHaveValue('off')
  await expect(mode).toBeEnabled()
  await page.unroute('**/v1/tools*')

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(mode).toBeVisible()
  const box = (await mode.boundingBox())!
  expect(box.height).toBeGreaterThanOrEqual(40)
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  const final = await request.get('/v1/tools', { headers })
  const settings = await final.json()
  expect(settings.codeMode).toBe('off')
  expect(settings.items.filter((item: { enabled: boolean }) => !item.enabled).map((item: { name: string }) => item.name)).toEqual(expect.arrayContaining(['grep', 'read']))
})
