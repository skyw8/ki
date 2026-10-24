import { expect, test } from '@playwright/test'
import { serverToken } from './global-setup.ts'

test.describe.configure({ mode: 'parallel' })

test('About shows the version and process identity of the serving daemon', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const meta = await (await request.get('/v1/meta', { headers })).json() as {
    version: string
    pid: number
  }
  expect(meta.version).toBeTruthy()
  expect(meta.pid).toBeGreaterThan(0)

  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-about').click()
  const about = page.getByTestId('about-settings')
  await expect(about).toBeVisible()
  const server = page.getByTestId('about-server')
  await expect(server).toContainText(meta.version)
  await expect(server).toContainText(String(meta.pid))

  await page.getByTestId('settings-tab-appearance').click()
  await page.getByTestId('lang-en').click()
  await page.getByTestId('settings-tab-about').click()
  await expect(about).toContainText('Server')
  await expect(server).toContainText(meta.version)
  await page.getByTestId('settings-tab-appearance').click()
  await page.getByTestId('lang-zh').click()
  await expect(page.getByTestId('settings-tab-about')).toHaveText('关于')
})

test('About is read-only and fits a phone viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')
  await page.getByTestId('mobile-nav-toggle').click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-about').click()
  const about = page.getByTestId('about-settings')
  await expect(about).toBeVisible()
  await expect(about.locator('button, input, textarea, select')).toHaveCount(0)
  const overflow = await about.evaluate(el => el.scrollWidth - el.clientWidth)
  expect(overflow, 'About must not overflow horizontally').toBeLessThanOrEqual(1)
})
