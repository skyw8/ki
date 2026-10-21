import { expect, test, type Locator, type Page } from '@playwright/test'
import { openSessionRow } from './sidebar.ts'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { serverToken, statePath } from './global-setup.ts'
import { sidecarBin } from './sidecar.ts'
import { MIN_TOUCH_SIZE } from './touch-target.ts'
import { newSession } from './session.ts'

// Every test is self-contained (verified standalone on its own server), so the
// parallel runner may split this file into one isolated process per test.
test.describe.configure({ mode: 'parallel' })

const repo = join(dirname(fileURLToPath(import.meta.url)), '../..')

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

async function expectMinTarget(locator: Locator, label: string): Promise<void> {
  // Why: boundingBox returns null before the control is rendered, and the chat
  // clamp toggle appears one frame after the bubble, so wait for visibility.
  await expect(locator, `${label} should be visible`).toBeVisible()
  const box = await locator.boundingBox()
  expect(box, `${label} should have a layout box`).toBeTruthy()
  expect(box!.width, `${label} width`).toBeGreaterThanOrEqual(MIN_TOUCH_SIZE)
  expect(box!.height, `${label} height`).toBeGreaterThanOrEqual(MIN_TOUCH_SIZE)
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

for (const width of [1440, 390]) test(`extension errors remain readable above details and config at ${width}px`, async ({ page, request }, testInfo) => {
  await page.setViewportSize({ width, height: 900 })
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const name = `indexerror${width}`
  const bin = sidecarBin(home, `index-error-sidecar-${width}`)
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  const error = `Index failed for /workspace/${'long-path/'.repeat(15)}: unsupported index version 1; expected 2.\nRebuild the index from the extension settings to continue.`
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name, capabilities: ['lifecycle', 'settings'],
    config: { schema: { type: 'object', properties: { label: { type: 'string' } } }, defaults: { label: 'demo' } },
    runtime: { kind: 'rpc', command: bin, env: { KI_SET_UI: '1', KI_STATUS_TONE: 'error', KI_STATUS_TEXT: error } },
  }))
  await page.goto('/')
  await reloadServer(page, request)
  const headers = { Authorization: `Bearer ${await tokenOf(page)}` }
  const listed = await request.get('/v1/extensions', { headers })
  const catalog = await listed.json() as { items?: { name: string }[] }
  await request.patch('/v1/extensions', { headers, data: { disabled: (catalog.items ?? []).map(item => item.name).filter(item => item !== name) } })
  await reloadServer(page, request)
  await page.reload()
  if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
  await newSession(page)
  await sendPrompt(page, `show extension error ${width}`)
  await expect(page.getByTestId('assistant-message')).toBeVisible()
  if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  const banner = page.getByTestId('ext-inspector-error')
  await expect(banner).toHaveText(error)
  await expect(banner).toHaveClass(/notice/)
  await page.getByTestId('ext-inspector-config-tab').click()
  await expect(page.getByTestId(`extension-config-${name}`)).toBeVisible()
  await expect(banner).toHaveText(error)
  await expect(page.getByTestId(`ext-nav-${name}`).locator('.ext-nav-dot')).toHaveClass(/tone-error/)
  const box = await banner.boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width).toBeLessThanOrEqual(width)
  expect(await banner.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('extension-error.png') })
  await page.getByTestId('ext-panel').getByRole('button', { name: '关闭对话框' }).click()
  await page.reload()
  await openSessionRow(page, `show extension error ${width}`)
  if (await page.getByTestId('mobile-nav-toggle').isVisible()) await page.getByTestId('mobile-nav-toggle').click()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(banner).toHaveText(error)
})

test('telegram config form renders schema fields and saves required inputs', async ({ page, request }) => {
  test.setTimeout(60_000)
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const name = 'telegram-bot'
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  const manifest = JSON.parse(readFileSync(join(repo, 'extensions', name, 'extension.json'), 'utf8')) as Record<string, unknown>
  delete manifest.i18n
  manifest.runtime = { kind: 'rpc', command: process.execPath, args: [join(repo, 'extensions/telegram-bot/dist/main.js')] }
  writeFileSync(join(dir, 'extension.json'), JSON.stringify(manifest))

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
  await expect(page.getByTestId('telegram-bot-id')).toBeVisible()
  await expect(page.getByTestId('telegram-bot-token')).toBeVisible()
  await expect(page.getByTestId('telegram-model-picker')).toBeVisible()
  await expect(page.getByTestId('telegram-config-save')).toBeVisible()

  await page.getByTestId('telegram-bot-id').fill('123456')
  await page.getByTestId('telegram-bot-token').fill('test-token')
  const telegramSaved = page.waitForResponse(response => response.url().includes('/v1/extensions/telegram-bot/config') && response.request().method() === 'PATCH')
  await page.getByTestId('telegram-config-save').click()
  await telegramSaved
  await page.reload()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId('telegram-bot-id')).toHaveValue('123456', { timeout: 15_000 })
  await expect(page.getByTestId('telegram-bot-token')).toHaveValue('')
  await expect(page.getByTestId('telegram-bot-token')).toHaveAttribute('placeholder', /configured|config\.tokenConfigured/i)

  await request.patch('/v1/extensions', { headers, data: { disabled: [name] } })
})

