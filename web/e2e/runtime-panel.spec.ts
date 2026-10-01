import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'
import type { LoopEvent } from '../src/api/types'

for (const width of [390, 1280]) {
 test(`runtime progress restores and controls the correct owner at ${width}px`, async ({ page }) => {
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
  const panel = page.getByTestId('runtime-panel')
  await panel.locator('summary').click()
  await expect(panel).toContainText('等待消息')
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
  await send({ type: 'agent_updated', agent: { ...agent, revision: 8, phase: 'settled', status: 'completed' } })
  await send({ type: 'agent_updated', agent: { ...agent, generation: 1, revision: 100 } })
  await expect(panel).toContainText('已结束')
  await expect(panel.getByRole('button', { name: '中断任务' })).toHaveCount(0)
 })
}
