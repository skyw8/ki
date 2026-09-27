import { expect, test } from '@playwright/test'
import { openStream } from './stream-fixture'
import type { LoopEvent } from '../src/api/types'

/**
 * The running tool and the running turn both show a live, ticking duration, and
 * swap to their authoritative settled value in place — the per-turn strip does
 * not appear only after the turn ends. The event stream is injected so the
 * "long-running" tool is deterministic instead of depending on a slow server.
 */
test('tool and turn durations tick live, then settle in place', async ({ page }) => {
  const f = await openStream(page, 0)
  const send = (ev: LoopEvent) => page.evaluate(ev => (window as unknown as { streamSend: (ev: LoopEvent) => void }).streamSend(ev), ev)
  const now = Date.now()

  await send({ type: 'turn_start', runId: 'live', seq: 1, timestamp: now - 5_000 })
  await send({ type: 'message_end', runId: 'live', seq: 2, entryId: 'run', message: { role: 'user', timestamp: now - 5_000, content: [{ type: 'text', text: 'run it' }] } })
  await send({ type: 'message_end', runId: 'live', seq: 3, message: { role: 'assistant', timestamp: now - 4_000, content: [{ type: 'text', text: 'running' }, { type: 'toolCall', id: 'tc1', name: 'Bash', arguments: { command: 'sleep 30' } }] } })
  await send({ type: 'tool_execution_start', runId: 'live', seq: 4, toolCallId: 'tc1', toolName: 'Bash', args: { command: 'sleep 30' }, timestamp: now - 3_800 })

  // The running tool row shows its elapsed already (about 3.8s) and keeps
  // counting. Before the fix this span was empty until tool_execution_end.
  const toolRow = page.getByTestId('tool-card').filter({ hasText: 'Bash' })
  const toolDur = toolRow.getByTestId('tool-duration')
  await expect(toolDur).toBeVisible()
  await expect(toolDur).toContainText('s')
  const toolFirst = await toolDur.textContent()
  await expect.poll(async () => toolDur.textContent()).not.toBe(toolFirst)

  // The turn's per-turn strip is rendered while live, marked with a pulse dot,
  // and its elapsed ticks too.
  const divider = page.getByTestId('turn-divider')
  await expect(divider).toHaveAttribute('data-live', 'true')
  await expect(divider.locator('.turn-live-dot')).toBeVisible()
  const turnFirst = await divider.getByTestId('turn-elapsed').textContent()
  await expect.poll(async () => divider.getByTestId('turn-elapsed').textContent()).not.toBe(turnFirst)

  // Settling replaces both values in place with the reported duration.
  await send({ type: 'tool_execution_end', runId: 'live', seq: 5, toolCallId: 'tc1', toolName: 'Bash', isError: false, durationMs: 4_200, result: 'done' })
  await expect(toolRow.getByTestId('tool-duration')).toHaveText('4.20 s')
  await send({ type: 'turn_end', runId: 'live', seq: 6, timestamp: now, durationMs: 5_000, message: { role: 'assistant', content: [{ type: 'text', text: 'done' }] } })
  await expect(divider).not.toHaveAttribute('data-live', 'true')
  await expect(divider.getByTestId('turn-elapsed')).toContainText('耗时')
})
