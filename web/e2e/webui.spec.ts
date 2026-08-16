import { expect, test, type Page } from '@playwright/test'
import { applyFollowTail } from '../src/follow-tail.ts'

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
  await expect(page.getByTestId('settings')).toContainText('外观')
  await expect(page.getByTestId('settings-theme')).toBeVisible()
  await page.getByTestId('settings-mask').click({ position: { x: 4, y: 4 } })
  await expect(page.getByTestId('settings')).toHaveCount(0)
  await page.getByTestId('open-model').click()
  await expect(page.getByTestId('model-dialog')).toBeVisible()
})

test('chat and trajectory talk to the fake runtime', async ({ page }) => {
  const prompt = `hello from playwright ${Date.now()}`
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.getByTestId('session-title')).toContainText(prompt)
  const asstActions = page.getByTestId('assistant-message').getByTestId('asst-actions')
  await expect(asstActions.getByTestId('copy-msg')).toBeVisible()
  await expect(asstActions.getByTestId('fork-msg')).toBeVisible()
  await expect(asstActions.getByTestId('regen-msg')).toBeVisible()
  const userActions = page.getByTestId('user-actions')
  await expect(userActions.getByTestId('copy-msg')).toBeVisible()
  await expect(userActions.getByTestId('edit-msg')).toBeVisible()

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
  await page.locator('[data-testid="traj-row"][data-kind="assistant"]').first().click()
  await expect(page.getByTestId('insp-loc')).toContainText('Turn')
  await expect(page.getByTestId('insp-loc')).toContainText('Step')
  await expect(page.getByRole('button', { name: 'Summary' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Preview' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Raw' })).toBeVisible()
  await page.locator('[data-testid="traj-row"][data-kind="system"]').first().click()
  await expect(page.getByTestId('system-prompt')).not.toHaveText('—')
  await page.getByRole('button', { name: 'Tools' }).click()
  await expect(page.getByTestId('system-tools')).toContainText('Read')
  await page.getByRole('button', { name: 'Context' }).click()
  await expect(page.getByTestId('system-diff')).toBeVisible()

  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('traj-follow').click()
  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'false')
  await page.getByTestId('traj-follow').click()
  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'true')
  const atTail = await page.getByTestId('traj-table-wrap').evaluate(el =>
    Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop) < 2)
  expect(atTail).toBeTruthy()

  await page.reload()
  await page.getByTestId('session-row').first().click()
  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
})
