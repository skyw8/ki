import { expect, test } from '@playwright/test'

// Why: Chromium cannot emulate a software keyboard, but the layout contracts
// that keep mobile editing usable can be tested. Shrinking the viewport is
// what Android Chrome does under interactive-widget=resizes-content when the
// keyboard opens; the app must keep the focused field inside the remaining
// band, and message edit must start with the caret at the end of the text.
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
  // Why: the layout contract (the hero grid must clamp its track so the chip
  // ellipsizes) is independent of where the label comes from, so inject a long
  // one directly; data flow for real model names is covered elsewhere.
  await chip.evaluate(el => {
    el.textContent = 'workbuddy-ai/a-very-long-model-name-that-must-be-truncated'
  })
  await expect.poll(() => page.evaluate(() => {
    const hero = document.querySelector('.hero')
    return !!hero && hero.scrollWidth <= window.innerWidth && document.documentElement.scrollWidth <= window.innerWidth
  }, null, { message: 'hero must not overflow horizontally' })).toBe(true)
  // The clipped label proves the ellipsis, not a wider composer, absorbed it.
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
  // Why: programmatic focus leaves the caret at index 0; editing tweaks the
  // tail of the message, so the composer parks the caret at the end.
  const [caret, length] = await input.evaluate(el => {
    const textarea = el as HTMLTextAreaElement
    return [textarea.selectionStart, textarea.value.length] as const
  })
  expect(caret).toBe(length)
  await page.setViewportSize({ width: 390, height: BAND })
  await expectInBand(input, 'edit composer')
})
