import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'
import type { LoopEvent } from '../src/api/types'

test.describe.configure({ mode: 'parallel' })

for (const width of [390, 1280]) {
 test(`runtime progress restores and controls the correct owner at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 844 })
  const fixture = await openStream(page, 0)
  const send = (ev: LoopEvent) => page.evaluate(ev => (window as unknown as { streamSend: (event: LoopEvent) => void }).streamSend(ev), ev)
  const process = { session_id: 79, revision: 2, cmd: 'server with a long command '.repeat(12), tty: true, status: 'running' as const, total_bytes: 0, started_at: new Date().toISOString() }
  const agent = { task_name: '/root/review', agent_id: 'a', session_id: 'child-session', generation: 2, revision: 3, status: 'running', phase: 'waiting_message', current_tools: [{ call_id: 'wait', name: 'wait_agent', started_at: new Date().toISOString() }] }
  await page.route(`**/v1/sessions/${fixture.id}*`, route => route.request().method() === 'GET' && !route.request().url().includes('/events')
   ? route.fulfill({ json: { id: fixture.id, running: true, entries: fixture.entries, processes: [process], agents: [agent], runtime: { ready: true } } }) : route.fallback())
  await page.route('**/v1/sessions/*/abort', route => route.fulfill({ json: { aborted: true } }))
  await send({ type: 'process_updated', process })
  await send({ type: 'agent_updated', agent })
  await expect(page.getByTestId('runtime-panel')).toHaveCount(0)
  await page.getByTestId('tab-config').click()
  const panel = page.getByTestId('runtime-panel')
  await expect(panel.locator('h2')).toHaveText(['Agents1', 'Processes1'])
  await expect(page.getByTestId('info-outline')).toContainText('Agents')
  await expect(page.getByTestId('info-outline')).toContainText('Processes')
  await expect(panel).toContainText('等待消息')
  await expect(panel).toContainText('wait_agent')
  // Runtime links reopen the owner's conversation; Info restores from its GET
  // snapshot rather than depending on another stream frame.
  const ownerNavigation = page.waitForRequest(request => request.method() === 'GET' && request.url().includes(`/v1/sessions/${fixture.id}`) && !request.url().includes('/events'))
  await panel.locator('.runtime-command').click()
  await ownerNavigation
  await expect(page.getByTestId('tab-conversation')).toHaveClass(/active/)
  await page.getByTestId('tab-config').click()
  await expect(panel).toContainText('wait_agent')
  const stop = panel.getByRole('button', { name: '停止进程' })
  const interrupt = panel.getByRole('button', { name: '中断任务' })
  for (const button of [stop, interrupt]) {
   const box = await button.boundingBox()
   expect(box?.height).toBeGreaterThanOrEqual(44)
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  const processRequest = page.waitForRequest(request => request.url().endsWith(`/v1/sessions/${fixture.id}/abort`))
  await stop.click()
  expect((await processRequest).postDataJSON()).toMatchObject({ scope: 'process', session_id: 79 })
  const agentRequest = page.waitForRequest(request => request.url().endsWith('/v1/sessions/child-session/abort'))
  await interrupt.click()
  expect((await agentRequest).postDataJSON()).toMatchObject({ scope: 'turn' })
  // An older stream frame cannot regress a new generation or settled process.
  await send({ type: 'process_updated', process: { ...process, revision: 6, status: 'exited', exit_code: 0 } })
  await send({ type: 'process_updated', process })
  await expect(panel.getByTestId('runtime-process')).toHaveCount(0)
  await expect(panel).toContainText('暂无运行中的进程')
  await send({ type: 'agent_updated', agent: { ...agent, revision: 8, phase: 'settled', status: 'completed' } })
  await send({ type: 'agent_updated', agent: { ...agent, generation: 1, revision: 100 } })
  await expect(panel).toContainText('已结束')
  await expect(panel.getByRole('button', { name: '中断任务' })).toHaveCount(0)
  await send({ type: 'agent_updated', agent: { ...agent, revision: 9, phase: 'settled', status: 'failed' } })
  await expect(panel.locator('[data-task="/root/review"] .runtime-phase')).toHaveText('失败')
  await send({ type: 'agent_updated', agent: { ...agent, revision: 10, phase: 'settled', status: 'killed' } })
  await expect(panel.locator('[data-task="/root/review"] .runtime-phase')).toHaveText('已中断')
  // A nested snapshot belongs under its canonical parent, even if delivered later.
  const nested = { ...agent, task_name: '/root/review/check', session_id: 'nested-session', revision: 1, status: 'completed', phase: 'settled' }
  await send({ type: 'agent_updated', agent: nested })
  await send({ type: 'agent_updated', agent: { task_name: '/root', session_id: fixture.id, status: 'running', phase: 'executing', generation: 1 } })
  const branch = panel.locator('.runtime-agent-node').filter({ has: page.locator('[data-task="/root/review"]') }).last().locator('.runtime-branch').first()
  const nameX = (task: string) => panel.locator(`[data-task="${task}"] .runtime-link`).evaluate(el => el.getBoundingClientRect().left)
  const rootX = await nameX('/root')
  const childX = await nameX('/root/review')
  const leafX = await nameX('/root/review/check')
  expect(childX - rootX).toBeCloseTo(24, 0)
  expect(leafX - childX).toBeCloseTo(24, 0)
  // Plain rows keep metadata aligned with names instead of a second card inset.
  for (const task of ['/root', '/root/review', '/root/review/check']) {
   const item = panel.locator(`[data-task="${task}"]`)
   await expect(item).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
   expect(await item.locator('.runtime-meta').evaluate(el => el.getBoundingClientRect().left)).toBeCloseTo(await nameX(task), 0)
  }
  await page.locator('#info-agents').screenshot({ path: testInfo.outputPath(`agents-${width}.png`) })
  await branch.getByRole('button', { name: '收起 /root/review', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'check', exact: true })).not.toBeVisible()
  await branch.getByRole('button', { name: '展开 /root/review', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'check', exact: true })).toBeVisible()
  await page.route('**/v1/sessions/nested-session*', route => route.fulfill({ json: { id: 'nested-session', title: 'Nested review', entries: fixture.entries, runtime: { ready: true } } }))
  const navigation = page.waitForRequest(request => request.url().includes('/v1/sessions/nested-session'))
  await panel.getByRole('button', { name: 'check', exact: true }).click()
  await navigation
  await expect(page.getByTestId('tab-conversation')).toHaveClass(/active/)
  await expect(page.getByTestId('runtime-panel')).toHaveCount(0)
 })
}

test('deep runtime trees keep touch controls and names inside a 320px viewport', async ({ page }) => {
 await page.setViewportSize({ width: 320, height: 844 })
 await openStream(page, 0)
 for (let depth = 1; depth <= 9; depth++) {
  await page.evaluate(agent => (window as unknown as { streamSend: (event: LoopEvent) => void }).streamSend({ type: 'agent_updated', agent }), {
   task_name: `/root/${Array.from({ length: depth }, (_, i) => `task_${i}`).join('/')}`,
   session_id: `deep-${depth}`, status: 'running', phase: 'executing',
  })
 }
 await page.getByTestId('tab-config').click()
 const panel = page.getByTestId('runtime-panel')
 await expect(panel.getByTestId('runtime-agent')).toHaveCount(9)
 const last = panel.locator('[data-task$="/task_8"]')
 await last.scrollIntoViewIfNeeded()
 await expect(last.getByRole('button', { name: 'task_8', exact: true })).toBeVisible()
 for (const button of await last.getByRole('button').all()) {
  const box = await button.boundingBox()
  expect(box?.height).toBeGreaterThanOrEqual(44)
  expect(box?.x).toBeGreaterThanOrEqual(0)
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(320)
 }
 expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})
