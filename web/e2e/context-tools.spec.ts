import { expect, test, type Page } from '@playwright/test'
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Entry, SessionDetail } from '../src/api/types'
import { serverToken } from './global-setup'

test.describe.configure({ mode: 'parallel' })

const longTool = `extension_${'long_tool_name_'.repeat(8)}`
const toolNames = ['exec_command', 'read', 'alpha', 'bravo', 'charlie', 'delta', longTool, 'foxtrot', 'glob']

async function seed(page: Page, withTools: boolean) {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await page.request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBeTruthy()
  const { id, dir } = await created.json() as { id: string; dir: string }
  const entries: Entry[] = []
  let parentId = ''
  const push = (entry: Entry) => {
    entries.push({ ...entry, parentId })
    parentId = entry.id
  }
  push({ type: 'message', id: 'old-input', message: { role: 'user', content: [{ type: 'text', text: 'Older tools' }] } })
  if (withTools) {
    for (const [rank, name] of toolNames.entries()) {
      const calls = rank === 0 ? 4 : rank === 1 ? 2 : 1
      for (let n = 0; n < calls; n++) {
        const call = `call-${rank}-${n}`
        push({ type: 'message', id: `assistant-${call}`, message: {
          role: 'assistant', content: [{ type: 'toolCall', id: call, name, arguments: {} }],
        } })
        push({ type: 'message', id: `result-${call}`, message: {
          role: 'toolResult', toolCallId: call, toolName: name,
          isError: rank === 0 ? n < 2 : rank === 1 && n === 0,
          content: [{ type: 'text', text: `OLDER_TOOL_BODY_${call}` }],
        } })
      }
    }
    // Persist a sibling in the same tree, not a separate session: a whole-tree
    // index must not leak its tool activity onto the active branch.
    entries.push({ type: 'message', id: 'sibling-result', parentId: 'old-input', message: {
      role: 'toolResult', toolCallId: 'sibling-call', toolName: 'SIBLING_ONLY', isError: true,
      content: [{ type: 'text', text: 'Sibling failure' }],
    } })
  }
  // Keep the ranked tools outside the four-turn initial body window. The
  // browser must recover failures from metadata without hydrating old bodies.
  for (let turn = 0; turn < 7; turn++) {
    push({ type: 'message', id: `tail-user-${turn}`, message: {
      role: 'user', content: [{ type: 'text', text: `Tail input ${turn}` }],
    } })
    push({ type: 'message', id: `tail-answer-${turn}`, message: {
      role: 'assistant', content: [{ type: 'text', text: `Tail reply ${turn}` }],
    } })
  }
  appendFileSync(join(dir, 'events.jsonl'), entries.map(entry => JSON.stringify(entry)).join('\n') + '\n')
  const configPath = join(dir, 'config.json')
  const title = `context-tools-${id}`
  writeFileSync(configPath, JSON.stringify({
    ...JSON.parse(readFileSync(configPath, 'utf8')), title, activeLeafId: parentId,
  }))
  await page.addInitScript(() => {
    localStorage.setItem('ki-theme', 'light')
    localStorage.setItem('ki-message-view', 'compact')
    localStorage.setItem('ki-message-view-keep', '1')
  })
  return { id, title }
}

async function openContext(page: Page, title: string) {
  await page.goto('/')
  if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await page.getByTestId('tab-context').click()
  await expect(page.getByTestId('context-tool-activity')).toBeVisible()
}

