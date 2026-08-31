import { expect, test, type Page } from '@playwright/test'
import { requestTitle, userRequests } from '../src/model.ts'
import type { ChatNode } from '../src/types.ts'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test('requestTitle prefers the first line then attachment names', () => {
  expect(requestTitle('  hello world  \nsecond')).toBe('hello world')
  expect(requestTitle('', [{ type: 'image', name: 'shot.png' }])).toBe('shot.png')
  expect(requestTitle('', [{ type: 'file', path: '/tmp/notes.md' }])).toBe('/tmp/notes.md')
  expect(requestTitle('   ')).toBe('')
})

test('userRequests walks the active path and drops optimistic duplicates', () => {
  const nodes: ChatNode[] = [
    { kind: 'user', id: 'opt-user-1', text: 'same turn', content: [] },
    { kind: 'user', id: 'u1', text: 'same turn', content: [] },
    { kind: 'assistant', id: 'a1', text: 'ok' },
    { kind: 'user', id: 'u2', text: 'next\nline', content: [] },
    { kind: 'tool', id: 't1', name: 'Bash' },
  ]
  expect(userRequests(nodes)).toEqual([
    { id: 'u1', title: 'same turn' },
    { id: 'u2', title: 'next' },
  ])
})

test('request navigator jumps to an earlier user turn', async ({ page }) => {
  test.setTimeout(45_000)
  const prompts = ['nav-alpha-unique', 'nav-beta-unique', 'nav-gamma-unique', 'nav-delta-unique', 'nav-epsilon-unique']
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  for (const prompt of prompts) {
    await sendPrompt(page, prompt)
    await expect(page.getByTestId('assistant-message').last()).toContainText('ok')
  }

  const toggle = page.getByTestId('request-nav-toggle')
  await expect(toggle).toBeVisible()
  await toggle.click()
  const panel = page.getByTestId('request-nav-panel')
  await expect(panel).toBeVisible()
  await expect(panel.getByTestId('request-nav-item')).toHaveCount(prompts.length)
  for (const prompt of prompts) {
    await expect(panel.getByTestId('request-nav-item').filter({ hasText: prompt })).toBeVisible()
  }

  const firstBubble = page.getByTestId('user-bubble').first()
  const before = await firstBubble.evaluate(el => {
    const scroll = el.closest('[data-testid="chat-scroll"]') as HTMLElement
    return el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })
  expect(before).toBeLessThan(-8)

  await panel.getByTestId('request-nav-item').filter({ hasText: prompts[0] }).click()
  await expect.poll(async () => firstBubble.evaluate(el => {
    const scroll = el.closest('[data-testid="chat-scroll"]') as HTMLElement
    return el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })).toBeLessThan(96)
  await expect(page.getByTestId('request-nav-item').filter({ hasText: prompts[0] })).toHaveClass(/active/)
})
