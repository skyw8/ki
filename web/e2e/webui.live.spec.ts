import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { statePath } from './run-state.ts'
import { expectContextItemCount, openContextCategory, openContextItem } from './context.ts'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

// Each case creates its own session; the tool case only reads the setup fixture.
test.describe.configure({ mode: 'parallel' })

test('live ping through chat and context', async ({ page }) => {
  const prompt = 'Reply with exactly the single word pong and nothing else.'
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText(/pong/i)
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect(page.getByTestId('toast')).toHaveCount(0)

  await page.getByTestId('tab-context').click()
  await expect(page.getByTestId('context-view')).toBeVisible()
  const human = await openContextCategory(page, 'human')
  await openContextItem(human.locator('.context-item').first())
  await expect(human).toContainText(prompt)
  const assistant = await openContextCategory(page, 'assistant')
  await openContextItem(assistant.locator('.context-item').last())
  await expect(assistant).toContainText(/pong/i)
  await page.getByTestId('context-step').first().click()
  await expectContextItemCount(page, 'assistant', 0)
})

test('live tool call shows in chat and context', async ({ page }) => {
  // A fresh home gives every session an empty temporary workspace, so the
  // fixture can only be reached by its absolute path (which the Read tool
  // accepts) rather than a workspace-relative name.
  const { cwd } = JSON.parse(readFileSync(statePath, 'utf8')) as { cwd: string }
  expect(cwd).toBeTruthy()
  const markerFile = join(cwd, 'pw-live.txt')
  const prompt = [
    'You must use the Read tool. Do not guess.',
    'Read the file ' + markerFile + ' and quote the marker token you find.',
    'Final answer on its own line: MARKER=<token>',
  ].join('\n')
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, prompt)

  await expect(page.locator('[data-testid="tool-card"][data-tool="read"]')).toBeVisible()
  await expect(page.getByTestId('assistant-message').last()).toContainText('KI-LIVE-MARKER-77')
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)

  await page.getByTestId('tab-context').click()
  await expect(page.getByTestId('context-view')).toBeVisible()
  const tools = await openContextCategory(page, 'tool')
  const readResult = tools.locator('.context-item').filter({ hasText: 'read' }).first()
  await openContextItem(readResult)
  await expect(readResult).toContainText('KI-LIVE-MARKER-77')
  const assistant = await openContextCategory(page, 'assistant')
  await openContextItem(assistant.locator('.context-item').last())
  await expect(assistant).toContainText('KI-LIVE-MARKER-77')
  await page.getByTestId('context-step').last().click()
  const requestTools = await openContextCategory(page, 'tool')
  await openContextItem(requestTools.locator('.context-item').filter({ hasText: 'read' }).first())
  await expect(requestTools.getByTestId('context-item-raw').first()).toContainText('KI-LIVE-MARKER-77')
})
