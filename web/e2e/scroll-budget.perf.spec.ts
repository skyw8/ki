// Scrolling budgets for a long transcript. Run with:
//   cd web && bun run test:perf
//
// The cases here are the measured failure modes of the virtual list on a phone
// viewport (see docs/webui.md): a gesture that is swallowed by a scroll
// correction, a page of older history that lands under the reader's feet, an
// open that fetches history nobody asked for, and the markdown parse of every
// newly mounted row landing in one frame.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { statePath } from './global-setup.ts'

/** A realistic assistant reply: the shapes that actually cost parse time. */
function richReply(n: number): string {
  const code = Array.from({ length: 18 }, (_, i) => `  const row${i} = table.filter(r => r.turn === ${n} + ${i}) // 步骤 ${i}`).join('\n')
  const log = Array.from({ length: 10 }, (_, i) => `2026-09-27T08:${String(i).padStart(2, '0')}:11.482Z INFO  turn=${n} step=${i} 已处理 1234 条记录，写入 assets/index-${i}.js`).join('\n')
  return [
    `## 第 ${n} 轮：检查构建与发布流程`,
    '',
    '这一轮先看 **构建产物** 与 `vitest` 的输出，再决定要不要改 `scripts/run.sh`。中文段落用来触发 CJK 插件：**这是加粗的中文**，*这是斜体中文*。',
    '',
    '1. 确认 `web/dist` 与二进制里的嵌入资源一致',
    '2. 检查 `assets/` 的内容哈希是否变了',
    '   - 变了说明 bundle 重建过',
    '',
    '```ts',
    code,
    '```',
    '',
    '| 项目 | 字节 | 说明 |',
    '| --- | --- | --- |',
    `| events.jsonl | ${n * 1024} | 会话日志 |`,
    '| assets/index.js | 812345 | 内容哈希 |',
    '',
    '> 备注：这一段是引用，用来撑出块级结构。',
    '',
    '```bash',
    log,
    '```',
  ].join('\n')
}

function seed(home: string, cwd: string, turns: number): string {
  const ws = `--${cwd.replace(/^[/\\]+/, '').replace(/[/\\:]/g, '-')}--`
  const lines: string[] = []
  let seq = 0
  let parent = ''
  const push = (entry: Record<string, unknown>) => {
    seq += 1
    const id = `01a0ceed${seq.toString(16).padStart(22, '0')}`
    lines.push(JSON.stringify({ ...entry, id, parentId: parent, timestamp: `2026-09-27T10:00:${String(seq % 60).padStart(2, '0')}.000000000Z` }))
    parent = id
    return id
  }
  const at = () => 1790000000000 + seq * 1000
  const sid = push({ type: 'session', version: 1, cwd, forkMode: 'flat' })
  for (let i = 0; i < turns; i++) {
    push({ type: 'message', message: { role: 'user', content: [{ type: 'text', text: `turn ${i}` }], timestamp: at(), durationMs: 0 } })
    const callId = `call_${i}`
    push({ type: 'request_header', system: `budget ${'S'.repeat(1024)}`, tools: [{ name: 'Bash', description: 'run', parameters: { type: 'object' } }], provider: 'deepseek', modelId: 'deepseek-flash' })
    push({
      type: 'message',
      message: {
        role: 'assistant', timestamp: at(), provider: 'deepseek', model: 'deepseek-flash', stopReason: 'toolUse',
        usage: { input: 8200, output: 640 }, content: [{ type: 'text', text: richReply(i) }, { type: 'toolCall', id: callId, name: 'Bash', arguments: { command: 'cd /data/hgy/ki && go test ./internal/server -count=1' } }],
      },
    })
    push({ type: 'message', message: { role: 'toolResult', toolName: 'Bash', toolCallId: callId, timestamp: at(), durationMs: 90, content: [{ type: 'text', text: `ok\tki/internal/server\t0.0${i % 9}s\t${'result '.repeat(10)}` }] } })
  }
  const dir = join(home, 'sessions', ws, `2026-09-27T10-00-00-000000000Z_${sid}`)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'events.jsonl'), `${lines.join('\n')}\n`)
  writeFileSync(join(dir, 'config.json'), JSON.stringify({ provider: 'deepseek', model: 'deepseek-flash', title: 'scroll-budget', activeLeafId: parent }, null, 2))
  return sid
}

