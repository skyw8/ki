import { expect, test } from '@playwright/test'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { serverToken, statePath } from './global-setup.ts'
import type { ToolSettings } from '../src/api/types'

// The parallel runner gives this case its own home, workspace and server.
test.describe.configure({ mode: 'parallel' })

test('MCP settings toggle configured servers without connecting or replacing tool settings', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const headers = { Authorization: `Bearer ${serverToken()}` }
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-mcp').click()
  await expect(page.getByTestId('mcp-settings')).toContainText('.ki/mcp.json')
  await page.getByTestId('settings').getByRole('button', { name: '关闭对话框' }).click()

  // Create the session before installing servers: no prompt in this case needs MCP.
  await page.getByTestId('composer-input').fill('mcp-settings-fixture')
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  const sessions = await (await request.get('/v1/sessions', { headers })).json()
  const id = sessions[0].id as string
  // Default sessions get their own temporary workspace, not the daemon's cwd.
  const cwd = sessions[0].cwd as string
  const marker = join(cwd, 'mcp-must-not-connect')
  const command = { command: process.execPath, args: ['-e', `require('node:fs').writeFileSync(${JSON.stringify(marker)},'connected')`] }
  const globalServers = {
    docs: command,
    fileOff: { url: 'http://127.0.0.1:9/mcp', enabled: false },
    local: command,
  }
  writeFileSync(join(home, 'mcp.json'), JSON.stringify({ version: 1, mcpServers: { ...globalServers, hidden: command } }))
  mkdirSync(join(cwd, '.ki'), { recursive: true })
  writeFileSync(join(cwd, '.ki', 'mcp.json'), JSON.stringify({ version: 1, mcpServers: {
    docs: { url: 'http://127.0.0.1:9/mcp' },
  } }))
  const endpoint = `/v1/tools?sessionId=${encodeURIComponent(id)}`
  const saved = await request.patch(endpoint, { headers, data: { disabled: ['grep'], mcpDisabled: ['hidden'] } })
  expect(saved.ok()).toBe(true)
  // A server absent from this workspace's editable catalog keeps its global override.
  writeFileSync(join(home, 'mcp.json'), JSON.stringify({ version: 1, mcpServers: globalServers }))

  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-mcp').click()
  const mcp = page.getByTestId('mcp-settings')
  const docs = page.getByTestId('mcp-on-docs')
  const fileOff = page.getByTestId('mcp-on-fileOff')
  await expect(docs).toBeEnabled()
  await expect(fileOff).toBeDisabled()
  await expect(fileOff).toHaveAttribute('aria-checked', 'false')
  await expect(mcp.locator('[data-name="docs"]')).toContainText('项目')
  await expect(mcp.locator('[data-name="docs"]')).toContainText('Streamable HTTP')
  await expect(mcp.locator('[data-name="local"]')).toContainText('全局')
  await expect(mcp.locator('[data-name="local"]')).toContainText(/stdio/)
  await expect(mcp.locator('[data-name="fileOff"]')).toContainText('配置文件中已禁用')

  const patch = page.waitForRequest(req => req.url().includes('/v1/tools') && req.method() === 'PATCH')
  await docs.click()
  expect((await patch).postDataJSON()).toEqual({ mcpDisabled: ['docs'] })
  await expect(docs).toBeEnabled()
  await expect(docs).toHaveAttribute('aria-checked', 'false')
  const settings = await (await request.get(endpoint, { headers })).json() as ToolSettings
  expect(settings).not.toHaveProperty('codeMode')
  expect(settings.items.find(item => item.name === 'grep')?.enabled).toBe(false)
  expect(settings.mcp.find(item => item.name === 'docs')?.enabled).toBe(false)
  const toggles = JSON.parse(readFileSync(join(home, 'toggles.json'), 'utf8'))
  expect(toggles.mcp.disabled).toEqual(expect.arrayContaining(['docs', 'hidden']))

  await page.reload()
  // Reload does not implicitly select a session; Info needs the same explicit owner.
  await page.getByTestId('session-title').click()
  await expect(page.locator('.session-row.active')).toHaveCount(1)
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-mcp').click()
  await expect(docs).toHaveAttribute('aria-checked', 'false')
  await expect(docs).toBeEnabled()
  await page.route('**/v1/tools*', async route => {
    if (route.request().method() !== 'PATCH') return route.continue()
    await route.fulfill({ status: 503, json: { error: 'mcp-settings-save-unavailable' } })
  })
  await docs.click()
  await expect(page.getByTestId('mcp-settings-error')).toContainText('mcp-settings-save-unavailable')
  await expect(docs).toHaveAttribute('aria-checked', 'false')
  await expect(docs).toBeEnabled()
  await page.getByTestId('toaster').getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByTestId('toaster')).toHaveCount(0)
  await page.unroute('**/v1/tools*')

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(docs).toBeVisible()
  const box = (await docs.boundingBox())!
  expect(box.height).toBeGreaterThanOrEqual(44)
  expect(box.width).toBeGreaterThanOrEqual(44)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  await page.getByTestId('settings').getByRole('button', { name: '关闭对话框' }).click()
  await page.getByTestId('tab-config').click()
  const info = page.getByTestId('cfg-mcp-info').filter({ hasText: 'docs' })
  await expect(info).toContainText('项目')
  await expect(info).toContainText('Streamable HTTP')
  await expect(info).toContainText('停用')
  await expect(info).toContainText(/工具/)
  await expect(page.getByTestId('info-outline')).toContainText('MCP')
  await expect(page.getByTestId('info-outline')).toContainText('docs')
  await expect(page.locator('#info-mcp')).toContainText('下次运行')
  await expect(info.locator('button, a')).toHaveCount(0)
  expect(existsSync(marker), 'catalog and Info reads must not start stdio clients').toBe(false)
})
