import { expect, test, type Page } from '@playwright/test'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test.describe.configure({ mode: 'serial' })

test('page boots and settings stub is empty', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await expect(page.getByRole('heading', { name: '开始对话' })).toBeVisible()
  await expect(page.getByTestId('composer-input')).toBeVisible()
  await page.getByTestId('open-settings').click()
  await expect(page.getByTestId('settings')).toContainText('设置项稍后接入')
})

test('chat and trajectory talk to the fake runtime', async ({ page }) => {
  const prompt = `hello from playwright ${Date.now()}`
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.getByTestId('session-title')).toContainText(prompt)

  const listed = await page.evaluate(async () => {
    const token = (window as unknown as { __KI__?: { token?: string } }).__KI__?.token ?? ''
    const res = await fetch('/v1/sessions', { headers: { Authorization: `Bearer ${token}` } })
    if (!res.ok) throw new Error(`list ${res.status}`)
    return res.json() as Promise<Array<{ title?: string }>>
  })
  expect(listed.some(s => (s.title ?? '').includes(prompt))).toBeTruthy()

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="user"]')).toContainText(prompt)
  await expect(page.locator('[data-testid="traj-row"][data-kind="assistant"]')).toContainText('ok')

  await page.reload()
  await page.getByTestId('session-row').first().click()
  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
})