type Frame = { t: number; top: number; sent: number; mark: string }

async function startFrames(page: Page): Promise<void> {
  await page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const w = window as unknown as { __frames: Frame[]; __raf: number; __mark: string; __long: number[] }
    w.__frames = []
    w.__long = []
    w.__mark = ''
    new PerformanceObserver(list => {
      for (const e of list.getEntries()) w.__long.push(e.duration)
    }).observe({ type: 'longtask', buffered: false })
    const loop = (t: number) => {
      const box = el.getBoundingClientRect()
      let node = w.__mark ? el.querySelector(`[data-mark="${w.__mark}"]`) as HTMLElement | null : null
      if (!node) {
        node = [...el.querySelectorAll('[data-testid="assistant-message"]')].find(r => r.getBoundingClientRect().bottom > box.top + 4) as HTMLElement | null
        if (node) {
          const id = String(Math.round(t) % 100000)
          node.dataset.mark = id
          w.__mark = id
        }
      }
      w.__frames.push({ t: Math.round(t), top: Math.round(el.scrollTop), sent: node ? Math.round(node.getBoundingClientRect().top - box.top) : -1, mark: w.__mark })
      w.__raf = requestAnimationFrame(loop)
    }
    w.__raf = requestAnimationFrame(loop)
  })
}

async function stopFrames(page: Page): Promise<{ frames: number; longTasks: number; longMax: number; longTotal: number; worstLurch: number; trace: string }> {
  return page.evaluate(() => {
    const w = window as unknown as { __frames: Frame[]; __long: number[]; __raf: number }
    cancelAnimationFrame(w.__raf)
    // The marked row is the reader's reference: in one frame it may only move by
    // as much as the reader asked for (a wheel is 400px here). A bigger step is
    // the layout changing without the scroll following it — the list jumping
    // under the reader. Frames where the mark was re-pinned (the row left the
    // window) are skipped: their offsets are not comparable.
    let worst = 0
    let trace = ''
    for (let i = 1; i < w.__frames.length; i++) {
      const a = w.__frames[i - 1]
      const b = w.__frames[i]
      if (a.sent < 0 || b.sent < 0 || a.mark !== b.mark) continue
      const move = Math.abs(b.sent - a.sent)
      if (move > worst) {
        worst = move
        trace = `t=${b.t} sent ${a.sent}->${b.sent} top ${a.top}->${b.top}`
      }
    }
    return {
      frames: w.__frames.length,
      longTasks: w.__long.length,
      longMax: w.__long.length ? Math.round(Math.max(...w.__long)) : 0,
      longTotal: Math.round(w.__long.reduce((a, b) => a + b, 0)),
      worstLurch: Math.round(worst),
      trace,
    }
  })
}

/**
 * markOffset tracks the topmost visible reply: it is the reader's reference, and
 * its on-screen offset is what a jump would move. The element is marked so the
 * same row is measured across frames and steps (a new mark means the row left the
 * window, and the two offsets are not comparable).
 */
async function markOffset(page: Page): Promise<{ id: string; offset: number } | null> {
  return page.evaluate(() => {
    const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
    const box = el.getBoundingClientRect()
    const row = [...el.querySelectorAll('[data-testid="assistant-message"]')].find(r => r.getBoundingClientRect().bottom > box.top + 4) as HTMLElement | null
    if (!row) return null
    const id = row.dataset.sentMark ?? String(Math.round(performance.now()))
    row.dataset.sentMark = id
    return { id, offset: Math.round(row.getBoundingClientRect().top - box.top) }
  })
}

async function wheelUp(page: Page, steps: number, px: number): Promise<void> {
  for (let i = 0; i < steps; i++) {
    await page.mouse.wheel(0, px)
    await page.waitForTimeout(120)
  }
}

test.describe.configure({ mode: 'serial' })

