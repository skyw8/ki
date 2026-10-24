import { expect, test, type Page } from '@playwright/test'
import { openSessionRow } from './sidebar.ts'
import { serverToken } from './global-setup'
import type { Entry } from '../src/api/types'

test.describe.configure({ mode: 'parallel' })

const cwd = '/repo'
const body = [
  'See [the plan](docs/plan.md) and [the window](docs/window.md#42-safe).',
  '',
  '![diagram](docs/tree.svg)',
  '',
  '[external](https://example.com/x) and [dangerous](javascript:alert(1))',
].join('\n')

function entries(): Entry[] {
  const stamp = 1_800_000_000_000
  return [
    { type: 'message', id: 'u', message: { role: 'user', timestamp: stamp, content: [{ type: 'text', text: 'show the links' }] } },
    { type: 'message', id: 'a', parentId: 'u', message: { role: 'assistant', timestamp: stamp + 10, stopReason: 'stop', content: [{ type: 'text', text: body }] } },
  ]
}

async function openTranscript(page: Page): Promise<string[]> {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `markdown-links-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  const previews: string[] = []
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({ json: { id, title, cwd, entries: entries(), running: false, leafId: 'a', oldestId: 'u', hasMore: false } })
    : route.fallback())
  await page.route(url => url.pathname === '/v1/fs', route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const path = new URL(route.request().url()).searchParams.get('path') ?? ''
    if (new URL(route.request().url()).searchParams.get('preview') === '1') {
      previews.push(path)
      return route.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', body: `preview of ${path}\n` })
    }
    return route.fallback()
  })
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  await page.goto('/')
  await openSessionRow(page, title)
  await expect(page.getByTestId('chat')).toBeVisible()
  return previews
}

test('relative markdown links open the workspace file preview instead of a blocked marker', async ({ page }) => {
  const previews = await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()
  await expect(assistant).not.toContainText('blocked')

  const plan = assistant.getByTestId('md-local-link').filter({ hasText: 'the plan' })
  await expect(plan).toHaveAttribute('data-path', '/repo/docs/plan.md')
  await plan.click()
  const preview = page.getByTestId('output-preview-body')
  await expect(preview).toContainText('preview of /repo/docs/plan.md')
  await page.getByTestId('output-preview-mask').click({ position: { x: 4, y: 4 } })
  await expect(preview).toHaveCount(0)

  const windowLink = assistant.getByTestId('md-local-link').filter({ hasText: 'the window' })
  await windowLink.click()
  await expect(page.getByTestId('output-preview-body')).toContainText('preview of /repo/docs/window.md')
  await page.getByTestId('output-preview-mask').click({ position: { x: 4, y: 4 } })

  expect(previews.filter(path => path.endsWith('.md'))).toEqual(['/repo/docs/plan.md', '/repo/docs/window.md'])
})

test('external links stay links and unsafe protocols degrade to plain text', async ({ page }) => {
  await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()
  await expect(assistant).not.toContainText('blocked')
  const external = assistant.getByRole('link', { name: 'external' })
  await expect(external).toHaveAttribute('href', 'https://example.com/x')
  await expect(external).toHaveAttribute('target', '_blank')
  const dangerous = assistant.getByText('dangerous')
  await expect(dangerous).toBeVisible()
  await expect(assistant.getByRole('link', { name: 'dangerous' })).toHaveCount(0)
  await expect(assistant.getByTestId('md-local-link').filter({ hasText: 'dangerous' })).toHaveCount(0)
})

test('relative markdown images render from the workspace instead of being blocked', async ({ page }) => {
  const previews = await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()
  const image = assistant.locator('img[data-path="/repo/docs/tree.svg"]')
  await expect(image).toHaveCount(1)
  await expect(image).toHaveAttribute('src', /^blob:/)
  await expect(assistant.getByTestId('md-local-image-missing')).toHaveCount(0)
  expect(previews).toEqual(['/repo/docs/tree.svg'])
})
