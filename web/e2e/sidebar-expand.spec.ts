import { expect, test } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expandWorkspaces } from './sidebar.ts'

test.describe.configure({ mode: 'parallel' })

async function createWorkspace(page: import('@playwright/test').Page, prefix: string) {
  const dir = mkdtempSync(join(tmpdir(), prefix))
  const headers = { Authorization: `Bearer ${await token(page)}` }
  const res = await page.request.post('/v1/workspaces', { headers, data: { path: dir } })
  expect(res.ok()).toBe(true)
  return { dir, ws: await res.json() as { id: string; title: string } }
}

async function token(page: import('@playwright/test').Page): Promise<string> {
  const { serverToken } = await import('./global-setup.ts')
  return serverToken()
}

test('workspaces start collapsed, so idle sessions stay out of the way', async ({ page }) => {
  const { dir, ws } = await createWorkspace(page, 'ki-sidebar-collapsed-')
  try {
    const headers = { Authorization: `Bearer ${await token(page)}` }
    const created = await page.request.post('/v1/sessions', { headers, data: { cwd: dir } })
    expect(created.ok()).toBe(true)
    const { id } = await created.json() as { id: string }
    await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title: 'idle-collapsed' } })

    await page.goto('/')
    const group = page.getByTestId('workspace-group').filter({ hasText: ws.title })
    const toggle = group.locator('.ws-toggle')
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(group.getByTestId('session-row')).toHaveCount(0)

    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await expect(group.getByTestId('session-row').filter({ hasText: 'idle-collapsed' })).toBeVisible()
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(group.getByTestId('session-row')).toHaveCount(0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('a running session opens its workspace by itself', async ({ page }) => {
  const { dir, ws } = await createWorkspace(page, 'ki-sidebar-running-')
  try {
    const headers = { Authorization: `Bearer ${await token(page)}` }
    const created = await page.request.post('/v1/sessions', { headers, data: { cwd: dir } })
    expect(created.ok()).toBe(true)
    const { id } = await created.json() as { id: string }
    await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title: 'running-auto' } })

    await page.goto('/')
    const group = page.getByTestId('workspace-group').filter({ hasText: ws.title })
    const toggle = group.locator('.ws-toggle')
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')

    await expandWorkspaces(page)
    await group.getByTestId('session-row').filter({ hasText: 'running-auto' }).locator('.session-main').click()
    const input = page.getByTestId('composer-input')
    await expect(input).toBeEnabled()
    await input.fill('e2e-hold sidebar-auto')
    await page.getByTestId('composer-send').click()
    await expect(page.getByTestId('composer-stop')).toBeVisible()

    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(group.getByTestId('session-row')).toHaveCount(0)

    await page.reload()
    const reloaded = page.getByTestId('workspace-group').filter({ hasText: ws.title })
    await expect(reloaded.locator('.ws-toggle')).toHaveAttribute('aria-expanded', 'true')
    await expect(reloaded.getByTestId('session-row')).toHaveCount(1)

    await reloaded.getByTestId('session-row').locator('.session-main').click()
    await page.getByTestId('composer-stop').click()
    await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
