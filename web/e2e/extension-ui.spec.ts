import { expect, test, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { serverToken, statePath } from './global-setup.ts'

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

async function tokenOf(page: Page) {
  void page
  return serverToken()
}

async function reloadServer(page: Page, request: { post: (url: string, opts: { headers: Record<string, string> }) => Promise<unknown> }) {
  const auth = await tokenOf(page)
  await request.post('/v1/reload', { headers: { Authorization: `Bearer ${auth}` } })
}

function writeExt(home: string, name: string, bin: string, extra: Record<string, unknown>) {
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name,
    capabilities: extra.capabilities ?? ['lifecycle'],
    runtime: { kind: 'rpc', command: bin, env: extra.env ?? {} },
  }))
}

// The freerouter config form dispatches on the extension name; a fixture with
// the same settings schema exercises the whole form round-trip.
test('freerouter config form edits fields without raw JSON', async ({ page, request }) => {
  test.setTimeout(60_000)
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'freerouter-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  const name = 'freerouter'
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  const number = (description: string) => ({ type: 'number', description })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name,
    capabilities: ['settings'],
    config: {
      schema: {
        type: 'object',
        additionalProperties: false,
        properties: {
          apiKey: { type: 'string', secret: true },
          baseUrl: { type: 'string' },
          raceWidth: { ...number('raced per round'), minimum: 1, maximum: 8 },
          maxBatches: { ...number('rounds per request'), minimum: 1, maximum: 6 },
          exhaustedTtlMs: number('quota cooldown'),
          slowTtlMs: number('slow cooldown'),
          firstTokenTimeoutMs: number('first token deadline'),
          idleTimeoutMs: number('stream idle deadline'),
          refreshIntervalMs: number('model list refresh'),
          listen: { type: 'string' },
        },
      },
      defaults: { raceWidth: 2, maxBatches: 3, exhaustedTtlMs: 90000, slowTtlMs: 15000, firstTokenTimeoutMs: 10000, idleTimeoutMs: 30000, refreshIntervalMs: 3600000, listen: '127.0.0.1:18427' },
    },
    runtime: { kind: 'rpc', command: bin },
  }))

  await page.goto('/')
  await reloadServer(page, request)
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
  const listed = await request.get('/v1/extensions', { headers })
  const catalog = await listed.json() as { items?: { name: string }[] }
  await request.patch('/v1/extensions', {
    headers,
    data: { disabled: (catalog.items ?? []).map(item => item.name).filter(item => item !== name) },
  })
  await reloadServer(page, request)
  await page.reload()

  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId(`extension-config-${name}`)).toBeVisible({ timeout: 15_000 })

  // No raw JSON textarea: the form renders dedicated inputs.
  await expect(page.getByTestId('extension-config-input')).toHaveCount(0)
  const raceWidth = page.getByTestId('freerouter-race-width')
  await expect(raceWidth).toBeVisible()
  await expect(page.getByTestId('freerouter-max-batches')).toBeVisible()
  await expect(page.getByTestId('freerouter-exhausted-ttl')).toBeVisible()
  await expect(page.getByTestId('freerouter-api-key')).toBeVisible()

  // Defaults surface in seconds; edit raceWidth and save.
  await expect(page.getByTestId('freerouter-exhausted-ttl')).toHaveValue('90')
  await raceWidth.fill('4')
  await page.getByTestId('freerouter-config-save').click()
  await expect(page.getByTestId('freerouter-race-width')).toBeVisible()

  // The saved value survives a reload of the editor.
  await page.reload()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId('freerouter-race-width')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('freerouter-race-width')).toHaveValue('4')

  // Cleanup: disable the fixture again so other specs start from defaults.
  await request.patch('/v1/extensions', { headers, data: { disabled: [name] } })
})

test('deep web search keeps the Codex model with the Codex provider', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const name = 'deep-web-search'
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name,
    capabilities: ['settings'],
    config: {
      schema: {
        type: 'object',
        additionalProperties: false,
        properties: {
          codexModel: { type: 'string' },
          provider: { type: 'string' },
          providerToggles: { type: 'object' },
          maxResults: { type: 'integer' },
          summaryModel: { type: 'string' },
          workflow: { type: 'string' },
          fetchContent: { type: 'boolean' },
        },
      },
      defaults: { codexModel: 'gpt-5.5', provider: 'all', maxResults: 5, summaryModel: '', workflow: 'none', fetchContent: false },
    },
    runtime: { kind: 'rpc', command: process.execPath, args: [join(repo, 'extensions/deep-web-search/dist/main.js')] },
  }))

  await page.goto('/')
  await reloadServer(page, request)
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
  const listed = await request.get('/v1/extensions', { headers })
  const catalog = await listed.json() as { items?: { name: string }[] }
  await request.patch('/v1/extensions', {
    headers,
    data: { disabled: (catalog.items ?? []).map(item => item.name).filter(item => item !== name) },
  })
  await reloadServer(page, request)
  await page.reload()

  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId(`extension-config-${name}`)).toBeVisible({ timeout: 15_000 })

  const global = page.locator('[aria-labelledby="deep-web-search-global-title"]')
  const providers = page.locator('[aria-labelledby="deep-web-search-providers-title"]')
  await expect(global.getByTestId('deep-web-search-codex-model-picker')).toHaveCount(0)
  await expect(providers.getByTestId('deep-web-search-codex-model-picker')).toBeVisible()

  await request.patch('/v1/extensions', { headers, data: { disabled: [name] } })
})

