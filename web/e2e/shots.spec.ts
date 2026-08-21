import { expect, test } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const dir = process.env.KI_SHOT_DIR || 'test-results/shots'

test('capture layout shots', async ({ page }) => {
  test.setTimeout(60_000)
  mkdirSync(dir, { recursive: true })
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.addInitScript(() => localStorage.setItem('ki-theme', 'light'))
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await page.screenshot({ path: `${dir}/01-hero.png` })

  await page.getByTestId('open-settings').click()
  await expect(page.getByTestId('settings')).toBeVisible()
  await page.screenshot({ path: `${dir}/02-settings.png` })
  await page.getByTestId('settings').getByRole('button', { name: '关闭对话框' }).click()
  await expect(page.getByTestId('settings')).toHaveCount(0)

  await page.getByTestId('open-model').click()
  await expect(page.getByTestId('model-dialog')).toBeVisible()
  await page.screenshot({ path: `${dir}/03-model.png` })
  await page.getByTestId('model-dialog').getByRole('button', { name: '关闭对话框' }).click()

  await page.getByTestId('composer-input').fill('layout shot')
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await page.screenshot({ path: `${dir}/04-chat.png` })

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  await page.screenshot({ path: `${dir}/05-trajectory.png` })

  await page.getByTestId('tab-config').click()
  await expect(page.getByTestId('session-info')).toBeVisible()
  await page.screenshot({ path: `${dir}/06-config.png` })
})