test.describe('scroll budgets', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

  test('opening, scrolling up and paging older history stay predictable', async ({ page }) => {
    test.setTimeout(240_000)
    const { home, cwd } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string; cwd: string }
    seed(home, cwd, 400)

    const requests: string[] = []
    page.on('response', res => {
      try {
        const url = new URL(res.url())
        if (url.pathname.startsWith('/v1/sessions/')) requests.push(url.pathname.split('/').pop() + url.search)
      } catch { /* not a URL we care about */ }
    })

    await page.goto('/')
    await expect(page.getByTestId('hero')).toBeVisible()
    await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: 'scroll-budget' }).first().click()
    await expect(page.getByTestId('assistant-message').first()).toBeVisible({ timeout: 30_000 })
    await page.waitForTimeout(2000)

    const opened = await page.getByTestId('chat-scroll').evaluate(el => {
      const e = el as HTMLElement
      return { top: Math.round(e.scrollTop), height: Math.round(e.scrollHeight), client: e.clientHeight }
    })
    expect(opened.top + opened.client, `opened at ${JSON.stringify(opened)}`).toBeGreaterThanOrEqual(opened.height - 2)

    // Opening a session must not fetch history nobody asked to see: the list
    // paints at scrollTop 0 before follow-tail takes it to the bottom, and that
    // used to trip the "near the top" rule.
    expect(requests.filter(r => r.includes('before=')), `open fetched ${JSON.stringify(requests)}`).toEqual([])

    const box = await page.getByTestId('chat-scroll').boundingBox()
    if (!box) throw new Error('no chat scroller')
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    // Start inside the loaded window, above the fold.
    await page.getByTestId('chat-scroll').evaluate(el => { const e = el as HTMLElement; e.scrollTop = (e.scrollHeight - e.clientHeight) * 0.5 })
    await page.waitForTimeout(1200)

    // Paging older history, while the loaded window is short enough to reach the
    // top: the row the reader is on must not move.
    //
    // The intent comes from the wheel above; the trigger itself is a discrete
    // jump to the top, because a wheel step is applied over several frames and the
    // page would be requested in the middle of it — the reader's remaining deltas
    // then move the view after the page landed, which is correct behaviour but
    // makes the position at request time impossible to compare against.
    let paged = false
    const onResponse = (res: import('@playwright/test').Response) => {
      try {
        if (new URL(res.url()).searchParams.has('before')) paged = true
      } catch { /* not a URL we track */ }
    }
    page.on('response', onResponse)
    await wheelUp(page, 5, -400)
    await page.waitForTimeout(400)
    await startFrames(page)
    await page.getByTestId('chat-scroll').evaluate(el => { (el as HTMLElement).scrollTop = 0 })
    // Read the row the reader is on, but only once the virtual window has caught
    // up with the jump: for a frame after a one-shot scroll the mounted rows are
    // still laid out for the previous position, and their offsets belong to a
    // different frame (measured: 56,094px). A row that actually crosses the top of
    // the viewport is the fresh window.
    const readAnchor = () => page.evaluate(() => {
      const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
      const box = el.getBoundingClientRect()
      const row = [...el.querySelectorAll('[data-item-key]')]
        .map(r => r as HTMLElement)
        .find(r => r.getBoundingClientRect().bottom > box.top + 1)
      const offset = row ? Math.round(row.getBoundingClientRect().top - box.top) : null
      return { key: row?.dataset.itemKey ?? null, offset }
    })
    await startFrames(page)
    for (let i = 0; i < 400 && !paged; i++) await page.waitForTimeout(20)
    expect(paged, 'reaching the top pages older history in').toBeTruthy()
    await expect.poll(async () => {
      const read = await readAnchor()
      return read.offset != null && Math.abs(read.offset) < 600
    }, { timeout: 5000 }).toBe(true)
    const anchor = (await readAnchor()) as { key: string; offset: number }
    page.off('response', onResponse)
    await page.waitForTimeout(1800)
    const pagingScroll = await stopFrames(page)
    const after = await page.evaluate((key: string) => {
      const el = document.querySelector('[data-testid="chat-scroll"]') as HTMLElement
      const box = el.getBoundingClientRect()
      const row = el.querySelector(`[data-item-key="${CSS.escape(key)}"]`) as HTMLElement | null
      return { offset: row ? Math.round(row.getBoundingClientRect().top - box.top) : null, users: document.querySelectorAll('[data-testid="user-bubble"]').length }
    }, anchor.key)
    // The prepended page goes in *above* the reader: the row they were on stays
    // where it was, instead of the view being thrown a page back (before the fix
    // scrollTop stayed at 0 while the height grew by ~60,000px, and the reader was
    // left ~25 turns earlier). What is left is the drift of the rows that were
    // measured while the page was in flight — under a row, not a screen.
    expect(after.offset, `anchor row is gone (users=${after.users})`).not.toBeNull()
    expect(Math.abs(after.offset! - anchor.offset), `anchor moved from ${anchor.offset} to ${after.offset}`).toBeLessThanOrEqual(64)
    expect(pagingScroll.worstLurch, `page load lurched (${pagingScroll.trace})`).toBeLessThan(600)

    // First pass: rows are visited for the first time, so their height is only
    // as good as the learned estimate. Nothing may lurch, and the content must
    // still move by the gesture.
    await startFrames(page)
    const steps: number[] = []
    let prevMark = await markOffset(page)
    for (let i = 0; i < 12; i++) {
      await wheelUp(page, 1, -400)
      const now = await markOffset(page)
      if (prevMark && now && now.id === prevMark.id) steps.push(now.offset - prevMark.offset)
      else steps.push(NaN)
      prevMark = now
    }
    const first = await stopFrames(page)
    const measured = steps.filter(n => !Number.isNaN(n))
    console.log(`paging anchor ${anchor.offset} -> ${after.offset}; first pass steps=${JSON.stringify(steps)} ${JSON.stringify(first)}`)
    expect(measured.length, 'steps with a stable reference row').toBeGreaterThan(6)
    expect(Math.max(...measured.map(d => Math.abs(d - 400))), `first pass content moved ${JSON.stringify(steps)} for a 400px gesture`).toBeLessThan(150)
    expect(first.worstLurch, `first pass lurch (${first.trace})`).toBeLessThan(600)

    // Second pass over the same rows: every height is known, so each gesture
    // moves the view by exactly what it asked for.
    await page.waitForTimeout(600)
    await startFrames(page)
    const revisited: number[] = []
    prevMark = await markOffset(page)
    for (let i = 0; i < 12; i++) {
      await wheelUp(page, 1, -400)
      const now = await markOffset(page)
      if (prevMark && now && now.id === prevMark.id) revisited.push(now.offset - prevMark.offset)
      else revisited.push(NaN)
      prevMark = now
    }
    const second = await stopFrames(page)
    const measuredAgain = revisited.filter(n => !Number.isNaN(n))
    console.log(`revisit steps=${JSON.stringify(revisited)} ${JSON.stringify(second)}`)
    expect(Math.max(...measuredAgain.map(d => Math.abs(d - 400))), `revisit content moved ${JSON.stringify(revisited)}`).toBeLessThan(80)
    expect(second.worstLurch, `revisit lurch (${second.trace})`).toBeLessThan(600)

    expect(second.longTasks, 'markdown parses must not pile up per frame').toBeLessThan(40)
    expect(second.longMax, 'worst blocked frame while scrolling').toBeLessThan(400)
  })
})

test.describe('perf hud', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

  test('the overlay is opt-in and reports live numbers', async ({ page }) => {
    const { home, cwd } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string; cwd: string }
    seed(home, cwd, 60)
    await page.goto('/')
    await expect(page.getByTestId('hero')).toBeVisible()
    await expect(page.getByTestId('perf-hud')).toHaveCount(0)
    await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: 'scroll-budget' }).first().click()
    await expect(page.getByTestId('assistant-message').first()).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId('perf-hud')).toHaveCount(0)

    await page.evaluate(() => localStorage.setItem('ki-perf-hud', '1'))
    await page.reload()
    // The overlay lives in the conversation view, which a reload does not reopen.
    await page.getByTestId('mobile-nav-toggle').click()
    await page.getByTestId('session-row').filter({ hasText: 'scroll-budget' }).first().click()
    await expect(page.getByTestId('perf-hud')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId('perf-hud')).toContainText('fps')
    await expect(page.getByTestId('perf-hud')).toContainText('drift')
    await page.getByTestId('perf-hud').getByRole('button', { name: 'off' }).click()
    await expect(page.getByTestId('perf-hud')).toHaveCount(0)
  })
})
