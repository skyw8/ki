import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { statePath } from './global-setup.ts'

// Each browser test passed alone with its own server and home. Keep that
// isolation when splitting this file so independent scenarios overlap safely.
test.describe.configure({ mode: 'parallel' })

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

/** Tail position of the chat scroller: are we pinned to the end and following? */
function tailProbe(page: Page) {
  return page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    return {
      scrollTop: Math.round(el.scrollTop),
      maxScroll: Math.round(el.scrollHeight - el.clientHeight),
      toBottom: !!document.querySelector('[data-testid="to-bottom"]'),
    }
  })
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
  // Wait for the turn to settle before sending again: a second prompt while the
  // run is still busy steers the in-flight one, whose final reply then never
  // arrives under load (the assistant count below stalls at two).
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
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
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    el.scrollTop = el.scrollHeight
  })
  // Virtual rows commit after the scroll event. Reading them in the same task
  // as scrollTop can find no visible fold, especially with shared browsers.
  let fold = ''
  await expect.poll(async () => {
    fold = await page.evaluate(() => {
      const box = document.querySelector('[data-testid="chat-scroll"]')!.getBoundingClientRect()
      const rows = [...document.querySelectorAll('[data-testid="fold-row"]')] as HTMLElement[]
      return rows.find(row => {
        const b = row.getBoundingClientRect()
        return b.top > box.top && b.bottom < box.bottom
      })?.dataset.fold ?? ''
    })
    return fold
  }).not.toBe('')
  await expect.poll(async () => (await foldProbe(page, fold)).toBottom).toBe(false)
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

  // Wait for the real completion rewrite: a fixed sleep wastes time and can
  // still inspect a running turn when the machine is busy.
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  const ended = await foldProbe(page, fold)
  expect(ended.rowTop).toBe(before.rowTop)
  expect(ended.scrollTop).toBe(before.scrollTop)
  expect(ended.maxScroll).toBeGreaterThan(before.maxScroll)
})

test('expanding or collapsing the newest fold at the tail keeps following the bottom', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  // Enough turns that the transcript scrolls, so "at the tail" is meaningful.
  for (let i = 0; i < 8; i++) {
    await sendPrompt(page, `fold-msg-${i}`)
    await expect(page.getByTestId('session-stats')).toContainText(`${i + 1} 轮 ·`)
  }
  // A tool turn gives the newest turn several reply nodes to fold.
  await sendPrompt(page, 'e2e-bash:echo fold-me')
  await expect(page.getByTestId('session-stats')).toContainText('9 轮 ·')
  await useCompact(page, '0')
  await page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    el.scrollTop = el.scrollHeight
  })
  await expect.poll(async () => (await tailProbe(page)).toBottom).toBe(false)
  const before = await tailProbe(page)
  expect(before.scrollTop).toBe(before.maxScroll)

  // Expanding the newest fold reveals rows below the pointer. While following
  // the tail must stay pinned, not drag the reader above the new end.
  await page.getByTestId('fold-row-btn').last().click()
  await expect(page.getByTestId('fold-row-btn').last()).toHaveAttribute('aria-expanded', 'true')
  await expect.poll(async () => {
    const p = await tailProbe(page)
    return p.maxScroll > before.maxScroll && p.scrollTop === p.maxScroll
  }).toBe(true)
  expect((await tailProbe(page)).toBottom).toBe(false)

  // Collapsing removes rows, which followOnAppend does not cover: the tail must
  // still stay pinned instead of leaving the reader above a clamped end.
  await page.getByTestId('fold-row-btn').last().click()
  await expect(page.getByTestId('fold-row-btn').last()).toHaveAttribute('aria-expanded', 'false')
  await expect.poll(async () => {
    const p = await tailProbe(page)
    return p.scrollTop === p.maxScroll
  }).toBe(true)
  expect((await tailProbe(page)).toBottom).toBe(false)
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
          { type: 'toolCall', id: callId, name: 'exec_command', arguments: { cmd: `cd /data/hgy/ki && go test ./internal/server -run TestReplay -count=1` } },
        ],
        timestamp: at(), provider: 'deepseek', model: 'deepseek-flash', stopReason: 'toolUse', durationMs: 0,
      },
    })
    push({
      type: 'message',
      message: {
        role: 'toolResult', toolName: 'exec_command', toolCallId: callId, timestamp: at(), durationMs: 20,
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

test('a running turn folds its replies like any finished turn', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await useCompact(page, '0')
  // Two rounds, each delayed: while the second one is pending the turn already
  // has a tool call plus its result to fold, and the run is still live.
  await sendPrompt(page, `e2e-write-env e2e-delay-2500 ${Date.now()}`)
  // composer-stop is the live-run control, so the assertions below run while the
  // turn is still in flight.
  await expect(page.getByTestId('composer-stop')).toBeVisible({ timeout: 20_000 })
  // The turn already has a tool call and its result, and both are folded away;
  // the prompt stays on screen. Before this, a live turn rendered every reply
  // node it had.
  await expect(page.getByTestId('fold-row')).toHaveCount(1, { timeout: 20_000 })
  await expect(page.getByTestId('user-bubble')).toHaveCount(1)
  await expect(page.getByTestId('assistant-message')).toHaveCount(0)
  await expect(page.getByTestId('tool-card')).toHaveCount(0)
})

test('a burst of streaming chunks keeps the main thread responsive', async ({ page }) => {
  test.setTimeout(90_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  // 400 chunks of ~130 bytes: each one repeats the whole accumulated text, so an
  // un-throttled client re-lexes a message that grows to ~50 KiB on every delta.
  await sendPrompt(page, 'e2e-stream-400')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  const stats = await page.evaluate(async () => {
    const root = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const tasks: number[] = []
    const obs = new PerformanceObserver(list => {
      for (const e of list.getEntries()) tasks.push(e.duration)
    })
    try {
      obs.observe({ type: 'longtask', buffered: false })
    } catch { /* not supported: no budget check below */ }
    const deadline = Date.now() + 40_000
    while (Date.now() < deadline) {
      if ((root.textContent ?? '').includes('item 399')) break
      await new Promise(r => setTimeout(r, 200))
    }
    obs.disconnect()
    return {
      done: (root.textContent ?? '').includes('item 399'),
      total: Math.round(tasks.reduce((a, b) => a + b, 0)),
      worst: Math.round(Math.max(0, ...tasks)),
      tasks: tasks.length,
    }
  })
  console.log(`burst stats: ${JSON.stringify(stats)}`)
  // The block is the number, not the count of long tasks in isolation: before the
  // throttle the same run blocked for seconds (every delta re-parsed the message).
  expect(stats.done, 'the stream finished').toBe(true)
  expect(stats.total, `long task total ${stats.tasks} tasks, worst ${stats.worst}ms`).toBeLessThan(2000)
  expect(stats.worst, `worst long task (total ${stats.total}ms)`).toBeLessThan(400)
})
