import { expect, test, type Page } from '@playwright/test'
import { readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { statePath } from './global-setup.ts'
import { appendTranscript } from './perf-seed.ts'

type GetMeasure = {
  ms: number
  bytes: number
  hasMore?: boolean
  oldestId: string
  entries: number
  index: number
  truncated: number
  unchanged: number
}

type HeapMeasure = { jsHeap: number; nodes: number; listeners: number }

async function api<T>(page: Page, path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  return page.evaluate(async ({ path, init }) => {
    const headers: Record<string, string> = {}
    const method = init?.method ?? 'GET'
    if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) {
      const csrf = document.cookie.split('; ').find(item => item.startsWith('ki_csrf='))?.slice('ki_csrf='.length)
      if (csrf) headers['X-Ki-CSRF'] = decodeURIComponent(csrf)
      headers['Content-Type'] = 'application/json'
    }
    const res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: init?.body != null ? JSON.stringify(init.body) : undefined,
    })
    if (!res.ok) throw new Error(`${method} ${path} ${res.status} ${await res.text()}`)
    return res.json() as Promise<T>
  }, { path, init })
}

async function measureGet(page: Page, path: string): Promise<GetMeasure> {
  return page.evaluate(async path => {
    const t0 = performance.now()
    const res = await fetch(path, { credentials: 'same-origin' })
    const buf = await res.arrayBuffer()
    const ms = performance.now() - t0
    if (!res.ok) throw new Error(`GET ${path} ${res.status}`)
    const json = JSON.parse(new TextDecoder().decode(buf)) as {
      hasMore?: boolean
      oldestId?: string
      entries?: Array<{ truncated?: boolean; promptUnchanged?: boolean }>
      index?: unknown[]
    }
    return {
      ms,
      bytes: buf.byteLength,
      hasMore: !!json.hasMore,
      oldestId: json.oldestId ?? '',
      entries: json.entries?.length ?? 0,
      index: json.index?.length ?? 0,
      truncated: (json.entries ?? []).filter(e => e.truncated).length,
      unchanged: (json.entries ?? []).filter(e => e.promptUnchanged).length,
    }
  }, path)
}

async function heap(page: Page): Promise<HeapMeasure> {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Performance.enable')
  const got = await cdp.send('Performance.getMetrics') as { metrics: Array<{ name: string; value: number }> }
  const value = (name: string) => got.metrics.find(m => m.name === name)?.value ?? 0
  return { jsHeap: value('JSHeapUsedSize'), nodes: value('Nodes'), listeners: value('JSEventListeners') }
}

function mb(bytes: number): string {
  return `${(bytes / 1024 / 1024).toFixed(1)}`
}

test.describe.configure({ mode: 'serial' })