test('extension ui.setStatus chip opens panel modal', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-ext-ui-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  writeExt(home, 'goalui', bin, { env: { KI_SET_UI: '1', KI_SET_UI_GLOBAL: '1' } })

  await page.goto('/')
  await reloadServer(page, request)

  await sendPrompt(page, `ext-ui ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toBeVisible({ timeout: 20_000 })
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
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
  await expect(page.getByTestId('ext-chips-more')).toBeVisible()
  await expect(page.getByTestId('ext-drawer')).toHaveCount(0)
  await page.getByTestId('ext-chip-goalui').click()
  await expect(page.getByTestId('ext-panel')).toBeVisible()
  await expect(page.getByTestId('ext-nav')).toBeVisible()
  await expect(page.getByTestId('ext-nav-goalui')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('ext-panel')).toContainText('fixture panel text')
  await expect(page.getByTestId('ext-nav-goalui')).toContainText('Goal · active')
  await expect(page.getByTestId('ext-action-ack')).toBeVisible()
  await page.getByTestId('ext-action-ack').click()
  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await expect(page.getByTestId('extension-on-goalui')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('extensions-status-goalui')).toHaveCount(0)
})

test('opening a session locks composer until runtime.ready', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-ext-lock-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  writeExt(home, 'slowboot', bin, { env: { KI_INIT_SLEEP_MS: '1500' } })

  await page.goto('/')
  await reloadServer(page, request)
  await page.getByTestId('new-session').click()
  const input = page.getByTestId('composer-input')
  await expect(input).toBeDisabled()
  await expect(input).toHaveAttribute('placeholder', /正在加载扩展|Loading extensions/)
  await expect(page.getByTestId('command-btn')).toBeDisabled()
  await expect(input).toBeEnabled({ timeout: 15_000 })
})

test('slash palette is two-level for completions', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-ext-slash-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  writeExt(home, 'goalcmd', bin, { capabilities: ['command'], env: { KI_COMPLETIONS: '1' } })

  await page.goto('/')
  await reloadServer(page, request)
  await page.getByTestId('new-session').click()
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled({ timeout: 15_000 })

  await page.getByTestId('command-btn').click()
  const palette = page.getByTestId('command-palette')
  await expect(palette).toBeVisible()
  const palBox = await palette.boundingBox()
  const cardBox = await page.getByTestId('composer-card').boundingBox()
  expect(palBox).toBeTruthy()
  expect(cardBox).toBeTruthy()
  expect(palBox!.y + palBox!.height).toBeLessThanOrEqual(cardBox!.y + 2)
  const goal = page.getByTestId('command-item-goal')
  await expect(goal).toBeVisible()
  await expect(goal).toContainText('/goal')
  await expect(goal).toContainText('<objective>')
  await expect(goal).not.toContainText('pause resume')
  await expect(goal.locator('.command-desc')).toContainText('Run a goal')

  await goal.click()
  await expect(input).toHaveValue('/goal ')
  await expect(page.getByTestId('command-item-goal-pause')).toBeVisible()
  await expect(page.getByTestId('command-item-goal-edit')).toBeVisible()
  await expect(page.getByTestId('command-item-goal-status')).toBeVisible()
  await page.getByTestId('command-item-goal-pause').click()
  await expect(input).toHaveValue('/goal pause ')
  await expect(palette).toHaveCount(0)
})

test('top bar folds extra chips into one inspector modal', async ({ page, request }) => {
  test.setTimeout(60_000)
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-ext-fold-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  const fixtures = [
    { name: 'vault', text: 'Vault · error', tone: 'error', title: 'Vault' },
    { name: 'syncx', text: 'Sync · wait', tone: 'warning', title: 'Sync' },
    { name: 'goalfold', text: 'Goal · active', tone: 'active', title: 'Goal' },
    { name: 'indexx', text: 'Index · done', tone: 'success', title: 'Index' },
    { name: 'notesx', text: 'Notes', tone: 'info', title: 'Notes' },
  ]
  for (const row of fixtures) {
    writeExt(home, row.name, bin, {
      env: {
        KI_SET_UI: '1',
        KI_STATUS_TEXT: row.text,
        KI_STATUS_TONE: row.tone,
        KI_PANEL_TITLE: row.title,
        KI_PANEL_SUMMARY: `${row.title} panel`,
      },
    })
  }

  await page.goto('/')
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
  const listed = await request.get('/v1/extensions', { headers })
  const catalog = await listed.json() as { items?: { name: string }[] }
  const keep = new Set(fixtures.map(row => row.name))
  const disabled = (catalog.items ?? []).map(item => item.name).filter(name => !keep.has(name))
  await request.patch('/v1/extensions', { headers, data: { disabled } })
  await page.reload()
  await page.getByTestId('new-session').click()
  await expect(page.getByTestId('composer-input')).toBeEnabled({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-vault')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-syncx')).toBeVisible()
  await expect(page.getByTestId('ext-chip-goalfold')).toBeVisible()
  await expect(page.getByTestId('ext-chip-indexx')).toHaveCount(0)
  await expect(page.getByTestId('ext-chip-notesx')).toHaveCount(0)
  await expect(page.getByTestId('ext-chips-more').locator('.ext-more-wide')).toHaveText('+2')

  const shotDir = process.env.KI_SHOT_DIR || 'test-results/ext-inspector'
  mkdirSync(shotDir, { recursive: true })
  await page.screenshot({ path: `${shotDir}/01-header.png` })

  await page.getByTestId('ext-chips-more').click()
  await expect(page.getByTestId('ext-panel')).toBeVisible()
  await expect(page.getByTestId('ext-nav-indexx')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('ext-inspector-main')).toContainText('Index panel')
  await page.screenshot({ path: `${shotDir}/02-inspector-overflow.png` })

  await page.getByTestId('ext-nav-vault').click()
  await expect(page.getByTestId('ext-nav-vault')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('ext-inspector-main')).toContainText('Vault panel')
  await page.screenshot({ path: `${shotDir}/03-inspector-vault.png` })

  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()
  await page.getByTestId('ext-chip-goalfold').click()
  await expect(page.getByTestId('ext-nav-goalfold')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('ext-action-ack')).toBeVisible()

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByTestId('ext-nav')).toBeVisible()
  await page.screenshot({ path: `${shotDir}/04-inspector-mobile.png` })
  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()
  await page.screenshot({ path: `${shotDir}/05-header-mobile.png` })
})

test('global extension chip opens the same config modal as Configure', async ({ page, request }) => {
  test.setTimeout(60_000)
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = join(tmpdir(), 'ki-pw-global-config-sidecar')
  execFileSync(goBin(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })
  const name = 'globalcfg'
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name,
    capabilities: ['settings'],
    config: {
      schema: { type: 'object', properties: { label: { type: 'string' } } },
      defaults: { label: 'demo' },
    },
    runtime: { kind: 'rpc', command: bin },
  }))
  writeExt(home, 'goalui', bin, { env: { KI_SET_UI: '1', KI_SET_UI_GLOBAL: '1' } })

  await page.goto('/')
  await reloadServer(page, request)
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
  const listed = await request.get('/v1/extensions', { headers })
  const catalog = await listed.json() as { items?: { name: string }[] }
  const keep = new Set([name, 'goalui'])
  await request.patch('/v1/extensions', {
    headers,
    data: { disabled: (catalog.items ?? []).map(item => item.name).filter(item => !keep.has(item)) },
  })
  await reloadServer(page, request)
  await page.reload()
  const chip = page.getByTestId(`ext-chip-${name}`)
  await expect(chip).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-goalui')).toBeVisible({ timeout: 15_000 })
  await chip.click()
  await expect(page.getByTestId('ext-panel')).toBeVisible()
  await expect(page.getByTestId('ext-nav')).toBeVisible()
  await expect(page.getByTestId(`ext-nav-${name}`)).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId(`extension-config-${name}`)).toBeVisible()
  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()
  await expect(page.getByTestId('ext-panel')).toHaveCount(0)

  await page.getByTestId('new-session').click()
  await sendPrompt(page, `unified extension page ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('ext-chip-goalui')).toBeVisible({ timeout: 15_000 })
  await page.getByTestId(`ext-chip-${name}`).click()
  await expect(page.getByTestId('ext-nav-goalui')).toBeVisible()
  await expect(page.getByTestId(`ext-nav-${name}`)).toHaveAttribute('aria-current', 'page')
  await page.getByTestId('ext-nav-goalui').click()
  await expect(page.getByTestId('ext-panel')).toContainText('fixture panel text')
  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()

  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId('settings')).toHaveCount(0)
  await expect(page.getByTestId('ext-panel')).toBeVisible()
  await expect(page.getByTestId(`ext-nav-${name}`)).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId(`extension-config-${name}`)).toBeVisible()
})
