import { expect, test, type Page } from '@playwright/test'
import { expandWorkspaces } from './sidebar.ts'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { newSession } from './session.ts'

test.describe.configure({ mode: 'parallel' })

async function sendPrompt(page: Page, text: string): Promise<void> {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

async function api<T>(page: Page, path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  return page.evaluate(async ({ path, init }) => {
    const headers: Record<string, string> = {}
    const method = init?.method ?? 'GET'
    if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) {
      const { csrfCookieName } = await (await fetch('/v1/auth/status', { cache: 'no-store' })).json() as { csrfCookieName: string }
      const prefix = `${csrfCookieName}=`
      const csrf = document.cookie.split(';').map(item => item.trim()).find(item => item.startsWith(prefix))?.slice(prefix.length)
      if (csrf) headers['X-Ki-CSRF'] = decodeURIComponent(csrf)
      headers['Content-Type'] = 'application/json'
    }
    const res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: init?.body != null ? JSON.stringify(init.body) : undefined,
    })
    if (!res.ok) throw new Error(`${method} ${path} ${res.status} ${await res.text()}`)
    return res.status === 204 ? (undefined as T) : (await res.json() as T)
  }, { path, init })
}

test('a run in another tab turns the sidebar dot green and back without a reload', async ({ page, context }) => {
  const other = await context.newPage()
  await page.goto('/')
  await other.goto('/')
  await expandWorkspaces(page)
  await expandWorkspaces(other)
  await expect(other.getByTestId('session-row')).toHaveCount(0)

  await newSession(page)
  await expandWorkspaces(other)
  await expect(other.getByTestId('session-row')).toHaveCount(1)

  await sendPrompt(page, 'e2e-hold push-dot-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await expect(page.locator('.session-row.active .dot.on')).toHaveCount(1)
  await expect(other.locator('[data-testid="session-row"] .dot.on')).toHaveCount(1)

  await page.getByTestId('composer-stop').click()
  await expect(page.locator('.session-row.active .dot.on')).toHaveCount(0)
  await expect(other.locator('[data-testid="session-row"] .dot.on')).toHaveCount(0)
})

test('a workspace deleted in another tab disappears without a reload', async ({ page, context }) => {
  const other = await context.newPage()
  await page.goto('/')
  await other.goto('/')

  const dir = mkdtempSync(join(tmpdir(), 'ki-push-ws-'))
  try {
    const ws = await api<{ id: string; title: string }>(page, '/v1/workspaces', { method: 'POST', body: { path: dir } })
    const row = other.locator('[data-testid="workspace-row"]', { hasText: ws.title })
    await expect(row).toHaveCount(1)

    await api(page, `/v1/workspaces/${ws.id}`, { method: 'DELETE' })
    await expect(row).toHaveCount(0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('a session deleted with its workspace closes in another tab', async ({ page, context }) => {
  const other = await context.newPage()
  await page.goto('/')
  await other.goto('/')

  const dir = mkdtempSync(join(tmpdir(), 'ki-push-del-'))
  try {
    const ws = await api<{ id: string; title: string }>(page, '/v1/workspaces', { method: 'POST', body: { path: dir } })
    await api(page, '/v1/sessions', { method: 'POST', body: { cwd: dir } })

    await expandWorkspaces(other)
    const group = other.getByTestId('workspace-group').filter({ hasText: ws.title })
    const sessionRow = group.getByTestId('session-row')
    await expect(sessionRow).toHaveCount(1)
    await sessionRow.first().click()
    await expect(other.getByTestId('composer-input')).toBeEnabled()

    await api(page, `/v1/workspaces/${ws.id}`, { method: 'DELETE' })
    await expect(other.getByTestId('hero')).toBeVisible()
    await expect(group).toHaveCount(0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
