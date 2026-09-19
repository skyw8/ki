import { expect, test, type Page } from '@playwright/test'
import { loadHistory, requestTitle, userRequests } from '../src/lib/model.ts'
import type { ChatNode, Entry, IndexEntry } from '../src/api/types.ts'

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
  const entries: Entry[] = [
    { type: 'message', id: 'u1', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'same turn' }] } },
    { type: 'message', id: 'a1', parentId: 'u1', message: { role: 'assistant', content: [{ type: 'text', text: 'ok' }] } },
    { type: 'message', id: 'u2', parentId: 'a1', message: { role: 'user', content: [{ type: 'text', text: 'next\nline' }] } },
  ]
  expect(userRequests(entries, 'u2', nodes)).toEqual([
    { id: 'u1', title: 'same turn' },
    { id: 'u2', title: 'next' },
  ])
})

test('userRequests covers the whole branch from index rows and keeps a pending prompt', () => {
  // The conversation loaded only the newest prompt; the two older ones are
  // body-less index rows (a preview, no message body).
  const index: IndexEntry[] = [
    { type: 'message', id: 'u1', parentId: '', role: 'user', preview: 'first ask' },
    { type: 'message', id: 'a1', parentId: 'u1', role: 'assistant', preview: 'ok' },
    { type: 'message', id: 'u2', parentId: 'a1', role: 'user', preview: 'second ask' },
    { type: 'message', id: 'a2', parentId: 'u2', role: 'assistant', preview: 'ok' },
  ]
  const entries: Entry[] = [
    { type: 'message', id: 'u3', parentId: 'a2', message: { role: 'user', content: [{ type: 'text', text: 'third ask' }] } },
  ]
  const view = loadHistory({
    id: 's', cwd: '/tmp', provider: 'p', model: 'm', title: 't', leafId: 'u3', entries, index,
  })
  const pending: ChatNode[] = [{ kind: 'user', id: 'opt-user-9', text: 'just typed', content: [] }]

  expect(userRequests(view.allEntries, view.leafId, pending)).toEqual([
    { id: 'u1', title: 'first ask' },
    { id: 'u2', title: 'second ask' },
    { id: 'u3', title: 'third ask' },
    { id: 'opt-user-9', title: 'just typed' },
  ])
})

test('userRequests lists human prompts only', () => {
  const entries: Entry[] = [
    { type: 'message', id: 'u1', parentId: '', message: { role: 'user', content: [{ type: 'text', text: 'typed here' }] } },
    {
      type: 'message', id: 'u2', parentId: 'u1',
      // The Agent tool's completion notification: a user turn the runtime wrote.
      message: { role: 'user', origin: 'agent:task-1', content: [{ type: 'text', text: '<task-notification>…' }] },
    },
    {
      type: 'message', id: 'u3', parentId: 'u2',
      // A subagent's own directive, again runtime-written.
      message: { role: 'user', origin: 'agent', content: [{ type: 'text', text: 'You are a subagent…' }] },
    },
    {
      type: 'message', id: 'u4', parentId: 'u3',
      // Extension-relayed input is still a person's turn.
      message: { role: 'user', origin: 'extension:telegram-bot', content: [{ type: 'text', text: '[Telegram] hi' }] },
    },
  ]
  expect(userRequests(entries, 'u4').map(item => item.id)).toEqual(['u1', 'u4'])
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
