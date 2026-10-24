import { expect, test } from '@playwright/test'

test.describe.configure({ mode: 'parallel' })
test.use({ viewport: { width: 390, height: 844 } })

const BAND = 434

async function expectInBand(locator: ReturnType<import('@playwright/test').Page['locator']>, label: string): Promise<void> {
  await expect.poll(async () => {
    const box = await locator.boundingBox()
    return !!box && box.y >= -1 && box.y + box.height <= BAND + 1
  }, { message: `${label} should be revealed inside the band above the keyboard` }).toBe(true)
}

test('hero composer with a long model name fits the phone viewport', async ({ page }) => {
  await page.goto('/')
  const chip = page.getByTestId('open-model')
  await expect(chip).toBeVisible()
  await chip.evaluate(el => {
    el.textContent = 'workbuddy-ai/a-very-long-model-name-that-must-be-truncated'
  })
  await expect.poll(() => page.evaluate(() => {
    const hero = document.querySelector('.hero')
    return !!hero && hero.scrollWidth <= window.innerWidth && document.documentElement.scrollWidth <= window.innerWidth
  }, null, { message: 'hero must not overflow horizontally' })).toBe(true)
  await expect.poll(() => chip.evaluate(el => el.scrollWidth > el.clientWidth + 1), null, { message: 'long model name should be truncated' }).toBe(true)
})

test('focused provider field stays in the visible band when the keyboard shrinks the viewport', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('mobile-nav-toggle').click()
  await page.getByTestId('open-settings').click()
  await page.locator('.provider-nav button').first().click()
  const input = page.getByTestId('provider-connection-form').locator('input').first()
  await input.scrollIntoViewIfNeeded()
  await input.click()
  await expect(input).toBeFocused()
  await page.setViewportSize({ width: 390, height: BAND })
  await expectInBand(input, 'focused provider field')
})

test('message edit focuses with the caret at the end and stays in the band', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('composer-input').fill('keyboard caret check')
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('assistant-message')).toBeVisible()
  await page.getByTestId('edit-msg').click()
  const input = page.getByTestId('edit-input')
  await expect(input).toBeFocused()
  const [caret, length] = await input.evaluate(el => {
    const textarea = el as HTMLTextAreaElement
    return [textarea.selectionStart, textarea.value.length] as const
  })
  expect(caret).toBe(length)
  await page.setViewportSize({ width: 390, height: BAND })
  await expectInBand(input, 'edit composer')
})
