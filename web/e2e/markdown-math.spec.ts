import { expect, test, type Page } from '@playwright/test'
import { openSessionRow } from './sidebar.ts'
import { serverToken } from './global-setup'
import type { Entry } from '../src/api/types'

test.describe.configure({ mode: 'parallel' })

const cwd = '/repo'
const body = [
  '令 \\(q_t\\) 为分量，价格 $5 保持原样。',
  '',
  '\\[',
  'L_{\\text{seq}}=\\frac{2}{\\beta}\\operatorname{softplus}',
  '\\left(\\beta\\,\\operatorname{mean}_t q_t\\right)',
  '\\]',
  '',
  '其中 $\\theta\\in\\mathbb{R}^n$ 是权重，$h>0$ 是步长。',
  '',
  '$$U_i(\\theta)=\\theta+h\\,v_i(\\theta)$$',
  '',
  '```tex',
  '\\(q_t\\) stays source',
  '```',
  '',
  '```ts',
  'const answer: number = 42',
  '```',
].join('\n')

function entries(): Entry[] {
  const stamp = 1_800_000_000_000
  return [
    { type: 'message', id: 'u', message: { role: 'user', timestamp: stamp, content: [{ type: 'text', text: 'show the math' }] } },
    { type: 'message', id: 'a', parentId: 'u', message: { role: 'assistant', timestamp: stamp + 10, stopReason: 'stop', content: [{ type: 'text', text: body }] } },
  ]
}

async function openTranscript(page: Page, routeBody = body): Promise<void> {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const response = await page.request.post('/v1/sessions', { headers, data: {} })
  const { id } = await response.json() as { id: string }
  const title = `markdown-math-${id}`
  await page.request.patch(`/v1/sessions/${id}`, { headers, data: { title } })
  await page.route(url => url.pathname === `/v1/sessions/${id}`, route => route.request().method() === 'GET'
    ? route.fulfill({
      json: {
        id,
        title,
        cwd,
        entries: routeBody === body ? entries() : entries().map(entry => entry.id === 'a'
          ? { ...entry, message: { ...entry.message, content: [{ type: 'text', text: routeBody }] } }
          : entry),
        running: false,
        leafId: 'a',
        oldestId: 'u',
        hasMore: false,
      },
    })
    : route.fallback())
  await page.addInitScript(() => localStorage.setItem('ki-lang', 'en'))
  await page.goto('/')
  await openSessionRow(page, title)
  await expect(page.getByTestId('chat')).toBeVisible()
}

test('latex delimiters render through katex inside the transcript', async ({ page }) => {
  await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()

  await expect(assistant.locator('.katex').first()).toBeVisible()
  await expect(assistant.locator('.katex-display')).toHaveCount(2)
  await expect(assistant).not.toContainText('$$')

  await expect(assistant.locator('.katex-mathml math')).toHaveCount(5)
  await expect(assistant.locator('.katex-html')).toHaveCount(5)
  await expect(assistant.locator('pre .katex')).toHaveCount(0)

  await expect(assistant).toContainText('$5')
  await expect(assistant.locator('.katex-error')).toHaveCount(0)
})

test('single dollar inline math renders without touching prices', async ({ page }) => {
  await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()

  await expect(assistant).not.toContainText('$\\theta')
  await expect(assistant).not.toContainText('$h>0$')
  await expect(assistant).toContainText('是权重')
  await expect(assistant).toContainText('是步长')

  await expect(assistant.locator('.katex-mathml math').filter({ hasText: '∈' })).toHaveCount(1)
  await expect(assistant).toContainText('价格 $5 保持原样')
})

test('code fences keep latex source and get highlighted tokens', async ({ page }) => {
  await openTranscript(page)
  const assistant = page.getByTestId('assistant-message').last()
  const blocks = assistant.locator('pre')
  await expect(blocks).toHaveCount(2)

  const tex = blocks.nth(0)
  await expect(tex).toContainText('\\(q_t\\) stays source')
  await expect(tex.locator('.katex')).toHaveCount(0)

  const ts = blocks.nth(1)
  await expect(ts).toContainText('const answer: number = 42')
  const colored = ts.locator('[style*="--shiki-light"]')
  await expect(colored.first()).toBeVisible()
  await expect(colored.filter({ hasText: 'const' })).toHaveCount(1)
})

test('a math error keeps the source instead of breaking the message', async ({ page }) => {
  await openTranscript(page, 'broken \\(\\frac{1}{\\) stays visible')
  const assistant = page.getByTestId('assistant-message').last()
  await expect(assistant).toContainText('stays visible')
  await expect(assistant.locator('.katex-error')).toHaveCount(1)
  await expect(assistant.locator('.katex-error')).toContainText('\\frac')
})
