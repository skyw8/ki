import { expect, test, type Page } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// The sidebar (session list + workspaces) is pushed over GET /v1/events, so a
// client reflects another client's work without reloading. These tests drive two
// tabs against one server and never call page.reload().
//
// Every test is self-contained, so the parallel runner may split this file into
// one isolated process per test.
test.describe.configure({ mode: 'parallel' })

async function sendPrompt(page: Page, text: string): Promise<void> {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

// Same-origin helpers so a test can arrange state without the UI, mirroring the
// real client's CSRF handling.
async function api<T>(page: Page, path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  return page.evaluate(async ({ path, init }) => {
    const headers: Record<string, string> = {}
    const method = init?.method ?? 'GET'
    if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) {
      const csrf = document.cookie.split('; ').find(item => item.startsWith('ki_csrf='))?.slice('ki_csrf='.length)
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
  await expect(other.getByTestId('session-row')).toHaveCount(0)

  // Session creation is pushed: the second tab grows the row with no refresh.
  await page.getByTestId('new-session').click()
  await expect(other.getByTestId('session-row')).toHaveCount(1)

  // Run start is pushed as a sessions invalidation, so the dot comes on here
  // and in the other tab.
  await sendPrompt(page, 'e2e-hold push-dot-run')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await expect(page.locator('.session-row.active .dot.on')).toHaveCount(1)
  await expect(other.locator('[data-testid="session-row"] .dot.on')).toHaveCount(1)

  // Run end is pushed too: aborting clears the dot in both tabs.
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

    // Open the pushed-in session in the second tab. Scope by workspace: the
    // shared server keeps the previous test's session around.
    const group = other.getByTestId('workspace-group').filter({ hasText: ws.title })
    const sessionRow = group.getByTestId('session-row')
    await expect(sessionRow).toHaveCount(1)
    await sessionRow.first().click()
    await expect(other.getByTestId('composer-input')).toBeEnabled()

    // Deleting the workspace deletes its sessions; the tab showing one must
    // leave the transcript instead of displaying a session that is gone.
    await api(page, `/v1/workspaces/${ws.id}`, { method: 'DELETE' })
    await expect(other.getByTestId('hero')).toBeVisible()
    await expect(group).toHaveCount(0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
