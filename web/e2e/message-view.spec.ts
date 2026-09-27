import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { statePath } from './global-setup.ts'
import { clampCompactKeep, foldReplies, groupTurns } from '../src/lib/messageView.ts'
import type { ChatNode } from '../src/api/types.ts'

const user = (id: string, text: string): ChatNode => ({ kind: 'user', id, text, content: [] })
const asst = (id: string, text = 'ok'): ChatNode => ({ kind: 'assistant', id, text })
const tool = (id: string, running = false): ChatNode => ({ kind: 'tool', id, name: 'Bash', args: {}, running })

test('groupTurns splits at user nodes and keeps a leading group', () => {
  const turns = groupTurns([asst('a0'), user('u1', 'one'), asst('a1'), user('u2', 'two'), asst('a2')])
  expect(turns.map(t => t.id)).toEqual(['a0', 'u1', 'u2'])
  expect(turns[0].user).toBeUndefined()
  expect(turns[1].user?.id).toBe('u1')
  expect(turns[1].nodes.map(n => n.id)).toEqual(['u1', 'a1'])
  expect(turns[2].nodes.map(n => n.id)).toEqual(['u2', 'a2'])
})

test('foldReplies keeps every user bubble and the newest keep replies of each turn', () => {
  const nodes = [
    user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b'),
    user('u2', 'two'), asst('a2a'), tool('t2'), asst('a2b'),
  ]
  // One reply kept per turn: the fold row sits between the prompt and it.
  expect(foldReplies(nodes, { keep: 1 }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1b',
    'u2', 'fold:u2', 'a2b',
  ])
  const folded = foldReplies(nodes, { keep: 1 }).find(i => i.kind === 'fold')
  expect(folded && folded.kind === 'fold' ? folded.nodes.map(n => n.id) : []).toEqual(['a1a', 't1'])

  // keep 0 folds every reply, keep 2 leaves the short turns alone.
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 'u2', 'fold:u2'])
  expect(foldReplies(nodes, { keep: 3 }).map(i => i.id)).toEqual(nodes.map(n => n.id))
  // A turn that is only a prompt has nothing to fold.
  expect(foldReplies([user('u1', 'one')], { keep: 0 }).map(i => i.id)).toEqual(['u1'])
})

test('foldReplies opens a fold in place without reordering the turn', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b')]
  expect(foldReplies(nodes, { keep: 1, expanded: new Set(['u1']) }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'a1a', 't1', 'a1b',
  ])
})

test('foldReplies never hides work in progress', () => {
  // A running tool and streaming text stay visible even at keep=0.
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1', true), asst('a1b')]
  expect(foldReplies(nodes, { keep: 0 }).map(i => i.id)).toEqual(['u1', 'fold:u1', 't1', 'a1b'])
})

test('foldReplies leaves the newest turn alone while a run is live', () => {
  const nodes = [user('u1', 'one'), asst('a1a'), tool('t1'), asst('a1b'), user('u2', 'two'), asst('a2a'), asst('a2b')]
  expect(foldReplies(nodes, { keep: 0, busy: true }).map(i => i.id)).toEqual([
    'u1', 'fold:u1', 'u2', 'a2a', 'a2b',
  ])
})

test('clampCompactKeep bounds the configured N', () => {
  expect(clampCompactKeep(Number.NaN)).toBe(1)
  expect(clampCompactKeep(-3)).toBe(0)
  expect(clampCompactKeep(2.6)).toBe(3)
  expect(clampCompactKeep(99)).toBe(20)
})

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

async function useCompact(page: Page, keep: string) {
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-message').click()
  await page.getByTestId('view-compact').click()
  await page.getByTestId('view-keep').fill(keep)
  await page.getByTestId('settings').getByRole('button', { name: '关闭对话框' }).click()
}

/** Position of one fold row (by turn id) inside the chat scroller. */
function foldProbe(page: Page, fold: string) {
  return page.evaluate((f: string) => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const row = document.querySelector(`[data-testid="fold-row"][data-fold="${f}"]`) as HTMLElement | null
    return {
      scrollTop: Math.round(el.scrollTop),
      maxScroll: Math.round(el.scrollHeight - el.clientHeight),
      rowTop: row ? Math.round(row.getBoundingClientRect().top - el.getBoundingClientRect().top) : null,
      toBottom: !!document.querySelector('[data-testid="to-bottom"]'),
    }
  }, fold)
}

async function clickFold(page: Page, fold: string) {
  await page.evaluate((f: string) => {
    const row = document.querySelector(`[data-testid="fold-row"][data-fold="${f}"]`) as HTMLElement
    ;(row.querySelector('[data-testid="fold-row-btn"]') as HTMLElement).click()
  }, fold)
}

