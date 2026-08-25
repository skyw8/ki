import { expect, test, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { statePath } from './global-setup.ts'

const repo = join(dirname(fileURLToPath(import.meta.url)), '../..')

function goBin(): string {
  if (process.env.GO) return process.env.GO
  try {
    execFileSync('go', ['version'], { stdio: 'ignore' })
    return 'go'
  } catch {
    for (const c of ['/home/hgy/sdk/go/bin/go', '/usr/local/go/bin/go']) {
      if (existsSync(c)) return c
    }
  }
  throw new Error('go toolchain not found')
}

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test('extension ui.setStatus chip opens panel text', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-ext-ui-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  const dir = join(home, 'extensions', 'goalui')
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name: 'goalui',
    capabilities: ['lifecycle'],
    runtime: { kind: 'rpc', command: bin, env: { KI_SET_UI: '1' } },
  }))

  await page.goto('/')
  const auth = await page.evaluate(() => (window as unknown as { __KI__?: { token?: string } }).__KI__?.token) || ''
  await request.post('/v1/reload', { headers: { Authorization: `Bearer ${auth}` } })

  await sendPrompt(page, `ext-ui ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toBeVisible({ timeout: 20_000 })
  const headers = { Authorization: `Bearer ${auth}` }
  const listed = await request.get('/v1/sessions', { headers })
  const items = (await listed.json()) as { id?: string }[] | { items?: { id: string }[] }
  const arr = Array.isArray(items) ? items : (items.items ?? [])
  const id = arr[0]?.id
  if (!id) throw new Error(`no session ${JSON.stringify(items)}`)
  let ui: unknown
  for (let i = 0; i < 40; i++) {
    const got = await request.get(`/v1/sessions/${id}`, { headers })
    const body = await got.json() as { extensionUi?: unknown }
    ui = body.extensionUi
    if (JSON.stringify(ui).includes('Goal · active')) break
    await page.waitForTimeout(100)
  }
  if (!JSON.stringify(ui).includes('Goal · active')) {
    throw new Error(`API missing chip: ${JSON.stringify(ui)}`)
  }
  await page.getByTestId('session-row').first().click()
  await expect(page.getByTestId('ext-chip-goalui')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-goalui')).toContainText('Goal · active')
  await page.getByTestId('ext-chip-goalui').click()
  await expect(page.getByTestId('ext-drawer')).toBeVisible()
  await expect(page.getByTestId('ext-drawer')).toContainText('fixture panel text')
  await expect(page.getByTestId('ext-action-ack')).toBeVisible()
  await page.getByTestId('ext-action-ack').click()
})
