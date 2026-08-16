import { expect, test, type Page } from '@playwright/test'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test.describe.configure({ mode: 'serial' })

test('live ping through chat and trajectory', async ({ page }) => {
  const prompt = 'Reply with exactly the single word pong and nothing else.'
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText(/pong/i)
  await expect(page.getByTestId('notice')).toHaveCount(0)

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="user"]')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="assistant"]')).toContainText(/pong/i)
})

test('live tool call shows in chat and trajectory', async ({ page }) => {
  const prompt = [
    'You must use the Read tool. Do not guess.',
    'Read the file pw-live.txt in the current workspace and quote the marker token you find.',
    'Final answer on its own line: MARKER=<token>',
  ].join('\n')
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.getByTestId('tool-card')).toBeVisible()
  await expect(page.getByTestId('assistant-message').last()).toContainText('KI-LIVE-MARKER-77')

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="tool"]')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="assistant"]').last()).toContainText('KI-LIVE-MARKER-77')
})
