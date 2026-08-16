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

test('workspace tree, pin, search, directory picker, to-bottom', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `ws-e2e ${Date.now()}`)
  await expect(page.getByTestId('workspace-row').first()).toBeVisible()
  await expect(page.getByTestId('session-row').first()).toBeVisible()

  await expect(page.locator('.ws-toolbar')).toBeVisible()
  await expect(page.locator('.ws-toolbar')).toContainText('工作区')
  await expect(page.getByTestId('add-workspace')).toBeVisible()
  await page.getByRole('button', { name: '搜索会话' }).click()
  await expect(page.getByTestId('session-search')).toBeVisible()
  await expect(page.locator('.header-actions')).toHaveClass(/hidden/)
  await page.getByRole('button', { name: '清除搜索' }).click()
  await expect(page.locator('.header-actions')).not.toHaveClass(/hidden/)
  await page.getByTestId('add-workspace').click()
  await expect(page.getByTestId('dir-browser')).toBeVisible()
  await expect(page.getByRole('navigation').getByRole('button', { name: '主目录' })).toBeVisible()
  await expect(page.getByTestId('dir-browser')).not.toContainText('/home/')
  await page.getByTestId('dir-new-folder').click()
  await expect(page.getByTestId('dir-create')).toBeVisible()
  await page.getByTestId('dir-create').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-create')).toHaveCount(0)
  await page.getByTestId('dir-path').click()
  const pathIn = page.getByLabel('编辑路径')
  await expect(pathIn).toBeVisible()
  await pathIn.fill('/tmp')
  await expect(page.getByTestId('dir-row').first()).toBeVisible({ timeout: 5000 })
  await page.getByTestId('dir-browser-mask').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-browser')).toHaveCount(0)

  await page.getByTestId('session-row').first().locator('button[aria-label="会话菜单"]').click()
  await page.getByRole('button', { name: '置顶' }).click()
  await expect(page.locator('.pin-mark')).toBeVisible()

  await page.getByRole('button', { name: '搜索会话' }).click()
  await page.getByTestId('session-search').fill('ws-e2e')
  await expect(page.getByTestId('search-hit').first()).toBeVisible()

  const tall = 'line\n'.repeat(80)
  await page.getByTestId('composer-input').fill(tall)
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('user-bubble').nth(1)).toBeVisible()
  const scroll = page.getByTestId('chat-scroll')
  await scroll.evaluate(el => { el.scrollTop = 0 })
  await expect(page.getByTestId('to-bottom')).toBeVisible()
  await page.getByTestId('to-bottom').click()
  await expect(page.getByTestId('to-bottom')).toHaveCount(0)
})