test('tool ranking restores older failures from the index, excludes siblings, and fits dark mobile layouts', async ({ page }) => {
  const { id, title } = await seed(page, true)
  const snapshots: Array<{ fields: string | null; detail: SessionDetail }> = []
  let bodyRequests = 0
  await page.route(`**/v1/sessions/${id}?*`, async route => {
    const params = new URL(route.request().url()).searchParams
    if (params.has('entries') || params.has('turn') || params.has('before')) bodyRequests++
    const response = await route.fetch()
    snapshots.push({ fields: params.get('fields'), detail: await response.json() as SessionDetail })
    await route.fulfill({ response })
  })
  const card = page.getByTestId('context-tool-activity')
  const rows = card.getByTestId('context-tool-row')
  const assertRanking = async () => {
    await expect(card.locator('.context-tool-totals strong')).toHaveText(['13', '3'])
    await expect(rows).toHaveCount(8)
    expect(await rows.evaluateAll(elements => elements.map(el => el.getAttribute('data-tool-name')))).toEqual(toolNames.slice(0, 8))
    await expect(rows.nth(0).locator('.context-tool-counts strong')).toHaveText('4')
    await expect(rows.nth(0).locator('.context-tool-failures')).toHaveText('失败 2')
    await expect(rows.nth(1).locator('.context-tool-counts strong')).toHaveText('2')
    await expect(rows.nth(1).locator('.context-tool-failures')).toHaveText('失败 1')
    await expect(card.locator('[data-tool-name="SIBLING_ONLY"]')).toHaveCount(0)
  }
  await openContext(page, title)
  await assertRanking()
  const assertIndexOnly = () => {
    const index = snapshots.filter(snapshot => snapshot.fields === 'index')
    expect(index.length).toBeGreaterThan(0)
    expect(index.at(-1)?.detail.index?.find(entry => entry.id === 'result-call-0-0')?.isError).toBe(true)
    expect(index.at(-1)?.detail.index?.some(entry => entry.id === 'sibling-result')).toBe(true)
    const initial = snapshots.filter(snapshot => snapshot.fields !== 'index')
    expect(initial.length).toBeGreaterThan(0)
    expect(initial.every(snapshot => !snapshot.detail.entries?.some(entry => entry.id.startsWith('result-call-')))).toBe(true)
    expect(bodyRequests).toBe(0)
  }
  assertIndexOnly()
  // A fresh document discards loaded state: the same counts must survive an
  // actual refresh with the old results still available only in the index.
  await page.reload()
  await page.getByTestId('session-row').filter({ hasText: title }).click()
  await page.getByTestId('tab-context').click()
  await assertRanking()
  expect(snapshots.filter(snapshot => snapshot.fields === 'index').length).toBeGreaterThanOrEqual(2)
  assertIndexOnly()
  const more = card.locator('.context-tool-more')
  await expect(more).toContainText('+1')
  await more.click()
  await expect(rows).toHaveCount(9)
  await expect(more).toHaveCount(0)
  expect(await rows.evaluateAll(elements => elements.map(el => el.getAttribute('data-tool-name')))).toEqual(toolNames)
  await page.setViewportSize({ width: 1280, height: 844 })
  if (process.env.KI_CONTEXT_TOOLS_SCREENSHOT === '1') {
    await card.screenshot({ path: join(tmpdir(), 'ki-context-tools.png') })
  }
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-appearance').click()
  await page.getByTestId('settings-theme').getByRole('radio').nth(1).click()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('settings')).toHaveCount(0)

  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 844 })
    await card.scrollIntoViewIfNeeded()
    const layout = await card.evaluate(el => {
      const dimensions = (selector: string) => {
        const row = el.querySelector(selector)!
        const track = row.querySelector('.context-tool-track')!.getBoundingClientRect()
        const bar = row.querySelector('.context-tool-bar')!.getBoundingClientRect()
        const failed = row.querySelector('[data-testid="context-tool-failed"]')!
        const other = row.querySelector('.context-tool-bar-other')!
        const f = failed.getBoundingClientRect()
        const o = other.getBoundingClientRect()
        return { scale: bar.width / track.width, failedShare: f.width / bar.width,
          otherShare: o.width / bar.width, failedFirst: failed === failed.parentElement!.firstElementChild,
          leftAligned: Math.abs(f.left - bar.left) < 1, adjacent: Math.abs(f.right - o.left) < 1,
          color: getComputedStyle(failed).backgroundColor, otherColor: getComputedStyle(other).backgroundColor }
      }
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = 1
      const ctx = canvas.getContext('2d')!
      const rgb = (color: string) => {
        ctx.clearRect(0, 0, 1, 1)
        ctx.fillStyle = color
        ctx.fillRect(0, 0, 1, 1)
        return Array.from(ctx.getImageData(0, 0, 1, 1).data)
      }
      const luminance = (color: number[]) => color.slice(0, 3).map(value => {
        const channel = value / 255
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
      }).reduce((sum, value, i) => sum + value * [0.2126, 0.7152, 0.0722][i], 0)
      const contrast = (element: Element) => {
        let background: Element | null = element
        while (background && rgb(getComputedStyle(background).backgroundColor)[3] < 255) background = background.parentElement
        const bg = luminance(rgb(background ? getComputedStyle(background).backgroundColor : 'black'))
        const fg = luminance(rgb(getComputedStyle(element).color))
        return (Math.max(bg, fg) + 0.05) / (Math.min(bg, fg) + 0.05)
      }
      return {
        exec: dimensions('[data-tool-name="exec_command"]'), read: dimensions('[data-tool-name="read"]'),
        contrast: Array.from(el.querySelectorAll('.context-tool-name, .context-tool-counts strong, .context-tool-failures')).map(contrast),
        red: rgb(getComputedStyle(el.querySelector('[data-testid="context-tool-failed"]')!).backgroundColor),
        fits: el.scrollWidth <= el.clientWidth && Array.from(el.querySelectorAll('.context-tool-heading')).every(heading => {
          const box = heading.getBoundingClientRect()
          return heading.scrollWidth <= heading.clientWidth && box.left >= 0 && box.right <= window.innerWidth
        }),
        pageFits: document.documentElement.scrollWidth <= window.innerWidth,
      }
    })
    expect(layout.exec.scale).toBeCloseTo(1, 2)
    expect(layout.read.scale).toBeCloseTo(0.5, 2)
    for (const bar of [layout.exec, layout.read]) {
      expect(bar.failedShare).toBeCloseTo(0.5, 2)
      expect(bar.otherShare).toBeCloseTo(0.5, 2)
      expect(bar.failedFirst && bar.leftAligned && bar.adjacent).toBe(true)
      expect(bar.color).not.toBe(bar.otherColor)
    }
    expect(layout.red[0]).toBeGreaterThan(layout.red[1])
    expect(layout.red[0]).toBeGreaterThan(layout.red[2])
    expect(Math.min(...layout.contrast)).toBeGreaterThanOrEqual(4.5)
    expect(layout.fits && layout.pageFits).toBe(true)
    await expect(page.locator('body')).toHaveAttribute('data-theme', 'dark')
  }
})

test('a branch without tool calls shows zero totals and an explicit empty state', async ({ page }) => {
  const { title } = await seed(page, false)
  await page.setViewportSize({ width: 390, height: 844 })
  await openContext(page, title)
  const card = page.getByTestId('context-tool-activity')
  await expect(card.locator('.context-tool-totals strong')).toHaveText(['0', '0'])
  await expect(card.locator('.context-empty')).toHaveText('尚未记录工具调用。')
  await expect(card.getByTestId('context-tool-row')).toHaveCount(0)
  await expect(card.locator('.context-tool-more')).toHaveCount(0)
})
