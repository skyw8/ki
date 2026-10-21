import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken, statePath } from './global-setup.ts'

// Every test opens the settings dialog and installs from the local mirror that
// global-setup published; nothing here mutates shared state beyond its own
// KI_HOME, so the parallel runner may split the file per test.
test.describe.configure({ mode: 'parallel' })

function homeDir(): string {
  return (JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }).home
}

function platformAsset(name: string): string {
  const goos = process.platform === 'win32' ? 'windows' : process.platform
  const goarch = process.arch === 'x64' ? 'amd64' : process.arch
  return `${name}-${goos}-${goarch}.zip`
}

async function openExtensions(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
}

test('settings installs an extension from the configured release', async ({ page, request }) => {
  const auth = { Authorization: `Bearer ${serverToken()}` }
  // Why: a previous spec in the same isolated home may have installed the
  // fixture already; remove it so this test owns the whole transition.
  const listed = await request.get('/v1/extensions/available', { headers: auth })
  const before = await listed.json() as { items?: { name: string; installed: boolean; asset: string }[] }
  const goalBefore = before.items?.find(item => item.name === 'goal')
  expect(goalBefore?.installed).toBe(false)
  expect(goalBefore?.asset).toBe(platformAsset('goal'))

  await openExtensions(page)
  const row = page.getByTestId('ext-available-goal')
  await expect(row).toBeVisible()
  await expect(row).toContainText('0.1.0')
  await expect(page.getByTestId('extensions-available')).toContainText('v0.0.0-test')

  const started = page.waitForResponse(response =>
    response.url().includes('/v1/extensions/goal/install') && response.request().method() === 'POST',
  )
  await page.getByTestId('ext-install-goal').click()
  expect((await started).status()).toBe(202)

  // Discovery picks the package up after the server reloads, which is the same
  // signal the toggle list above uses.
  await expect(page.getByTestId('cfg-extension').filter({ hasText: 'goal' })).toBeVisible({ timeout: 20_000 })
  await expect(row).toHaveCount(0)

  const after = await request.get('/v1/extensions/available', { headers: auth })
  const catalog = await after.json() as { items?: { name: string; installed: boolean; installedVersion?: string }[] }
  const goal = catalog.items?.find(item => item.name === 'goal')
  expect(goal?.installed).toBe(true)
  expect(goal?.installedVersion).toBe('0.1.0')
  // The archive's single wrapping directory must be stripped on unpack.
  expect(readFileSync(join(homeDir(), 'extensions', 'goal', 'prompt', 'APPEND.md'), 'utf8')).toContain('fixture install prompt')
  await expect(page.getByTestId('extension-on-goal')).toHaveAttribute('aria-checked', 'true')
})

test('install failure stays visible and does not publish a package', async ({ page, request }) => {
  const auth = { Authorization: `Bearer ${serverToken()}` }
  // A catalog name whose asset the mirror does not publish: the checksum lookup
  // fails before anything reaches KI_HOME/extensions.
  const missing = 'codex-oauth'
  const res = await request.post(`/v1/extensions/${missing}/install`, { headers: auth })
  expect(res.status()).toBe(202)
  await expect.poll(async () => {
    const listed = await request.get('/v1/extensions/available', { headers: auth })
    const body = await listed.json() as { items?: { name: string; install?: { state: string } }[] }
    return body.items?.find(item => item.name === missing)?.install?.state
  }, { timeout: 20_000 }).toBe('failed')

  await openExtensions(page)
  const row = page.getByTestId(`ext-available-${missing}`)
  await expect(row).toBeVisible()
  await expect(page.getByTestId(`ext-install-error-${missing}`)).toBeVisible()
  await expect(page.getByTestId(`ext-install-${missing}`)).toHaveText(/重试|Retry/)
  // The failed download must not leave a discoverable package behind.
  await expect(page.getByTestId('cfg-extension').filter({ hasText: missing })).toHaveCount(0)
})