test('compact mode folds each turn\'s earlier replies and keeps the prompt visible', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  // The fake assistant answers with a Bash call for this prompt, so the turn
  // has more than one reply node and something to fold.
  await sendPrompt(page, 'e2e-bash:echo fold-me')
  await expect(page.getByTestId('tool-card')).toHaveCount(1)
  await sendPrompt(page, 'fold-beta')
  await expect(page.getByTestId('session-stats')).toContainText('2 轮 ·')

  // Detailed shows both turns in full: the tool call, its result, two replies.
  await expect(page.getByTestId('fold-row')).toHaveCount(0)
  await expect(page.getByTestId('user-bubble')).toHaveCount(2)
  await expect(page.getByTestId('assistant-message')).toHaveCount(3)
  await expect(page.getByTestId('tool-card')).toHaveCount(1)

  // Keep the default 1: the tool turn folds its earlier replies, the plain turn
  // has nothing to fold, and no user bubble is ever folded away.
  await useCompact(page, '1')
  await expect(page.getByTestId('fold-row')).toHaveCount(1)
  await expect(page.getByTestId('user-bubble')).toHaveCount(2)
  await expect(page.getByTestId('user-bubble').first()).toContainText('e2e-bash:echo fold-me')
  await expect(page.getByTestId('assistant-message')).toHaveCount(2)
  await expect(page.getByTestId('tool-card')).toHaveCount(0)

  // The fold opens in place: the hidden tool call and its result come back…
  await page.getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('fold-row-btn')).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByTestId('tool-card')).toHaveCount(1)
  // …and closes again from the same row.
  await page.getByTestId('fold-row-btn').click()
  await expect(page.getByTestId('fold-row-btn')).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByTestId('tool-card')).toHaveCount(0)

  // N=0 folds every reply, still keeping both prompts on screen.
  await useCompact(page, '0')
  await expect(page.getByTestId('fold-row')).toHaveCount(2)
  await expect(page.getByTestId('user-bubble')).toHaveCount(2)
  await expect(page.getByTestId('assistant-message')).toHaveCount(0)

  // The preference is per-browser and survives a reload.
  const saved = await page.evaluate(() => [localStorage.getItem('ki-message-view'), localStorage.getItem('ki-message-view-keep')])
  expect(saved).toEqual(['compact', '0'])
})

test('opening a fold under the viewport keeps the row put and pauses follow-tail', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  for (let i = 0; i < 8; i++) {
    await sendPrompt(page, `fold-msg-${i}`)
    await expect(page.getByTestId('session-stats')).toContainText(`${i + 1} 轮 ·`)
  }
  await useCompact(page, '0')
  await expect(page.getByTestId('fold-row').first()).toBeVisible()

  // A run that stays busy for a few seconds: the tail keeps moving after the
  // click, which is what used to drag the reader away.
  await sendPrompt(page, 'e2e-delay-3000 keep-streaming')
  await page.waitForTimeout(600)
  const fold = await page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    el.scrollTop = el.scrollHeight
    const box = el.getBoundingClientRect()
    const rows = [...document.querySelectorAll('[data-testid="fold-row"]')] as HTMLElement[]
    // A row the reader can actually see and click, near the tail.
    const visible = rows.filter(r => {
      const b = r.getBoundingClientRect()
      return b.top > box.top && b.bottom < box.bottom
    })
    return visible[0].dataset.fold as string
  })
  await page.waitForTimeout(200)
  const before = await foldProbe(page, fold)
  expect(before.rowTop).not.toBeNull()
  expect(before.toBottom).toBe(false)

  await clickFold(page, fold)
  const after = await foldProbe(page, fold)
  // The row is the pointer target: it must not move, and the viewport must not
  // keep claiming it is at the tail now that the fold pushed the tail away.
  expect(after.rowTop).toBe(before.rowTop)
  expect(after.scrollTop).toBe(before.scrollTop)
  expect(after.toBottom).toBe(true)

  // The run ends and rewrites the node list; the reader stays where they are.
  await page.waitForTimeout(4000)
  const ended = await foldProbe(page, fold)
  expect(ended.rowTop).toBe(before.rowTop)
  expect(ended.scrollTop).toBe(before.scrollTop)
  expect(ended.maxScroll).toBeGreaterThan(before.maxScroll)
})

/**
 * A session whose big turn is what a long tool run really looks like: dozens of
 * assistant/tool round trips in a single turn, so one fold row hides the whole
 * block. Written straight into the fixture home (the same place webui.spec.ts
 * drops skill and extension fixtures) because the fake model cannot produce a
 * turn anywhere near this size.
 */
