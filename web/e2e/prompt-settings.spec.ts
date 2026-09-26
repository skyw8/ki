import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken, statePath } from './global-setup.ts'

// Every test here is self-contained: the parallel runner may split the file into
// one isolated fake server per test, and each test writes only to its own
// KI_HOME and workspace.
test.describe.configure({ mode: 'parallel' })

function testHome(): string {
  return (JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }).home
}

async function openPromptSettings(page: Page, request: APIRequestContext): Promise<void> {
  // Open a session through the API and select its row instead of newSession():
  // this spec can share a server with earlier tests, where the sidebar list
  // renders after the helper has already counted rows.
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const marker = `prompt-settings-${Date.now()}-${Math.random().toString(16).slice(2)}`
  const created = await request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBe(true)
  const { id } = (await created.json()) as { id: string }
  const titled = await request.patch(`/v1/sessions/${id}`, { headers, data: { title: marker } })
  expect(titled.ok()).toBe(true)

  await page.goto('/')
  const row = page.getByTestId('session-row').filter({ hasText: marker })
  await expect(row).toBeVisible()
  await row.click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-prompt').click()
  await expect(page.getByTestId('prompt-settings')).toBeVisible()
}

test('prompt settings list every source and edit the global file', async ({ page, request }) => {
  await openPromptSettings(page, request)

  // The built-in layer is harness-owned: shown, in the effective stack, never
  // editable.
  const builtin = page.getByTestId('prompt-builtin')
  await expect(builtin).toContainText("NEVER use 'grep' or 'find'")
  await expect(page.getByTestId('prompt-edit-builtin')).toHaveCount(0)
  await expect(page.getByTestId('prompt-source-builtin')).toContainText(/只读|Read-only/)
  await expect(page.getByTestId('prompt-effective')).toContainText("only supported search tools")

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

// The project file must not hide the global one: both layers reach the prompt,
// global first, and the project path is the selected workspace's .ki tree.
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
  const builtinAt = text.indexOf("NEVER use 'grep' or 'find'")
  const globalAt = text.indexOf('GLOBAL-E2E-RULE')
  const projectAt = text.indexOf('PROJECT-E2E-RULE')
  expect(builtinAt, `effective order: ${text}`).toBeGreaterThanOrEqual(0)
  expect(globalAt).toBeGreaterThan(builtinAt)
  expect(projectAt).toBeGreaterThan(globalAt)

  const projectPath = (await page.getByTestId('prompt-source-project').locator('.prompt-source-path').textContent()) ?? ''
  expect(projectPath.endsWith(join('.ki', 'prompt', 'APPEND_SYSTEM.md'))).toBe(true)
  expect(readFileSync(projectPath, 'utf8')).toBe('PROJECT-E2E-RULE')
})

// A blank editor would render nothing while leaving a file behind, so the
// server rejects it and the UI offers delete instead.
test('empty prompt text is rejected', async ({ page, request }) => {
  await openPromptSettings(page, request)
  // The home is shared when the suite runs in one process, so compare the file
  // before and after instead of assuming it does not exist.
  const path = join(testHome(), 'prompt', 'APPEND_SYSTEM.md')
  const before = existsSync(path) ? readFileSync(path, 'utf8') : null
  await page.getByTestId('prompt-edit-global').fill('   ')
  await page.getByTestId('prompt-save-global').click()
  await expect(page.getByTestId('toast')).toContainText(/内容为空|text is empty/)
  const after = existsSync(path) ? readFileSync(path, 'utf8') : null
  expect(after).toBe(before)
})