test('long history and huge message stay within GET/UI budgets', async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()

  const state = JSON.parse(readFileSync(statePath, 'utf8')) as { cwd?: string }
  const workspaces = await api<Array<{ id: string; path: string }>>(page, '/v1/workspaces')
  let workspaceId = workspaces[0]?.id
  if (!workspaceId) {
    const created = await api<{ id: string }>(page, '/v1/workspaces', {
      method: 'POST',
      body: { path: state.cwd || '/tmp/ki-perf-ws', title: 'perf' },
    })
    workspaceId = created.id
  }

  const history = await api<{ id: string; dir: string }>(page, '/v1/sessions', {
    method: 'POST',
    body: { workspaceId },
  })
  const histSeed = appendTranscript(history.dir, {
    title: 'perf-history',
    turns: 80,
    assistantBytes: 64,
    toolResultBytes: 2 * 1024,
    systemBytes: 2048,
    repeatSamePrompt: true,
  })
  const histJSONL = statSync(join(history.dir, 'events.jsonl')).size
  await api(page, `/v1/sessions/${history.id}`, { method: 'PATCH', body: { title: 'perf-history' } })

  const huge = await api<{ id: string; dir: string }>(page, '/v1/sessions', {
    method: 'POST',
    body: { workspaceId },
  })
  const hugeSeed = appendTranscript(huge.dir, {
    title: 'perf-huge',
    turns: 1,
    assistantBytes: 300 * 1024,
    systemBytes: 256,
  })
  const hugeJSONL = statSync(join(huge.dir, 'events.jsonl')).size
  await api(page, `/v1/sessions/${huge.id}`, { method: 'PATCH', body: { title: 'perf-huge' } })

  const listed = await api<Array<{ id: string; title?: string }>>(page, '/v1/sessions')
  expect(listed.map(s => s.title), `listed ${JSON.stringify(listed)}`).toEqual(expect.arrayContaining(['perf-history', 'perf-huge']))

  const histGet = await measureGet(page, `/v1/sessions/${history.id}`)
  const histGet2 = await measureGet(page, `/v1/sessions/${history.id}`)
  const runtime = await measureGet(page, `/v1/sessions/${history.id}?fields=runtime`)
  const histBefore = await measureGet(page, `/v1/sessions/${history.id}?before=${histGet.oldestId}`)
  const hugeGet = await measureGet(page, `/v1/sessions/${huge.id}`)
  const hugeEntry = await measureGet(page, `/v1/sessions/${huge.id}?entry=${hugeSeed.leafId}`)

  await page.reload()
  await expect(page.getByTestId('hero')).toBeVisible()
  const wsToggle = page.locator('.ws-toggle').first()
  await expect(wsToggle).toBeVisible()
  const historyTitle = page.getByTestId('session-title').filter({ hasText: 'perf-history' })
  if (!await historyTitle.isVisible()) await wsToggle.click()
  await expect(historyTitle).toBeVisible()
  const before = await heap(page)

  const tOpen = performance.now()
  await page.getByTestId('session-title').filter({ hasText: 'perf-history' }).click()
  await expect(page.getByTestId('chat')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('user-bubble').first()).toBeVisible()
  const historyOpenMs = performance.now() - tOpen
  const historyHeap = await heap(page)
  const historyAssistants = await page.getByTestId('assistant-message').count()
  const historyUsers = await page.getByTestId('user-bubble').count()

  const olderWait = page.waitForResponse(res => {
    try {
      const url = new URL(res.url())
      return url.pathname.endsWith(`/v1/sessions/${history.id}`) && url.searchParams.has('before') && res.ok()
    } catch {
      return false
    }
  })
  await page.getByTestId('chat-scroll').evaluate(el => {
    const node = el as HTMLElement
    node.scrollTop = 0
    node.dispatchEvent(new Event('scroll'))
  })
  await olderWait

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  const trajRows = await page.getByTestId('traj-row').count()
  const trajHeap = await heap(page)

  await page.getByTestId('tab-conversation').click()
  const hydrateWait = page.waitForResponse(res => {
    try {
      const url = new URL(res.url())
      return url.pathname.endsWith(`/v1/sessions/${huge.id}`) && (url.searchParams.has('entry') || url.searchParams.has('entries')) && res.ok()
    } catch {
      return false
    }
  })
  await page.getByTestId('session-title').filter({ hasText: 'perf-huge' }).click()
  const tHuge = performance.now()
  await expect(page.getByTestId('chat')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('assistant-message').first()).toBeVisible({ timeout: 30_000 })
  await hydrateWait
  const hugeOpenMs = performance.now() - tHuge
  const hugeHeap = await heap(page)
  const hugeAssistants = await page.getByTestId('assistant-message').count()

  const rows = [
    ['history jsonl', `${histJSONL}`, '-', `${histSeed.leafId ? 'disk' : ''}`],
    ['history GET', `${histGet.bytes}`, histGet.ms.toFixed(0), `index=${histGet.index} entries=${histGet.entries} unchanged=${histGet.unchanged} hasMore=${histGet.hasMore}`],
    ['history GET#2', `${histGet2.bytes}`, histGet2.ms.toFixed(0), 'mtime cache'],
    ['fields=runtime', `${runtime.bytes}`, runtime.ms.toFixed(0), `index=${runtime.index}`],
    ['history before', `${histBefore.bytes}`, histBefore.ms.toFixed(0), `entries=${histBefore.entries} hasMore=${histBefore.hasMore}`],
    ['history open UI', '-', historyOpenMs.toFixed(0), `asstDOM=${historyAssistants} userDOM=${historyUsers} heap=${mb(historyHeap.jsHeap)}MiB nodes=${historyHeap.nodes}`],
    ['history traj', '-', '-', `rowsDOM=${trajRows} heap=${mb(trajHeap.jsHeap)}MiB`],
    ['huge jsonl', `${hugeJSONL}`, '-', `truncatedGET=${hugeGet.truncated}`],
    ['huge GET', `${hugeGet.bytes}`, hugeGet.ms.toFixed(0), `entries=${hugeGet.entries}`],
    ['huge ?entry', `${hugeEntry.bytes}`, hugeEntry.ms.toFixed(0), 'full body'],
    ['huge open UI', '-', hugeOpenMs.toFixed(0), `asstDOM=${hugeAssistants} heap=${mb(hugeHeap.jsHeap)}MiB (idle ${mb(before.jsHeap)}MiB)`],
  ]
  const table = ['case'.padEnd(18) + 'bytes'.padStart(12) + 'ms'.padStart(10) + '  note', ...rows.map(r => r[0].padEnd(18) + r[1].padStart(12) + r[2].padStart(10) + '  ' + r[3])]
  console.log(`\n${table.join('\n')}\n`)
  test.info().annotations.push({ type: 'perf', description: table.join(' | ') })

  expect(histGet.hasMore, '80-turn leaf should exceed the tail window').toBeTruthy()
  expect(histGet.oldestId, 'tail oldestId').toBeTruthy()
  expect(histGet.unchanged, 'repeated request headers should omit system/tools').toBeGreaterThan(0)
  expect(histGet.bytes, 'slim history GET').toBeLessThan(histJSONL)
  expect(histGet.bytes, 'history GET budget').toBeLessThan(1_500_000)
  expect(histGet.ms, 'history GET latency').toBeLessThan(3_000)
  expect(runtime.bytes, 'runtime GET budget').toBeLessThan(200_000)
  expect(runtime.index, 'runtime omits index').toBe(0)
  expect(histBefore.entries, 'before window').toBeGreaterThan(0)
  expect(histBefore.index, 'before omits index').toBe(0)
  expect(histBefore.bytes, 'before GET budget').toBeLessThan(histJSONL)
  expect(historyOpenMs, 'open long history').toBeLessThan(12_000)
  expect(historyAssistants, 'chat virtualizes assistants').toBeLessThan(80)
  expect(historyUsers, 'chat virtualizes users').toBeLessThan(80)
  expect(trajRows, 'trajectory virtualizes rows').toBeLessThan(120)
  expect(historyHeap.jsHeap, 'history JS heap').toBeLessThan(400 * 1024 * 1024)
  expect(hugeGet.truncated, 'huge assistant truncated in list').toBeGreaterThan(0)
  expect(hugeGet.bytes, 'truncated huge GET').toBeLessThan(120_000)
  expect(hugeEntry.bytes, 'full huge entry').toBeGreaterThan(300 * 1024)
  expect(hugeOpenMs, 'open huge message').toBeLessThan(20_000)
  expect(hugeHeap.jsHeap, 'huge JS heap').toBeLessThan(500 * 1024 * 1024)
})