function seedBigFoldSession(home: string, cwd: string): { title: string; userNodeId: string } {
  // Mirrors internal/session.EncodeCWD: the workspace directory a session with
  // this cwd belongs to.
  const ws = `--${cwd.replace(/^[/\\]+/, '').replace(/[/\\:]/g, '-')}--`
  const node = (n: number): string => `01a0deadbeef${n.toString(16).padStart(22, '0')}`
  const sid = node(1)
  const lines: string[] = []
  let seq = 0
  let parent = ''
  const push = (entry: Record<string, unknown>) => {
    seq += 1
    const id = node(seq)
    lines.push(JSON.stringify({ ...entry, id, parentId: parent, timestamp: `2026-09-26T15:00:${String(seq).padStart(2, '0')}.000000000Z` }))
    parent = id
    return id
  }
  const at = () => 1790000000000 + seq * 1000
  const user = (text: string) => push({ type: 'message', message: { role: 'user', content: [{ type: 'text', text }], timestamp: at(), durationMs: 0 } })
  const reply = (text: string) => push({ type: 'message', message: { role: 'assistant', content: [{ type: 'text', text }], timestamp: at(), provider: 'deepseek', model: 'deepseek-flash', stopReason: 'stop', durationMs: 0 } })
  const step = (n: number) => {
    const callId = `call_${n}`
    push({
      type: 'message',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: `Step ${n}: check the tree before the release.` },
          { type: 'text', text: `Checking step ${n} of the release so the notes stay accurate.` },
          { type: 'toolCall', id: callId, name: 'Bash', arguments: { command: `cd /data/hgy/ki && go test ./internal/server -run TestReplay -count=1` } },
        ],
        timestamp: at(), provider: 'deepseek', model: 'deepseek-flash', stopReason: 'toolUse', durationMs: 0,
      },
    })
    push({
      type: 'message',
      message: {
        role: 'toolResult', toolName: 'Bash', toolCallId: callId, timestamp: at(), durationMs: 20,
        content: [{ type: 'text', text: `ok  \tki/internal/server\t2.4${n}s\n--- PASS: TestReplay (0.31s)\n--- PASS: TestReplayBounded (0.02s)\nPASS` }],
      },
    })
  }

  push({ type: 'session', version: 1, timestamp: '2026-09-26T15:00:00.000000000Z', cwd, forkMode: 'flat' })
  user('fold warmup')
  reply('Warmup reply.')
  user('second warmup')
  reply('Second warmup reply.')
  const userNodeId = user('大回合')
  for (let i = 0; i < 45; i++) step(i)
  reply('**大回合结束**：所有改动都已验证。')
  user('tail prompt')
  reply('Tail reply.')
  const dir = join(home, 'sessions', ws, `2026-09-26T15-00-00-000000000Z_${sid}`)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'events.jsonl'), `${lines.join('\n')}\n`)
  writeFileSync(join(dir, 'config.json'), JSON.stringify({ provider: 'deepseek', model: 'deepseek-flash', thinkingEffort: 'high', activeLeafId: parent }, null, 2))
  return { title: 'fold warmup', userNodeId }
}

test.describe('big fold on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

  test('opening a fold that hides a whole long turn keeps the reader in place', async ({ page }) => {
    test.setTimeout(60_000)
    const { home, cwd } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string; cwd: string }
    const { userNodeId } = seedBigFoldSession(home, cwd)
    await page.addInitScript(() => {
      localStorage.setItem('ki-message-view', 'compact')
      localStorage.setItem('ki-message-view-keep', '1')
    })
    await page.goto('/')
    await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: 'fold warmup' }).first().click()
    await expect(page.getByTestId('fold-row').first()).toBeVisible({ timeout: 20_000 })

    // Reader's place: the fold row a third down the viewport, with transcript
    // above it for the row to fall back to.
    const before = await page.evaluate((fold: string) => {
      const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
      const row = document.querySelector(`[data-testid="fold-row"][data-fold="${fold}"]`) as HTMLElement
      el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - el.clientHeight * 0.35
      return { scrollTop: Math.round(el.scrollTop) }
    }, userNodeId)
    expect(before.scrollTop).toBeGreaterThan(0)
    await page.waitForTimeout(300)

    const probe = () => page.evaluate((fold: string) => {
      const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
      const row = document.querySelector(`[data-testid="fold-row"][data-fold="${fold}"]`) as HTMLElement
      return {
        scrollTop: Math.round(el.scrollTop),
        maxScroll: Math.round(el.scrollHeight - el.clientHeight),
        rowTop: Math.round(row.getBoundingClientRect().top - el.getBoundingClientRect().top),
        tools: document.querySelectorAll('[data-testid="tool-card"]').length,
      }
    }, userNodeId)
    const start = await probe()
    expect(start.tools).toBe(0)

    await page.locator(`[data-testid="fold-row"][data-fold="${userNodeId}"] [data-testid="fold-row-btn"]`).click()
    for (const wait of [50, 300, 1200]) {
      await page.waitForTimeout(wait)
      const now = await probe()
      // The list may not rebuild itself under the reader's finger, and the
      // scroll position must not be clamped back to the top of the window.
      expect(now.rowTop).toBe(start.rowTop)
      expect(now.scrollTop).toBe(start.scrollTop)
    }
    const after = await probe()
    expect(after.tools).toBeGreaterThan(0)
    expect(after.maxScroll).toBeGreaterThan(start.maxScroll)
  })
})
