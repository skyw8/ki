import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { expandWorkspaces } from './sidebar.ts'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken, statePath } from './global-setup.ts'

test.describe.configure({ mode: 'parallel' })

function testHome(): string {
  return (JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }).home
}

async function openPromptSettings(page: Page, request: APIRequestContext): Promise<void> {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const marker = `prompt-settings-${Date.now()}-${Math.random().toString(16).slice(2)}`
  const created = await request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBe(true)
  const { id } = (await created.json()) as { id: string }
  const titled = await request.patch(`/v1/sessions/${id}`, { headers, data: { title: marker } })
  expect(titled.ok()).toBe(true)

  await page.goto('/')
  await expandWorkspaces(page)
  const row = page.getByTestId('session-row').filter({ hasText: marker })
  await expect(row).toBeVisible()
  await row.click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-prompt').click()
  await expect(page.getByTestId('prompt-settings')).toBeVisible()
}

test('prompt settings list every source and edit the global file', async ({ page, request }) => {
  await openPromptSettings(page, request)

  const builtin = page.getByTestId('prompt-builtin')
  await expect(builtin).toContainText("In shell commands or pipelines, prefer 'rg' and 'fd' over 'grep' and 'find'")
  await expect(page.getByTestId('prompt-edit-builtin')).toHaveCount(0)
  await expect(page.getByTestId('prompt-source-builtin')).toContainText(/只读|Read-only/)
  await expect(page.getByTestId('prompt-effective')).toContainText('on an unsupported target')

  const editor = page.getByTestId('prompt-edit-global')
  await editor.fill('GLOBAL-E2E-RULE')
  await expect(page.getByTestId('prompt-bytes-global')).toContainText(`15 / ${64 * 1024}`)
  await page.getByTestId('prompt-save-global').click()

  await expect(page.getByTestId('prompt-effective')).toContainText('GLOBAL-E2E-RULE')
  await expect(page.getByTestId('prompt-source-global')).toContainText(/已创建|Created/)
  const globalPath = join(testHome(), 'prompt', 'APPEND_SYSTEM.md')
  expect(readFileSync(globalPath, 'utf8')).toBe('GLOBAL-E2E-RULE')

  page.on('dialog', dialog => void dialog.accept())
  await page.getByTestId('prompt-delete-global').click()
  await expect(page.getByTestId('prompt-effective')).not.toContainText('GLOBAL-E2E-RULE')
  await expect(page.getByTestId('prompt-source-global')).toContainText(/未创建|Not created/)
  expect(existsSync(globalPath)).toBe(false)
})

test('project file adds to the global layer', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const saved = await request.put('/v1/prompt/append', { headers, data: { source: 'global', text: 'GLOBAL-E2E-RULE' } })
  expect(saved.ok()).toBe(true)

  await openPromptSettings(page, request)
  await page.getByTestId('prompt-edit-project').fill('PROJECT-E2E-RULE')
  await page.getByTestId('prompt-save-project').click()

  const effective = page.getByTestId('prompt-effective')
  await expect(effective).toContainText('PROJECT-E2E-RULE')
  const text = (await effective.textContent()) ?? ''
  const builtinAt = text.indexOf("In shell commands or pipelines, prefer 'rg' and 'fd' over 'grep' and 'find'")
  const globalAt = text.indexOf('GLOBAL-E2E-RULE')
  const projectAt = text.indexOf('PROJECT-E2E-RULE')
  expect(builtinAt, `effective order: ${text}`).toBeGreaterThanOrEqual(0)
  expect(globalAt).toBeGreaterThan(builtinAt)
  expect(projectAt).toBeGreaterThan(globalAt)

  const projectPath = (await page.getByTestId('prompt-source-project').locator('.prompt-source-path').textContent()) ?? ''
  expect(projectPath.endsWith(join('.ki', 'prompt', 'APPEND_SYSTEM.md'))).toBe(true)
  expect(readFileSync(projectPath, 'utf8')).toBe('PROJECT-E2E-RULE')
})

test('empty prompt text is rejected', async ({ page, request }) => {
  await openPromptSettings(page, request)
  const path = join(testHome(), 'prompt', 'APPEND_SYSTEM.md')
  const before = existsSync(path) ? readFileSync(path, 'utf8') : null
  await page.getByTestId('prompt-edit-global').fill('   ')
  await page.getByTestId('prompt-save-global').click()
  await expect(page.getByTestId('toast')).toContainText(/内容为空|text is empty/)
  const after = existsSync(path) ? readFileSync(path, 'utf8') : null
  expect(after).toBe(before)
})