test('deep web search keeps the Codex model with the Codex provider', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const name = 'deep-web-search'
  const dir = join(home, 'extensions', name)
  mkdirSync(dir, { recursive: true })
  const manifest = JSON.parse(readFileSync(join(repo, 'extensions', name, 'extension.json'), 'utf8')) as Record<string, unknown>
  delete manifest.i18n
  manifest.runtime = { kind: 'rpc', command: process.execPath, args: [join(repo, 'extensions/deep-web-search/dist/main.js')] }
  writeFileSync(join(dir, 'extension.json'), JSON.stringify(manifest))


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
  await expect(global.getByTestId('deep-web-search-toggle-codex')).toHaveCount(0)
  await expect(page.getByTestId('deep-web-search-provider-toggles')).toHaveCount(0)
  await expect(providers.getByTestId('deep-web-search-codex-model-picker')).toBeVisible()
  const codexToggle = providers.getByTestId('deep-web-search-toggle-codex')
  await expect(codexToggle).toHaveAccessibleName(/Codex|provider\.codex/)
  await expectMinTarget(codexToggle, 'deep-web Codex toggle')
  await expect(providers.getByTestId('deep-web-search-toggle-exa')).toBeVisible()
  await expect(providers.getByTestId('deep-web-search-toggle-deepseek')).toBeVisible()
  await expect(providers.getByTestId('deep-web-search-toggle-tinyfish')).toBeVisible()
  await expect(providers.getByTestId('deep-web-search-toggle-duckduckgo')).toBeVisible()
  await expect(page.getByTestId('deep-web-search-provider-codex')).toContainText(/Codex|provider\.codex/)
  await expect(page.getByTestId('deep-web-search-provider-deepseek')).toContainText(/DeepSeek|provider\.deepseek/)
  await expect(codexToggle).toHaveAttribute('aria-checked', 'true')
  await codexToggle.click()
  await expect(page.getByTestId('deep-web-search-provider-codex')).toHaveClass(/is-off/)
  await expect(codexToggle).toHaveAttribute('aria-checked', 'false')
  const deepSaved = page.waitForResponse(response => response.url().includes('/v1/extensions/deep-web-search/config') && response.request().method() === 'PATCH')
  await page.getByTestId('deep-web-search-config-save').click()
  await deepSaved
  await page.reload()
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-extensions').click()
  await page.getByTestId(`cfg-configure-${name}`).click()
  await expect(page.getByTestId('deep-web-search-toggle-codex')).toHaveAttribute('aria-checked', 'false', { timeout: 15_000 })
  const fetchContent = page.getByTestId('deep-web-search-fetch-content')
  const fetchTarget = page.locator('.extension-config-options label').filter({ has: fetchContent })
  await expect(fetchContent).toHaveAccessibleName(/.+/)
  await expectMinTarget(fetchTarget, 'deep-web fetch-content checkbox')

  await request.patch('/v1/extensions', { headers, data: { disabled: [name] } })
})

test('extension ui.setStatus chip opens panel modal', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = sidecarBin(home, 'ext-ui-sidecar')
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
  await openSessionRow(page)
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
  await expect(page.getByTestId('cfg-configure-goalui')).toBeVisible()
  await page.getByTestId('cfg-configure-goalui').click()
  await expect(page.getByTestId('settings')).toHaveCount(0)
  await expect(page.getByTestId('ext-panel')).toBeVisible()
  await expect(page.getByTestId('ext-nav-goalui')).toHaveAttribute('aria-current', 'page')
})

test('opening a session locks composer until runtime.ready', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = sidecarBin(home, 'ext-lock-sidecar')
  writeExt(home, 'slowboot', bin, { env: { KI_INIT_WAIT_FILE: join(home, 'slowboot-release') } })

  await page.goto('/')
  await reloadServer(page, request)
  await newSession(page)
  const input = page.getByTestId('composer-input')
  await expect(input).toBeDisabled()
  await expect(input).toHaveAttribute('placeholder', /正在加载扩展|Loading extensions/)
  await expect(page.getByTestId('command-btn')).toBeDisabled()
  // Why: release the sidecar gate only after observing the locked composer; a
  // fixed init sleep either flakes under load or wastes time.
  writeFileSync(join(home, 'slowboot-release'), '')
  await expect(input).toBeEnabled({ timeout: 15_000 })
})

test('slash palette is two-level for completions', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = sidecarBin(home, 'ext-slash-sidecar')
  writeExt(home, 'goalcmd', bin, { capabilities: ['command'], env: { KI_COMPLETIONS: '1' } })

  await page.goto('/')
  await reloadServer(page, request)
  await newSession(page)
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

test('top bar folds extra chips into one inspector modal', async ({ page, request }, testInfo) => {
  test.setTimeout(60_000)
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const bin = sidecarBin(home, 'ext-fold-sidecar')
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
  await newSession(page)
  await expect(page.getByTestId('composer-input')).toBeEnabled({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-vault')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('ext-chip-syncx')).toBeVisible()
  await expect(page.getByTestId('ext-chip-goalfold')).toBeVisible()
  await expect(page.getByTestId('ext-chip-indexx')).toHaveCount(0)
  await expect(page.getByTestId('ext-chip-notesx')).toHaveCount(0)
  await expect(page.getByTestId('ext-chips-more').locator('.ext-more-wide')).toHaveText('+2')

  // Why: independent Playwright invocations can exercise this fixture at the
  // same time. The per-test output path prevents their screenshots colliding.
  const shotDir = process.env.KI_SHOT_DIR || testInfo.outputPath('ext-inspector')
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
  const bin = sidecarBin(home, 'global-config-sidecar')
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

  await newSession(page)
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
