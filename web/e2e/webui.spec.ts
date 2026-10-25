import { expect, test, type Locator, type Page, type Route } from '@playwright/test'
import { expandWorkspaces, openSessionRow } from './sidebar.ts'
import { appendFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { applyFollowTail } from '../src/lib/follow-tail.ts'
import { nodeTypes, nodeValues, parseMarkdown } from './markdown-parse.ts'
import { serverToken, statePath } from './global-setup.ts'
import { MIN_TOUCH_SIZE } from './touch-target.ts'
import { focusedSessionReader, newSession } from './session.ts'
import { contextCategory, expectColoredContextSegments, expectContextItemCount, expectConversationPosition, openContextCategory, openContextItem, openSystemSources } from './context.ts'
import type { Entry } from '../src/api/types'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

async function pressComposingEnter(input: Locator): Promise<void> {
  for (const init of [{ isComposing: true }, { keyCode: 229 }]) {
    const unhandled = await input.evaluate((el, init) => el.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Enter', code: 'Enter', bubbles: true, cancelable: true, ...init,
    })), init)
    expect(unhandled, `candidate confirmation ${JSON.stringify(init)} should stay with the input method`).toBe(true)
  }
}

function trackComposerRequests(page: Page): { prompt: number; abort: number } {
  const counts = { prompt: 0, abort: 0 }
  page.on('request', request => {
    if (request.method() !== 'POST') return
    const action = new URL(request.url()).pathname.match(/^\/v1\/sessions\/[^/]+\/(prompt|abort)$/)?.[1]
    if (action === 'prompt' || action === 'abort') counts[action]++
  })
  return counts
}

async function expectMinTarget(locator: Locator, label: string): Promise<void> {
  await expect(locator, `${label} should be visible`).toBeVisible()
  const box = await locator.boundingBox()
  expect(box, `${label} should have a layout box`).toBeTruthy()
  expect(box!.width, `${label} width`).toBeGreaterThanOrEqual(MIN_TOUCH_SIZE)
  expect(box!.height, `${label} height`).toBeGreaterThanOrEqual(MIN_TOUCH_SIZE)
}

test.describe.configure({ mode: 'parallel' })

test('slash palette is available before creating a session', async ({ page }) => {
  await page.goto('/')
  const input = page.getByTestId('composer-input')
  await expect(input).toBeVisible()
  await input.fill('/')
  await expect(page.getByTestId('command-palette')).toBeVisible()
  await expect(page.getByTestId('command-item-new')).toBeVisible()
})

test('session list conditional fetch returns 304', async ({ page }) => {
  await page.goto('/')
  const out = await page.evaluate(async () => {
    const first = await fetch('/v1/sessions', { credentials: 'same-origin', cache: 'no-store' })
    const etag = first.headers.get('ETag')
    await first.text()
    const second = await fetch('/v1/sessions', {
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { 'If-None-Match': etag ?? '' },
    })
    return { etag, first: first.status, second: second.status }
  })
  expect(out.first).toBe(200)
  expect(out.etag).toBeTruthy()
  expect(out.second).toBe(304)
})

test('settings navigation and controls are consistent', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await expect(page.getByRole('heading', { name: '开始对话' })).toBeVisible()
  await expect(page.getByTestId('composer-input')).toBeVisible()
  await page.getByTestId('open-settings').click()
  await expect(page.getByTestId('settings-tab-providers')).toHaveText('模型供应商')
  await expect(page.getByTestId('settings-tab-skills')).toHaveText('Skills')
  await expect(page.getByTestId('settings-tab-tools')).toHaveText('Tools')
  await expect(page.getByTestId('settings-tab-mcp')).toHaveText('MCP')
  await expect(page.getByTestId('settings-tab-extensions')).toHaveText('Extensions')
  await expect(page.getByTestId('settings-tab-prompt')).toHaveText('System Prompt')
  await expect(page.getByTestId('settings-tab-message')).toHaveText('Message')
  await expect(page.getByTestId('settings-tab-notifications')).toHaveText('通知')
  await expect(page.getByTestId('settings-tab-appearance')).toHaveText('主题和语言')
  await expect(page.getByTestId('settings-tab-about')).toHaveText('关于')
  for (const pageName of ['providers', 'skills', 'tools', 'mcp', 'extensions', 'prompt', 'message', 'notifications', 'appearance', 'about']) {
    await expect(page.locator(`#settings-panel-${pageName}`)).toHaveCount(1)
  }
  await expect(page.locator('#settings-panel-skills')).toHaveAttribute('hidden', '')
  const providersTab = page.getByTestId('settings-tab-providers')
  await expect(providersTab).toHaveAttribute('tabindex', '0')
  await expect(providersTab).toHaveAttribute('aria-controls', 'settings-panel-providers')
  await providersTab.focus()
  await providersTab.press('End')
  const aboutTab = page.getByTestId('settings-tab-about')
  await expect(aboutTab).toBeFocused()
  await expect(aboutTab).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'settings-tab-about-control')
  await expect(page.locator('#settings-panel-providers')).toHaveAttribute('hidden', '')
  await aboutTab.press('Home')
  await expect(providersTab).toBeFocused()
  await expect(providersTab).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('tabpanel')).toHaveAttribute('id', 'settings-panel-providers')
  await expect(page.getByTestId('provider-settings')).toContainText('Anthropic')
  await expect(page.locator('.provider-nav [data-provider-id="anthropic"]')).toHaveText('Anthropic')
  await expect(page.locator('.provider-nav')).not.toContainText('缺少密钥')
  await expect(page.locator('.provider-nav')).not.toContainText('API key needed')
  const providerOrder = await page.locator('.provider-nav [data-provider-id]').evaluateAll(els => els.map(el => ({
    id: el.getAttribute('data-provider-id'),
    ready: !!el.querySelector('.provider-status-dot.ready') && !el.classList.contains('is-disabled'),
  })))
  const firstUnready = providerOrder.findIndex(item => !item.ready)
  if (firstUnready >= 0) {
    expect(providerOrder.slice(firstUnready).every(item => !item.ready)).toBe(true)
  }
  if (firstUnready > 0 && firstUnready < providerOrder.length) {
    await expect(page.getByTestId('provider-nav-split')).toHaveCount(1)
  }
  await expect(page.getByTestId('settings-theme')).toHaveCount(0)

  const baseURL = page.getByTestId('provider-base-url')
  const apiProtocol = page.getByTestId('provider-api')
  await expect(baseURL).toBeVisible()
  await expect(apiProtocol).toBeVisible()
  const controlMetrics = await Promise.all([baseURL, apiProtocol].map(locator => locator.evaluate(element => {
    const style = getComputedStyle(element)
    return { height: style.height, fontSize: style.fontSize, lineHeight: style.lineHeight, fontFamily: style.fontFamily }
  })))
  expect(controlMetrics[0]).toEqual(controlMetrics[1])
  expect(controlMetrics[0].height).toBe('40px')
  expect(controlMetrics[0].fontSize).toBe('14px')
  await apiProtocol.click()
  const apiListbox = page.getByRole('listbox', { name: 'API 协议' })
  await expect(apiListbox).toBeVisible()
  await expect(apiListbox.locator('[role="option"][aria-selected="true"]')).toHaveCount(1)
  await apiProtocol.press('ArrowDown')
  await apiProtocol.press('Escape')
  await expect(page.getByRole('listbox', { name: 'API 协议' })).toHaveCount(0)

  const providerList = page.locator('.provider-nav')
  const providerContent = page.locator('.provider-content')
  const scrollLayout = await page.evaluate(() => {
    const outer = document.querySelector<HTMLElement>('.settings-page')!
    const left = document.querySelector<HTMLElement>('.provider-nav')!
    const right = document.querySelector<HTMLElement>('.provider-content')!
    return {
      outerOverflow: getComputedStyle(outer).overflow,
      leftOverflow: getComputedStyle(left).overflowY,
      rightOverflow: getComputedStyle(right).overflowY,
      leftScrollable: left.scrollHeight > left.clientHeight,
      rightScrollable: right.scrollHeight > right.clientHeight,
    }
  })
  expect(scrollLayout).toEqual({ outerOverflow: 'hidden', leftOverflow: 'auto', rightOverflow: 'auto', leftScrollable: true, rightScrollable: true })
  await providerList.evaluate(element => { element.scrollTop = 120 })
  expect(await providerList.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
  expect(await providerContent.evaluate(element => element.scrollTop)).toBe(0)
  const leftScroll = await providerList.evaluate(element => element.scrollTop)
  await providerContent.evaluate(element => { element.scrollTop = 120 })
  expect(await providerContent.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
  expect(await providerList.evaluate(element => element.scrollTop)).toBe(leftScroll)

  await page.getByTestId('settings-tab-appearance').click()
  await expect(page.getByTestId('appearance-settings')).toBeVisible()
  await expect(page.getByTestId('settings-theme')).toBeVisible()
  await expect(page.getByTestId('settings-lang')).toBeVisible()
  await page.getByTestId('settings-tab-skills').click()
  await expect(page.getByTestId('skills-settings')).toBeVisible()
  await page.getByTestId('settings-tab-tools').click()
  await expect(page.getByTestId('tools-settings')).toBeVisible()
  const agentToggle = page.getByTestId('tool-on-spawn_agent')
  const patchToggle = page.getByTestId('tool-on-apply_patch')
  await expect(patchToggle).toBeVisible()
  await expect(patchToggle).toHaveAttribute('aria-checked', 'true')
  await patchToggle.click()
  await expect(patchToggle).toHaveAttribute('aria-checked', 'false')
  await patchToggle.click()
  await expect(patchToggle).toHaveAttribute('aria-checked', 'true')
  await expect(agentToggle).toHaveAttribute('aria-checked', 'true')
  await agentToggle.click()
  await expect(agentToggle).toHaveAttribute('aria-checked', 'false')
  await agentToggle.click()
  await expect(agentToggle).toHaveAttribute('aria-checked', 'true')
  await page.getByTestId('settings-tab-extensions').click()
  await expect(page.getByTestId('extensions-settings')).toBeVisible()
  await page.getByTestId('settings-tab-agents').click()
  await expect(page.getByTestId('agents-settings')).toBeVisible()
  const maxThreads = page.getByTestId('agents-max-threads')
  await expect(maxThreads).toHaveValue('4')
  await expect(page.getByTestId('agents-max-threads-save')).toBeDisabled()
  await expect(maxThreads).toHaveAttribute('min', '2')
  await expect(maxThreads).toHaveAttribute('max', '64')
  for (const invalid of ['1', '65', '128']) {
    await maxThreads.fill(invalid)
    await expect(maxThreads).toHaveAttribute('aria-invalid', 'true')
    await expect(page.getByTestId('agents-max-threads-save')).toBeDisabled()
  }
  await maxThreads.fill('5')
  await page.getByTestId('agents-max-threads-save').click()
  await expect(page.getByTestId('agents-max-threads-save')).toBeDisabled()
  const contextWindow = page.getByTestId('agents-context-window')
  await expect(contextWindow).toHaveValue('500000')
  await contextWindow.fill('4000000')
  await page.getByTestId('agents-context-window-save').click()
  await expect(page.getByTestId('agents-context-window-save')).toBeDisabled()
  await page.getByTestId('settings-tab-message').click()
  await expect(page.getByTestId('message-settings')).toBeVisible()
  await expect(page.getByTestId('busy-steer')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('busy-steer')).toHaveText('Steer')
  await expect(page.getByTestId('busy-queue')).toHaveText('Queue')
  await page.getByTestId('busy-queue').click()
  await expect(page.getByTestId('busy-queue')).toHaveAttribute('aria-checked', 'true')
  await page.getByTestId('settings-tab-appearance').click()
  await page.getByTestId('settings-tab-agents').click()
  await expect(page.getByTestId('agents-context-window')).toHaveValue('4000000')
  await page.getByTestId('settings-tab-appearance').click()
  await page.getByTestId('lang-en').click()
  await expect(page.getByTestId('settings-tab-appearance')).toHaveText('Theme & language')
  await expect(page.getByTestId('tab-conversation')).toHaveText('Chat')
  await page.getByTestId('lang-zh').click()
  await expect(page.getByTestId('settings-tab-appearance')).toHaveText('主题和语言')
  await expect(page.getByTestId('tab-conversation')).toHaveText('对话')
  await page.getByTestId('settings-mask').click({ position: { x: 4, y: 4 } })
  await expect(page.getByTestId('settings')).toHaveCount(0)
  await page.getByTestId('open-model').click()
  await expect(page.getByTestId('model-dialog')).toBeVisible()
  const modelSearch = page.getByTestId('model-search')
  await expect(modelSearch).toBeFocused()
  await modelSearch.fill('anthr snnt')
  await expect(page.getByTestId('model-option')).not.toHaveCount(0)
  for (const spec of await page.getByTestId('model-option').evaluateAll(options => options.map(option => option.getAttribute('data-spec') || ''))) {
    expect(spec.toLowerCase()).toContain('anthropic/')
    expect(spec.toLowerCase()).toContain('sonnet')
  }
  await modelSearch.fill('provider-model-that-does-not-exist')
  await expect(page.getByTestId('model-search-empty')).toBeVisible()
  await page.getByRole('button', { name: '清除模型搜索' }).click()
  await expect(page.getByTestId('model-option')).not.toHaveCount(0)
})

test('nested dialogs isolate lower layers and restore attributes after out-of-order cleanup', async ({ page }) => {
  await page.goto('/')
  const initialBodyOverflow = await page.evaluate(() => document.body.style.overflow)
  await page.getByTestId('open-settings').click()
  const settings = page.getByTestId('settings')
  await expect(settings).toBeVisible()

  await settings.evaluate(element => {
    element.setAttribute('aria-hidden', 'false')
    element.setAttribute('inert', 'preserve-me')
    element.querySelector<HTMLButtonElement>('[data-testid="add-provider"]')?.click()
  })

  const provider = page.getByTestId('new-provider-dialog')
  await expect(provider).toBeVisible()
  await expect(settings).toHaveAttribute('aria-hidden', 'true')
  await expect(settings).toHaveAttribute('inert', '')
  await expect(provider).not.toHaveAttribute('aria-hidden')
  await expect(provider).not.toHaveAttribute('inert')
  await expect(provider.getByLabel('供应商 ID')).toBeFocused()
  await provider.locator('.provider-dialog-close').focus()
  await page.keyboard.press('Shift+Tab')
  await expect(provider.getByRole('button', { name: '创建供应商' })).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(provider.locator('.provider-dialog-close')).toBeFocused()

  await page.locator('button[aria-label="添加图片或文件"]').first().evaluate(element => (element as HTMLButtonElement).click())
  const attachments = page.getByRole('dialog', { name: '选择图片或文件' })
  await expect(attachments).toBeVisible()
  await expect(settings).toHaveAttribute('aria-hidden', 'true')
  await expect(settings).toHaveAttribute('inert', '')
  await expect(provider).toHaveAttribute('aria-hidden', 'true')
  await expect(provider).toHaveAttribute('inert', '')
  await expect(attachments).not.toHaveAttribute('aria-hidden')
  await expect(attachments).not.toHaveAttribute('inert')

  await provider.locator('.provider-dialog-close').evaluate(element => (element as HTMLButtonElement).click())
  await expect(provider).toHaveCount(0)
  await expect(settings).toHaveAttribute('aria-hidden', 'true')
  await expect(settings).toHaveAttribute('inert', '')
  await expect(attachments).not.toHaveAttribute('aria-hidden')
  await expect(attachments).not.toHaveAttribute('inert')

  await page.keyboard.press('Escape')
  await expect(attachments).toHaveCount(0)
  await expect(settings).toHaveAttribute('aria-hidden', 'false')
  await expect(settings).toHaveAttribute('inert', 'preserve-me')
  expect(await page.evaluate(() => document.body.style.overflow)).toBe('hidden')

  await settings.evaluate(element => {
    element.removeAttribute('aria-hidden')
    element.removeAttribute('inert')
  })
  await settings.getByRole('button', { name: '关闭对话框' }).click()
  await expect(settings).toHaveCount(0)
  expect(await page.evaluate(() => document.body.style.overflow)).toBe(initialBodyOverflow)
})

test('provider settings supports a complete add and edit flow', async ({ page }) => {
  const providerID = `pw-provider-${Date.now()}`
  const modelID = `pw-model-${Date.now()}`
  let refreshedMagpie = false
  await page.route('**/v1/providers', async route => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const catalog = await response.json() as { providers: Array<{ id: string; models: unknown[]; customized?: boolean; defaultModel: string; credential: unknown }> }
    if (refreshedMagpie) {
      const magpie = catalog.providers.find(provider => provider.id === 'magpie')
      if (magpie) {
        magpie.models = [{
          id: 'vendor/model/nested', name: 'Magpie Synced Model', provider: 'magpie',
          api: 'completions', baseUrl: 'http://127.0.0.1:3425/v1', enabled: true, builtin: false,
          contextWindow: 262144, maxTokens: 32768, input: ['text', 'image'], reasoning: true, cost: null,
        }]
        magpie.defaultModel = 'vendor/model/nested'
        magpie.customized = true
        magpie.credential = { configured: true, source: 'stored', type: 'api_key' }
      }
    }
    await route.fulfill({ status: response.status(), contentType: 'application/json', body: JSON.stringify(catalog) })
  })
  await page.route('**/v1/providers/magpie/models/refresh', async route => {
    refreshedMagpie = true
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
  })
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('add-provider').click()

  await expect(page.getByTestId('new-provider-dialog')).toBeVisible()
  await expect(page.getByTestId('new-provider-form').getByLabel('供应商 ID')).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('new-provider-dialog')).toHaveCount(0)
  await expect(page.getByTestId('settings')).toBeVisible()
  await page.getByTestId('add-provider').evaluate(button => {
    button.addEventListener('click', () => {
      requestAnimationFrame(() => {
        document.querySelector<HTMLInputElement>('[data-testid="new-provider-form"] input[type="url"]')?.focus()
      })
    }, { capture: true, once: true })
  })
  await page.getByTestId('add-provider').click()

  const create = page.getByTestId('new-provider-form')
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
  await expect(create.getByLabel('Base URL')).toBeFocused()
  await create.getByLabel('供应商 ID').fill(providerID)
  await create.getByLabel('显示名称').fill('Playwright Provider')
  await create.getByLabel('Base URL').fill('https://example.test/v1')
  await expect(create.getByLabel('供应商 ID')).toHaveValue(providerID)
  await expect(create.getByLabel('显示名称')).toHaveValue('Playwright Provider')
  await expect(create.getByLabel('Base URL')).toHaveValue('https://example.test/v1')
  const protocol = create.getByRole('combobox', { name: 'API 协议' })
  await protocol.click()
  await expect(protocol).toHaveAttribute('aria-expanded', 'true')
  await create.getByLabel('Base URL').evaluate(input => input.dispatchEvent(new Event('scroll')))
  await expect(protocol).toHaveAttribute('aria-expanded', 'true')
  await create.evaluate(dialog => dialog.dispatchEvent(new Event('scroll')))
  await expect(protocol).toHaveAttribute('aria-expanded', 'false')
  await protocol.click()
  await page.getByRole('option', { name: 'Responses' }).click()
  await expect(protocol).toHaveText('Responses')
  await create.getByLabel('首个模型 ID').fill('starter-model')
  await create.getByRole('button', { name: '创建供应商' }).click()

  await expect(page.getByTestId('new-provider-dialog')).toHaveCount(0)
  await expect(page.locator(`.provider-nav [data-provider-id="${providerID}"]`)).toHaveText('Playwright Provider')
  const connection = page.getByTestId('provider-connection-form')
  await expect(connection.getByLabel('供应商 ID')).toHaveValue(providerID)
  await expect(connection.getByRole('combobox', { name: 'API 协议' })).toHaveText('Responses')
  await connection.getByLabel('显示名称').fill('Playwright Provider Edited')
  await connection.getByLabel('Base URL').fill('https://example.test/responses/v1')
  await connection.getByRole('button', { name: '保存更改' }).click()
  await expect(page.getByRole('heading', { name: 'Playwright Provider Edited' })).toBeVisible()
  await expect(connection.getByLabel('Base URL')).toHaveValue('https://example.test/responses/v1')

  await page.getByTestId('add-model').click()
  const model = page.getByTestId('new-model-form')
  await model.getByLabel('模型 ID').fill(modelID)
  await model.getByLabel('显示名称').fill('Playwright Model')
  await model.getByLabel('上下文窗口').fill('64000')
  await model.getByLabel('最大输出').fill('8192')
  await model.getByRole('button', { name: '添加', exact: true }).click()
  const modelRow = page.getByTestId('provider-model-row').filter({ hasText: modelID })
  await expect(modelRow).toContainText('64,000 ctx')
  await expect(modelRow.getByRole('checkbox')).toHaveAccessibleName(/Playwright Model/)
  await expectMinTarget(modelRow.locator('.compact-switch'), 'model enabled switch')

  await modelRow.getByTestId('edit-model').click()
  const editDlg = page.getByTestId('model-advanced')
  await expect(editDlg).toBeVisible()
  await expect(editDlg.locator('textarea')).toBeFocused()
  const parsed = JSON.parse(await editDlg.locator('textarea').inputValue()) as { name?: string; maxTokens?: number }
  expect(parsed.name).toBe('Playwright Model')
  await page.keyboard.press('Escape')
  await expect(editDlg).toHaveCount(0)
  await expect(page.getByTestId('settings')).toBeVisible()

  await modelRow.getByTestId('edit-model').click()
  const ta = page.getByTestId('model-advanced').locator('textarea')
  const body = JSON.parse(await ta.inputValue()) as Record<string, unknown>
  body.maxTokens = 4096
  await ta.fill(JSON.stringify(body, null, 2))
  await page.getByTestId('model-advanced').getByRole('button', { name: '保存' }).click()
  await expect(page.getByTestId('model-advanced')).toHaveCount(0)
  await modelRow.getByTestId('edit-model').click()
  expect((JSON.parse(await page.getByTestId('model-advanced').locator('textarea').inputValue()) as { maxTokens?: number }).maxTokens).toBe(4096)
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: '删除供应商' }).click()
  await expect(page.locator(`.provider-nav [data-provider-id="${providerID}"]`)).toHaveCount(0)
  await page.locator('.provider-nav [data-provider-id="magpie"]').click()
  const refreshMagpie = page.getByTestId('refresh-magpie-models')
  await expect(page.locator('.magpie-sync-card')).toBeVisible()
  await expect(refreshMagpie).toBeVisible()
  await expect(refreshMagpie).toHaveText('自动获取模型列表')
  await expectMinTarget(refreshMagpie, 'Magpie catalog refresh')
  await refreshMagpie.click()
  const synced = page.getByTestId('provider-model-row').filter({ hasText: 'vendor/model/nested' })
  await expect(synced).toContainText('Magpie Synced Model')
  await expect(synced).toContainText('262,144 ctx')
})

test('markdown parse keeps fences, emphasis, CJK, and streaming closers', () => {
  const inline = parseMarkdown('use the `Read` tool')
  expect(nodeValues(inline, 'inlineCode')).toEqual(['Read'])

  const nested = parseMarkdown('see `` `nested` `` here')
  expect(nodeValues(nested, 'inlineCode')).toEqual(['`nested`'])

  const fullwidth = parseMarkdown('path: \uFF40internal/tools\uFF40')
  expect(nodeValues(fullwidth, 'inlineCode')).toEqual(['internal/tools'])

  const fence = parseMarkdown('  ## Title\n\n  ```go\nfmt.Println("hi")\n  ```\n')
  expect(nodeTypes(fence)).toContain('heading')
  expect(nodeValues(fence, 'code')).toEqual(['fmt.Println("hi")'])

  const quote = parseMarkdown('> quoted `x`')
  expect(nodeTypes(quote)).toContain('blockquote')
  expect(nodeValues(quote, 'inlineCode')).toEqual(['x'])

  const em = parseMarkdown('**bold** and *em*')
  expect(nodeTypes(em)).toEqual(expect.arrayContaining(['strong', 'emphasis']))
  expect(nodeValues(em, 'text')).toEqual(['bold', ' and ', 'em'])

  const cjk = parseMarkdown('这是**强调。**结尾')
  expect(nodeTypes(cjk)).toContain('strong')
  expect(nodeValues(cjk, 'text')).toEqual(['这是', '强调。', '结尾'])

  const streaming = parseMarkdown('before **bold', true)
  expect(nodeTypes(streaming)).toContain('strong')
  expect(nodeValues(streaming, 'text')).toEqual(['before ', 'bold'])

  const leftover = parseMarkdown('before **bold', false)
  expect(nodeTypes(leftover)).not.toContain('strong')
  expect(nodeValues(leftover, 'text')).toEqual(['before **bold'])

  const table = parseMarkdown('| Col A | Col B |\n| --- | --- |\n| 1 | 2 |\n')
  expect(nodeTypes(table)).toContain('table')

  const mermaid = parseMarkdown('```mermaid\nflowchart LR\n  Start --> End\n```\n')
  expect(nodeTypes(mermaid)).toContain('code')
  expect(nodeValues(mermaid, 'code')).toEqual(['flowchart LR\n  Start --> End'])
})

test('chat and context browser talk to the fake runtime', async ({ page }) => {
  const prompt = `hello from playwright ${Date.now()}`
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(prompt)
  await input.press('Enter')
  await expect(page.locator('.session-row.active')).toBeVisible()

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('request-nav')).toHaveCount(1)
  await expect(page.getByTestId('request-nav-panel')).toHaveCount(0)
  await expect(page.getByTestId('assistant-message').first()).toContainText('ok')
  await expect(page.getByTestId('chat-system-prompt')).toHaveCount(0)
  await expect(page.getByTestId('session-stats')).toContainText('缓存命中 91.84%')
  await expect(page.getByTestId('session-stats')).toContainText('输入 98 · 输出 2')
  const turnDivider = page.getByTestId('turn-divider')
  await expect(turnDivider).toHaveCount(1)
  await expect(turnDivider).toHaveAttribute('data-turn', '1')
  await expect(turnDivider).toContainText('第 1 轮')
  await expect(turnDivider).toContainText('1 步')
  await expect(turnDivider).toContainText('缓存命中 91.84%')
  await expect(turnDivider.getByTestId('turn-cache-miss')).toContainText('缓存未命中 0 次')
  await expect(turnDivider.getByTestId('turn-elapsed')).toBeVisible()
  await expect(page.getByTestId('session-title').filter({ hasText: prompt })).toBeVisible()
  const asstActions = page.getByTestId('assistant-message').getByTestId('asst-actions')
  await expect(asstActions.getByTestId('copy-msg')).toBeVisible()
  await expect(asstActions.getByTestId('fork-msg')).toBeVisible()
  await expect(asstActions.getByTestId('regen-msg')).toBeVisible()
  await expect(asstActions.getByTestId('context-msg')).toBeVisible()
  const userActions = page.getByTestId('user-actions')
  await expect(userActions.getByTestId('copy-msg')).toBeVisible()
  await expect(userActions.getByTestId('edit-msg')).toBeVisible()

  const listed = await page.evaluate(async () => {
    const res = await fetch('/v1/sessions', { credentials: 'same-origin' })
    if (!res.ok) throw new Error(`list ${res.status}`)
    return res.json() as Promise<Array<{ title?: string }>>
  })
  expect(listed.some(s => (s.title ?? '').includes(prompt))).toBeTruthy()

  await page.getByTestId('tab-context').click()
  await expect(page.getByTestId('context-view')).toBeVisible()
  await expect(page.getByTestId('context-current')).toBeVisible()
  await expect(page.getByTestId('context-trend')).toBeVisible()
  await expect(page.getByTestId('context-events')).toBeVisible()
  const human = await openContextCategory(page, 'human')
  const userItem = human.locator('.context-item').first()
  await openContextItem(userItem)
  await expect(userItem.getByTestId('context-item-raw')).toContainText(prompt)
  const assistant = await openContextCategory(page, 'assistant')
  await openContextItem(assistant.locator('.context-item').first())
  await expect(assistant).toContainText('ok')
  const tools = await openContextCategory(page, 'tools')
  const readTool = tools.locator('.context-item').filter({ has: page.locator('summary', { hasText: /^read\b/ }) }).first()
  await openContextItem(readTool)
  await expect(readTool).toContainText('Read a file')
  await expect(readTool).toContainText('file_path')
  await expect(readTool.getByRole('button', { name: /复制|Copy/ })).toBeVisible()
  const system = await openContextCategory(page, 'system')
  await openContextItem(system.locator('.context-item').first())
  const builtin = page.locator('.context-system-source[data-source-kind="builtin"]').first()
  await openContextItem(builtin)
  await expect(builtin).toContainText('You are a helpful assistant operating inside ki')

  await page.getByTestId('context-search').fill(prompt)
  await expect(contextCategory(page, 'human').locator('.context-item')).toHaveCount(1)
  await expect(contextCategory(page, 'assistant').locator('.context-item')).toHaveCount(0)
  await page.getByTestId('context-search').fill('')
  await page.getByTestId('context-trend-delta').click()
  await expect(page.getByTestId('context-trend-delta')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('context-trend-turn').click()
  await expect(page.getByTestId('context-trend-turn')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('context-trend-step').click()
  await page.getByTestId('context-trend-total').click()
  await page.getByTestId('context-step').first().click()
  const requestSelect = page.getByTestId('context-request-select')
  await expect(requestSelect).not.toHaveValue('')
  await expect(page.getByTestId('context-browser').getByTestId('context-request-summary')).toContainText(/轮次 1.*步骤 1|Turn 1.*Step 1/)
  await expectContextItemCount(page, 'assistant', 0)
  const requestHuman = await openContextCategory(page, 'human')
  await openContextItem(requestHuman.locator('.context-item').first())
  await expect(requestHuman.getByTestId('context-item-raw')).toContainText(prompt)
  await requestSelect.selectOption('')
  await expectContextItemCount(page, 'assistant', 1)
  await page.getByTestId('tab-conversation').click()
  await asstActions.getByTestId('context-msg').click()
  await expect(page.getByTestId('context-view')).toBeVisible()
  await expect(requestSelect).not.toHaveValue('')
  await expectContextItemCount(page, 'assistant', 0)

  await page.reload()
  await openSessionRow(page)
  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.getByTestId('turn-divider')).toHaveCount(1)
  await page.getByTestId('tab-context').click()
  await expect(page.getByTestId('context-step')).toHaveCount(1)
  await page.getByTestId('context-step').click()
  const historicalHuman = await openContextCategory(page, 'human')
  await openContextItem(historicalHuman.locator('.context-item').first())
  await expect(historicalHuman.getByTestId('context-item-raw')).toContainText(prompt)
  await expectContextItemCount(page, 'assistant', 0)
  await historicalHuman.locator('.context-item').first().getByRole('button', { name: /在对话中查看|Show in conversation/ }).click()
  await expect(page.getByTestId('chat')).toBeVisible()
  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
})

test('context separates system sources and browses the tool input of each request', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const workspace = mkdtempSync(join(tmpdir(), 'ki-context-source-'))
  const skill = join(home, 'skills', 'context-source-skill')
  const extension = join(home, 'extensions', 'context-source-extension')
  const globalAppend = join(home, 'prompt', 'APPEND_SYSTEM.md')
  const previousAppend = existsSync(globalAppend) ? readFileSync(globalAppend) : undefined
  const headers = { Authorization: `Bearer ${serverToken()}` }
  mkdirSync(join(workspace, '.ki', 'prompt'), { recursive: true })
  mkdirSync(join(home, 'prompt'), { recursive: true })
  mkdirSync(skill, { recursive: true })
  mkdirSync(extension, { recursive: true })
  writeFileSync(join(workspace, 'AGENTS.md'), '# Context fixture\nCONTEXT-AGENTS-MARKER\n')
  writeFileSync(globalAppend, 'CONTEXT-GLOBAL-APPEND-MARKER\n')
  writeFileSync(join(workspace, '.ki', 'prompt', 'APPEND_SYSTEM.md'), 'CONTEXT-PROJECT-APPEND-MARKER\n')
  writeFileSync(join(skill, 'SKILL.md'), '---\nname: context-source-skill\ndescription: CONTEXT-SKILL-CATALOG-MARKER\n---\nCONTEXT-UNLOADED-SKILL-BODY\n')
  writeFileSync(join(extension, 'extension.json'), JSON.stringify({
    name: 'context-source-extension', version: '1.0.0',
    capabilities: ['prompt.append'], prompt: { append: ['APPEND.md'] }, runtime: { kind: 'none' },
  }))
  writeFileSync(join(extension, 'APPEND.md'), 'CONTEXT-EXTENSION-PROMPT-MARKER\n')

  try {
    expect((await request.post('/v1/reload', { headers })).ok()).toBe(true)
    const createdWorkspace = await request.post('/v1/workspaces', { headers, data: { path: workspace, title: 'Context sources' } })
    expect(createdWorkspace.ok()).toBe(true)
    const workspaceId = (await createdWorkspace.json() as { id: string }).id
    const createdSession = await request.post('/v1/sessions', { headers, data: { workspaceId } })
    expect(createdSession.ok()).toBe(true)
    const sessionId = (await createdSession.json() as { id: string }).id
    expect((await request.patch(`/v1/sessions/${sessionId}`, { headers, data: { title: 'Context sources fixture' } })).ok()).toBe(true)
    await page.goto('/')
    await expandWorkspaces(page)
    await page.getByTestId('session-title').filter({ hasText: 'Context sources fixture' }).click()
    await sendPrompt(page, 'e2e-bash: echo KI-CONTEXT-TOOL-MARKER')
    await expect(page.getByTestId('assistant-message').last()).toContainText('ok')
    await page.getByTestId('tab-context').click()
    await expect(page.getByTestId('context-step')).toHaveCount(2)
    const viewport = page.viewportSize()
    await page.setViewportSize({ width: 1440, height: 1000 })
    const desktopShot = test.info().outputPath('context-desktop.png')
    await page.screenshot({ path: desktopShot, fullPage: true, animations: 'disabled' })
    await test.info().attach('Context desktop', { path: desktopShot, contentType: 'image/png' })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('mobile-nav-toggle')).toHaveAttribute('aria-expanded', 'false')
    const mobileShot = test.info().outputPath('context-mobile.png')
    await page.screenshot({ path: mobileShot, fullPage: true, animations: 'disabled' })
    await test.info().attach('Context mobile', { path: mobileShot, contentType: 'image/png' })
    await page.getByTestId('context-browser').scrollIntoViewIfNeeded()
    const browserShot = test.info().outputPath('context-mobile-browser.png')
    await page.screenshot({ path: browserShot, fullPage: true, animations: 'disabled' })
    await test.info().attach('Context mobile browser', { path: browserShot, contentType: 'image/png' })
    if (viewport) await page.setViewportSize(viewport)
    await openContextCategory(page, 'system')
    const systemItem = contextCategory(page, 'system').locator('.context-item').first()
    await openContextItem(systemItem)
    await expect(systemItem.getByTestId('context-locate-entry')).toHaveCount(0)
    const schemas = await openContextCategory(page, 'tools')
    const schemaItem = schemas.locator('.context-item').first()
    await openContextItem(schemaItem)
    await expect(schemaItem.getByTestId('context-locate-entry')).toHaveCount(0)
    const projectSources = await openSystemSources(page, 'project')
    await expect(projectSources.filter({ hasText: 'CONTEXT-AGENTS-MARKER' })).toHaveCount(1)
    const operatorSources = await openSystemSources(page, 'append-operator')
    await expect(operatorSources.filter({ hasText: 'CONTEXT-GLOBAL-APPEND-MARKER' })).toHaveCount(1)
    await expect(operatorSources.filter({ hasText: 'CONTEXT-PROJECT-APPEND-MARKER' })).toHaveCount(1)
    const extensionSources = await openSystemSources(page, 'extension')
    await expect(extensionSources.filter({ hasText: 'CONTEXT-EXTENSION-PROMPT-MARKER' })).toHaveCount(1)
    const skillsSources = await openSystemSources(page, 'skills')
    await expect(skillsSources.filter({ hasText: 'CONTEXT-SKILL-CATALOG-MARKER' })).toHaveCount(1)
    await expect(systemItem.getByTestId('context-item-raw').first()).not.toContainText('CONTEXT-UNLOADED-SKILL-BODY')
    await expectContextItemCount(page, 'extension', 0)

    const tools = await openContextCategory(page, 'tool')
    const result = tools.locator('.context-item').first()
    await openContextItem(result)
    await expect(result.getByTestId('context-item-raw')).toContainText('KI-CONTEXT-TOOL-MARKER')
    await page.getByTestId('context-step').first().click()
    await expectContextItemCount(page, 'tool', 0)
    await page.getByTestId('context-step').last().click()
    const requestTools = await openContextCategory(page, 'tool')
    await openContextItem(requestTools.locator('.context-item').first())
    await expect(requestTools.getByTestId('context-item-raw')).toContainText('KI-CONTEXT-TOOL-MARKER')
    await expect(page.getByTestId('context-request-select')).not.toHaveValue('')
    await page.getByTestId('context-request-select').selectOption('')
    await expectContextItemCount(page, 'tool', 1)
    const currentTools = await openContextCategory(page, 'tool')
    const currentResult = currentTools.locator('.context-item').first()
    await openContextItem(currentResult)
    await currentResult.getByTestId('context-locate-entry').click()
    await expect(page.getByTestId('chat')).toBeVisible()
    const toolRow = page.getByTestId('tool-card').filter({ hasText: 'KI-CONTEXT-TOOL-MARKER' })
    await expect(toolRow).toBeVisible()
    const rowId = await toolRow.locator('xpath=ancestor::*[@data-item-key]').getAttribute('data-item-key')
    expect(rowId).toBeTruthy()
    await expect(page.getByTestId('chat')).toHaveAttribute('data-anchor-key', rowId!)
  } finally {
    if (previousAppend === undefined) rmSync(globalAppend, { force: true })
    else writeFileSync(globalAppend, previousAppend)
    rmSync(skill, { recursive: true, force: true })
    rmSync(extension, { recursive: true, force: true })
    rmSync(workspace, { recursive: true, force: true })
    await request.post('/v1/reload', { headers })
  }
})

test('context hydrates historical agent and compaction inputs and locates early checkpoint events', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const created = await request.post('/v1/sessions', { headers, data: {} })
  expect(created.ok()).toBe(true)
  const { id, dir } = await created.json() as { id: string; dir: string }
  const configPath = join(dir, 'config.json')
  const config = JSON.parse(readFileSync(configPath, 'utf8')) as Record<string, unknown>
  const title = `Context checkpoint fixture ${id}`
  const entries: Array<Entry | (Entry & { responses: unknown })> = []
  let parentId = ''
  let clock = Date.parse('2026-10-01T12:00:00Z')
  const push = (entry: Entry | (Entry & { responses: unknown })) => {
    entries.push({ ...entry, parentId, timestamp: new Date(clock++).toISOString() })
    parentId = entry.id
  }
  const input = (entryId: string, text: string, origin?: string) => push({
    type: 'message', id: entryId, message: { role: 'user', origin, content: [{ type: 'text', text }] },
  })
  const header = (entryId: string) => push({
    type: 'request_header', id: entryId, system: `Context checkpoint fixture System ${'fixture rule '.repeat(30)}`,
    tools: [{ name: 'context_fixture_tool', description: 'Historical tool schema fixture', parameters: {
      type: 'object', properties: { query: { type: 'string', description: 'An explicit fixture query for context classification' } },
    } }],
    provider: 'fake', modelId: 'free',
  })
  const answer = (entryId: string, text: string, inputTokens = 10) => push({
    type: 'message', id: entryId, message: { role: 'assistant', content: [{ type: 'text', text }], usage: { input: inputTokens, output: 2 } },
  })
  input('context-old-human', 'Before checkpoint')
  input('context-old-agent', `Historical agent message ${'history '.repeat(100)}CONTEXT-AGENT-FULL-END`, 'agent:history-fixture')
  header('context-old-request')
  push({
    type: 'message', id: 'context-old-answer',
    message: { role: 'assistant', content: [
      { type: 'text', text: `Completed before checkpoint ${'assistant input '.repeat(60)}CONTEXT-ASSISTANT-FULL-END` },
      { type: 'toolCall', id: 'context-fixture-call', name: 'context_fixture_tool', arguments: { query: 'Historical fixture' } },
    ], usage: { input: 10, output: 2 } },
  })
  push({
    type: 'message', id: 'context-old-tool-result',
    message: { role: 'toolResult', toolCallId: 'context-fixture-call', toolName: 'context_fixture_tool',
      content: [{ type: 'text', text: `Historical tool input ${'tool result '.repeat(100)}CONTEXT-TOOL-FULL-END` }] },
  })
  header('context-color-request')
  answer('context-color-answer', 'Own response is not part of the colored request input', 200_000)
  push({
    type: 'compaction', id: 'context-local-checkpoint',
    summary: `Historical local summary ${'summary '.repeat(100)}CONTEXT-SUMMARY-FULL-END`, tokensBefore: 2000,
  })
  push({ type: 'model_change', id: 'context-model-change', provider: 'fake', modelId: 'free' })
  input('context-kept-human', 'After local checkpoint')
  input('context-kept-agent', 'CONTEXT-KEPT-AGENT-MARKER', 'agent:kept-fixture')
  header('context-local-request')
  answer('context-local-answer', 'Completed after local checkpoint')
  push({
    type: 'compaction', id: 'context-remote-checkpoint', tokensBefore: 3000,
    responses: { binding: { provider: 'fake', model: 'free' }, items: [{ type: 'compaction', encrypted_content: 'CONTEXT-SECRET-OPAQUE' }] },
  })
  for (let turn = 0; turn < 12; turn++) {
    input(`context-tail-user-${turn}`, `Checkpoint tail input ${turn}`)
    header(`context-tail-request-${turn}`)
    answer(`context-tail-answer-${turn}`, `Checkpoint tail reply ${turn}`)
  }
  appendFileSync(join(dir, 'events.jsonl'), entries.map(entry => JSON.stringify(entry)).join('\n') + '\n')
  writeFileSync(configPath, JSON.stringify({ ...config, title, activeLeafId: parentId }))
  await page.addInitScript(() => {
    localStorage.setItem('ki-message-view', 'compact')
    localStorage.setItem('ki-message-view-keep', '1')
  })
  await page.goto('/')
  await expandWorkspaces(page)
  await page.getByTestId('session-title').filter({ hasText: title }).click()
  await expect(page.getByTestId('assistant-message').last()).toContainText('Checkpoint tail reply 11')
  await page.getByTestId('tab-context').click()
  const picker = page.getByTestId('context-request-select')
  await expect(picker.locator('option[value="context-old-request"]')).toHaveCount(1)
  expect(await page.getByTestId('context-step').count()).toBeLessThanOrEqual(24)
  await expect(page.getByTestId('context-reported-line')).toBeVisible()
  await expect(page.getByTestId('context-reported-bar')).toHaveCount(0)
  await expectContextItemCount(page, 'remote', 1)
  await expect(page.getByTestId('context-checkpoint-notice')).toBeVisible()
  await expectContextItemCount(page, 'compaction', 0)
  await expectContextItemCount(page, 'agent', 0)
  const remote = await openContextCategory(page, 'remote')
  await openContextItem(remote.locator('.context-item').first())
  await expect(remote.getByTestId('context-item-raw')).not.toContainText('CONTEXT-SECRET-OPAQUE')

  await picker.selectOption('context-old-request')
  await expectContextItemCount(page, 'agent', 1)
  const historicalAgent = contextCategory(page, 'agent').locator('.context-item').first()
  await openContextItem(historicalAgent)
  await expect.poll(async () => Number(await historicalAgent.getAttribute('data-tokens')),
    { message: 'metadata-only Agent content must have its stored full-body estimate before hydration' }).toBeGreaterThan(0)
  await expect(historicalAgent.getByTestId('context-item-raw')).not.toContainText('CONTEXT-AGENT-FULL-END')
  await historicalAgent.getByTestId('context-hydrate').click()
  await expect(historicalAgent.getByTestId('context-item-raw')).toContainText('CONTEXT-AGENT-FULL-END')
  await expectContextItemCount(page, 'assistant', 0)
  await expectContextItemCount(page, 'compaction', 0)

  await picker.selectOption('context-color-request')
  await expectColoredContextSegments(page, 'context-color-request', ['system', 'tools', 'assistant', 'tool'])
  const colorColumn = page.locator('[data-testid="context-step"][data-request-id="context-color-request"]')
  await expect(colorColumn.locator('[data-category="human"]')).toHaveCSS('background-color', 'rgb(228, 87, 86)')
  await expect(colorColumn.locator('[data-category="tool"]')).toHaveCSS('background-color', 'rgb(19, 169, 154)')
  await expect(page.getByTestId('context-reported-line').locator('circle[data-value="200000"]')).toHaveCount(1)
  const historicalTool = (await openContextCategory(page, 'tool')).locator('.context-item').first()
  await openContextItem(historicalTool)
  await expect(historicalTool.getByTestId('context-item-raw')).not.toContainText('CONTEXT-TOOL-FULL-END')
  await expect.poll(async () => Number(await historicalTool.getAttribute('data-tokens')),
    { message: 'historical Tool result estimate must not require its full body' }).toBeGreaterThan(0)

  await picker.selectOption('context-local-request')
  await expectContextItemCount(page, 'compaction', 1)
  await expectContextItemCount(page, 'agent', 1)
  const localSummary = contextCategory(page, 'compaction').locator('.context-item').first()
  await openContextItem(localSummary)
  await expect(localSummary.getByTestId('context-item-raw')).not.toContainText('CONTEXT-SUMMARY-FULL-END')
  await localSummary.getByTestId('context-hydrate').click()
  await expect(localSummary.getByTestId('context-item-raw')).toContainText('CONTEXT-SUMMARY-FULL-END')
  await expectContextItemCount(page, 'remote', 0)

  const events = page.getByTestId('context-events')
  let parked: Route | undefined
  let intercepted = false
  const historyRoute = async (route: Route) => {
    if (!intercepted && new URL(route.request().url()).searchParams.has('before')) {
      intercepted = true
      parked = route
      return
    }
    await route.continue()
  }
  const historyURL = `**/v1/sessions/${id}?**`
  await page.route(historyURL, historyRoute)
  try {
    for (const entryId of ['context-remote-checkpoint', 'context-local-checkpoint']) {
      const event = events.locator(`[data-event-id="${entryId}"]`)
      await expect(event).toHaveAttribute('data-event-type', 'compaction')
      await event.getByTestId('context-event-action').click()
      await expect(page.getByTestId('chat')).toBeVisible()
      if (entryId === 'context-remote-checkpoint') {
        await expect.poll(() => !!parked, { message: 'event navigation must page to its older checkpoint' }).toBe(true)
        await expect(page.getByTestId('chat')).toHaveAttribute('data-scroll-intent', /reading|seeking/)
        await parked!.continue()
        parked = undefined
      }
      await expectConversationPosition(page, entryId)
      await expect(page.getByTestId('user-bubble').filter({ hasText: 'Checkpoint tail input 11' })).toHaveCount(0)
      await page.getByTestId('tab-context').click()
    }
    const modelEvent = events.locator('[data-event-id="context-model-change"]')
    await expect(modelEvent).toHaveAttribute('data-event-type', 'model_change')
    await modelEvent.getByTestId('context-event-action').click()
    await expectConversationPosition(page, 'context-local-checkpoint')
    await page.getByTestId('tab-context').click()
    await picker.selectOption('context-old-request')
    const agentCategory = await openContextCategory(page, 'agent')
    const hiddenAgent = agentCategory.locator('[data-entry-id="context-old-agent"]')
    await openContextItem(hiddenAgent)
    await hiddenAgent.getByTestId('context-locate-entry').click()
    await expectConversationPosition(page, 'context-old-agent')
  } finally {
    await parked?.continue().catch(() => {})
    await page.unroute(historyURL, historyRoute)
  }
})

test('markdown table copy and diagram toggle/download/copy', async ({ page }) => {
  const stubSvg = '<svg xmlns="http://www.w3.org/2000/svg" width="120" height="60"><rect width="120" height="60" fill="#eee"/></svg>'
  await page.route('https://www.plantuml.com/**', route => route.fulfill({ contentType: 'image/svg+xml', body: stubSvg }))
  await page.goto('/')
  await sendPrompt(page, 'e2e-markdown')
  const asst = page.getByTestId('assistant-message').last()
  await expect(asst.getByTestId('md-table')).toBeVisible()
  await expect(asst.getByRole('columnheader', { name: 'Col A' })).toBeVisible()
  await expect(asst.getByRole('cell', { name: '1' })).toBeVisible()
  await asst.getByTestId('md-table-copy').click()
  await expect(asst.getByTestId('md-table-copy')).toHaveAttribute('aria-label', '已复制')

  const mermaid = asst.getByTestId('md-mermaid')
  await expect(mermaid).toBeVisible()
  await expect(mermaid.getByTestId('md-mermaid-diagram')).toHaveAttribute('aria-pressed', 'true')
  await expect(mermaid.getByTestId('md-mermaid-svg').locator('svg')).toBeVisible()
  const actionIds = await mermaid.locator('.md-block-actions button').evaluateAll(els => els.map(el => el.getAttribute('data-testid')))
  expect(actionIds).toEqual(['md-mermaid-download-png', 'md-mermaid-download-svg', 'md-mermaid-copy'])
  await mermaid.getByTestId('md-mermaid-svg').click()
  const mermaidZoom = page.getByTestId('md-mermaid-zoom')
  await expect(mermaidZoom).toBeVisible()
  const level = mermaidZoom.getByTestId('md-mermaid-zoom-level')
  const zoomImage = mermaidZoom.locator('img')
  await expect.poll(() => zoomImage.evaluate(image => image.naturalWidth)).toBeGreaterThan(0)
  const fitted = await mermaidZoom.locator('.diagram-zoom-stage').evaluate(stage => {
    const image = stage.querySelector('img')!
    const scale = Math.min((stage.clientWidth - 32) / image.naturalWidth, (stage.clientHeight - 32) / image.naturalHeight)
    return `${Math.round(scale * 100)}%`
  })
  await expect(level).toHaveText(fitted)
  await mermaidZoom.getByTestId('md-mermaid-zoom-in').click()
  await expect(level).not.toHaveText(fitted)
  await mermaidZoom.getByTestId('md-mermaid-zoom-fit').click()
  await expect(level).toHaveText(fitted)
  await mermaidZoom.getByTestId('md-mermaid-zoom-close').click()
  await expect(mermaidZoom).toHaveCount(0)
  await mermaid.getByTestId('md-mermaid-source').click()
  await expect(mermaid.getByTestId('md-mermaid-source')).toHaveAttribute('aria-pressed', 'true')
  await expect(mermaid.getByTestId('md-mermaid-code')).toContainText('flowchart LR')
  await mermaid.getByTestId('md-mermaid-copy').click()
  await expect(mermaid.getByTestId('md-mermaid-copy')).toHaveAttribute('aria-label', '已复制')
  await mermaid.getByTestId('md-mermaid-diagram').click()
  await expect(mermaid.getByTestId('md-mermaid-svg').locator('svg')).toBeVisible()
  const mermaidSvg = page.waitForEvent('download')
  await mermaid.getByTestId('md-mermaid-download-svg').click()
  expect((await mermaidSvg).suggestedFilename()).toBe('mermaid.svg')
  const mermaidPng = page.waitForEvent('download')
  await mermaid.getByTestId('md-mermaid-download-png').click()
  expect((await mermaidPng).suggestedFilename()).toBe('mermaid.png')

  const plantuml = asst.getByTestId('md-plantuml')
  await expect(plantuml).toBeVisible()
  await expect(plantuml.getByTestId('md-plantuml-diagram')).toHaveAttribute('aria-pressed', 'true')
  await expect(plantuml.getByTestId('md-plantuml-svg').locator('img')).toBeVisible()
  const frame = await plantuml.getByTestId('md-plantuml-svg').boundingBox()
  const image = await plantuml.getByTestId('md-plantuml-svg').locator('img').boundingBox()
  expect(frame && image).toBeTruthy()
  expect(Math.abs((image!.x + image!.width / 2) - (frame!.x + frame!.width / 2))).toBeLessThan(2)
  await plantuml.getByTestId('md-plantuml-svg').click()
  const plantumlZoom = page.getByTestId('md-plantuml-zoom')
  await expect(plantumlZoom).toBeVisible()
  await plantumlZoom.getByTestId('md-plantuml-zoom-close').click()
  await expect(plantumlZoom).toHaveCount(0)
  await plantuml.getByTestId('md-plantuml-source').click()
  await expect(plantuml.getByTestId('md-plantuml-code')).toContainText('@startuml')
  await plantuml.getByTestId('md-plantuml-copy').click()
  await expect(plantuml.getByTestId('md-plantuml-copy')).toHaveAttribute('aria-label', '已复制')
  await plantuml.getByTestId('md-plantuml-diagram').click()
  await expect(plantuml.getByTestId('md-plantuml-svg').locator('img')).toBeVisible()
  const plantumlPng = page.waitForEvent('download')
  await plantuml.getByTestId('md-plantuml-download-png').click()
  expect((await plantumlPng).suggestedFilename()).toBe('plantuml.png')
})

test('edit branches in place with attachments and fork opens a new session', async ({ page }) => {
  const original = `branch-original-${Date.now()}`
  const edited = `branch-edited-${Date.now()}`
  await page.goto('/')
  await sendPrompt(page, original)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')

  const before = await page.evaluate(async () => {
    const sessions = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ id: string; cwd: string; title?: string }>
    const original = sessions.find(s => (s.title ?? '').includes('branch-original'))!
    return { count: sessions.length, cwd: original.cwd, id: original.id }
  })
  writeFileSync(join(before.cwd, 'edit-attachment.txt'), 'attachment marker')
  writeFileSync(join(before.cwd, 'preview.png'), Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64'))
  mkdirSync(join(before.cwd, 'Pictures'))

  await page.getByTestId('edit-msg').click()
  await page.getByTestId('edit-input').fill(edited)
  await page.getByRole('button', { name: '添加图片或文件' }).click()
  await page.getByRole('button', { name: 'Pictures' }).click()
  await expect(page.getByText('这个目录中没有文件')).toBeVisible()
  await expect(page.getByRole('dialog', { name: '选择图片或文件' })).toBeVisible()
  await page.locator('.attachment-crumb button').nth(-2).click()
  await page.getByRole('button', { name: /preview\.png/ }).click()
  await expect(page.locator('.attachment-preview img')).toBeVisible()
  await expect.poll(() => page.locator('.attachment-preview img').evaluate(img => (img as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
  await page.getByRole('button', { name: '添加', exact: true }).click()
  await expect(page.locator('.composer-image img')).toBeVisible()
  await expect(page.locator('.attachment-draft-image')).not.toContainText('preview.png')
  await page.getByRole('button', { name: '放大查看图片' }).click()
  await expect(page.getByRole('dialog', { name: '图片预览' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: '图片预览' })).toHaveCount(0)
  await page.getByRole('button', { name: '添加图片或文件' }).click()
  await page.getByRole('button', { name: /edit-attachment\.txt/ }).click()
  await expect(page.locator('.attachment-text-preview pre')).toContainText('attachment marker')
  await page.getByRole('button', { name: '添加', exact: true }).click()
  await expect(page.locator('.attachment-draft-file[title="edit-attachment.txt"]')).toBeVisible()
	await page.getByTestId('edit-input').evaluate(input => {
	  const transfer = new DataTransfer()
	  transfer.items.add(new File(['pasted attachment'], 'pasted.txt', { type: 'text/plain' }))
	  const event = new Event('paste', { bubbles: true, cancelable: true })
	  Object.defineProperty(event, 'clipboardData', { value: transfer })
	  input.dispatchEvent(event)
	})
	await expect(page.locator('.attachment-draft-file[title="pasted.txt"]')).toBeVisible()
	await page.evaluate(() => {
	  const transfer = new DataTransfer()
	  transfer.items.add(new File(['global drop'], 'global-drop.go', { type: 'text/plain' }))
	  ;(window as unknown as { __dropTransfer?: DataTransfer }).__dropTransfer = transfer
	  document.body.dispatchEvent(new DragEvent('dragenter', { bubbles: true, cancelable: true, dataTransfer: transfer }))
	})
	await expect(page.getByTestId('global-drop-overlay')).toContainText('添加到编辑消息')
	await page.evaluate(() => {
	  const transfer = (window as unknown as { __dropTransfer?: DataTransfer }).__dropTransfer!
	  document.body.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }))
	})
	await expect(page.getByTestId('global-drop-overlay')).toHaveCount(0)
	await expect(page.locator('.attachment-draft-file[title="global-drop.go"]')).toBeVisible()
  await page.getByTestId('edit-send').click()
  const editedBubble = page.getByTestId('user-bubble').filter({ hasText: edited })
  const originalBubble = page.getByTestId('user-bubble').filter({ hasText: original })
  await expect(editedBubble).toContainText(edited)
  await expect(editedBubble.locator('.message-image img')).toBeVisible()
  await expect(editedBubble.locator('.message-images')).toBeVisible()
  await expect(editedBubble.locator('.user-text-bubble')).toHaveText(edited)
  await editedBubble.getByRole('button', { name: '放大查看图片' }).click()
  await expect(page.getByRole('dialog', { name: '图片预览' })).toBeVisible()
  await page.getByRole('button', { name: '关闭图片预览' }).click()
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.locator('.branch-nav')).toContainText('2 / 2')
  await expectMinTarget(page.locator('.branch-nav button').first(), 'previous branch')
  await expectMinTarget(page.locator('.branch-nav button').last(), 'next branch')

  await page.locator('.branch-nav button').first().click()
  await expect(originalBubble).toBeVisible()
  await expect(originalBubble).toContainText(original)
  await page.locator('.branch-nav button').last().click()
  await expect(editedBubble).toBeVisible()
  await expect(editedBubble).toContainText(edited)

  const forkResponse = page.waitForResponse(response => response.url().endsWith(`/sessions/${before.id}/fork`) && response.request().method() === 'POST')
  await page.getByTestId('fork-msg').click()
  const child = await (await forkResponse).json() as { id: string }
  await expect.poll(async () => page.evaluate(async () => {
    return (await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as unknown[]).length
  })).toBe(before.count + 1)
  const readFocusedSession = await focusedSessionReader(page)
  await expect.poll(() => readFocusedSession()).toBe(child.id)
  await expect(editedBubble).toContainText(edited)

  await page.getByTestId('regen-msg').click()
  await expect(page.locator('.branch-nav')).toContainText('2 / 2')
  await expect.poll(async () => page.evaluate(async () => {
    return (await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as unknown[]).length
  })).toBe(before.count + 1)
})

test('fork works while running and regenerate reports the busy block', async ({ page, request }) => {
  await page.goto('/')
  await sendPrompt(page, 'fork-regen first')
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const count = () => page.evaluate(async () => (
    await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as unknown[]
  ).length)

  await sendPrompt(page, 'e2e-hold')
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  const parentId = await page.evaluate(async () => {
    const list = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ id: string; running?: boolean }>
    return list.find(s => s.running)?.id ?? ''
  })
  expect(parentId).not.toBe('')
  const before = await count()

  try {
    const regen = page.getByTestId('regen-msg').first()
    await expect(regen).toHaveAttribute('data-disabled', 'true')
    await regen.click()
    await expect(page.getByTestId('toaster')).toContainText('当前对话正在运行')

    await page.getByTestId('fork-msg').first().click()
    await expect.poll(count).toBe(before + 1)
  } finally {
    await request.post(`/v1/sessions/${parentId}/abort`, { headers })
  }
})

test('new session keeps tab choices while reload uses the server default', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  const chip = page.getByTestId('open-model')
  await expect(chip).not.toHaveText('选择模型')
  await expect(page.getByTestId('thinking-select')).toHaveText('medium')
  await chip.click()
  await page.getByTestId('model-option').and(page.locator('[data-spec="openai/gpt-5.6-terra"]')).click()
  await expect(chip).toHaveText('gpt-5.6-terra')
  const thinking = page.getByTestId('thinking-select')
  await expect(thinking).toHaveText('medium')
  await thinking.click()
  const thinkingMenu = page.getByRole('listbox', { name: 'Thinking effort' })
  await expect(thinkingMenu).toBeVisible()
  const thinkingScroll = await thinkingMenu.evaluate(element => {
    const menu = element as HTMLElement
    menu.style.maxHeight = '80px'
    menu.scrollTop = menu.scrollHeight
    return { scrollTop: menu.scrollTop, scrollable: menu.scrollHeight > menu.clientHeight }
  })
  expect(thinkingScroll.scrollable).toBeTruthy()
  expect(thinkingScroll.scrollTop).toBeGreaterThan(0)
  await expect(thinkingMenu).toBeVisible()
  await page.getByRole('option', { name: 'high', exact: true }).click()
  await expect(thinking).toHaveText('high')

  await newSession(page)
  await expect(chip).toHaveText('gpt-5.6-terra')
  await expect(thinking).toHaveText('high')
  const created = await page.evaluate(async () => {
    const sessions = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ id: string }>
    return fetch(`/v1/sessions/${sessions[0].id}`, { credentials: 'same-origin' }).then(r => r.json()) as Promise<{ provider: string; model: string; thinkingEffort?: string }>
  })
  expect(created.provider).toBe('openai')
  expect(created.model).toBe('gpt-5.6-terra')
  expect(created.thinkingEffort).toBe('high')

  await page.reload()
  await expect(page.getByTestId('open-model')).toHaveText('gpt-5.6-terra')
  await expect(page.getByTestId('thinking-select')).toHaveText('medium')
  await openSessionRow(page)
  await expect(page.getByTestId('thinking-select')).toHaveText('high')
})

test('workspace tree, pin, search, directory picker, to-bottom', async ({ page }) => {
  const { cwd } = JSON.parse(readFileSync(statePath, 'utf8')) as { cwd: string }
  expect(cwd).toBeTruthy()
  await page.goto('/')
  await sendPrompt(page, `ws-e2e ${Date.now()}`)
  await expect(page.getByTestId('workspace-row').first()).toBeVisible()
  await expect(page.getByTestId('session-row').first()).toBeVisible()

  await expect(page.locator('.ws-toolbar')).toBeVisible()
  await expect(page.locator('.ws-toolbar')).toContainText('工作区')
  await expect(page.getByTestId('add-workspace')).toBeVisible()
  await page.getByRole('button', { name: '搜索会话' }).click()
  await expect(page.getByTestId('session-search')).toBeVisible()
  await expect(page.locator('.header-actions')).toHaveClass(/hidden/)
  await page.getByRole('button', { name: '清除搜索' }).click()
  await expect(page.locator('.header-actions')).not.toHaveClass(/hidden/)
  await page.getByTestId('add-workspace').click()
  await expect(page.getByTestId('dir-browser')).toBeVisible()
  await expect(page.getByRole('navigation').getByRole('button', { name: '主目录' })).toBeVisible()
  await expect(page.locator('.dir-col')).toHaveCount(1)
  await expect(page.locator('.dir-row.on')).toHaveCount(0)
  await page.getByTestId('dir-new-folder').click()
  await expect(page.getByTestId('dir-create')).toBeVisible()
  await page.getByTestId('dir-create').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-create')).toHaveCount(0)
  await page.getByTestId('dir-path').click()
  const pathIn = page.getByLabel('编辑路径')
  await expect(pathIn).toBeVisible()
  await pathIn.fill(cwd)
  await expect(page.getByTestId('dir-row').first()).toBeVisible({ timeout: 5000 })
  await page.getByTestId('dir-browser-mask').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-browser')).toHaveCount(0)

  await page.getByTestId('session-row').first().locator('button[aria-label="会话菜单"]').click()
  await page.getByRole('menuitem', { name: '置顶' }).click()
  await expect(page.locator('.pin-mark')).toBeVisible()

  await page.getByRole('button', { name: '搜索会话' }).click()
  await page.getByTestId('session-search').fill('ws-e2e')
  await expect(page.getByTestId('search-hit').first()).toBeVisible()

  const tall = 'line\n'.repeat(80)
  await page.getByTestId('composer-input').fill(tall)
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('user-bubble').nth(1)).toBeVisible()
  await expectMinTarget(page.getByTestId('user-bubble-toggle'), 'long-message toggle')
  await page.getByTestId('user-bubble-toggle').click()
  await expect(page.getByTestId('user-bubble-toggle')).toHaveAttribute('aria-label', '收起')
  const scroll = page.getByTestId('chat-scroll')
  await scroll.evaluate(el => { el.scrollTop = el.scrollHeight })
  await scroll.evaluate(el => { el.scrollTop = 0 })
  await expect(page.getByTestId('to-bottom')).toBeVisible()
  await page.getByTestId('to-bottom').click()
  await expect(page.getByTestId('to-bottom')).toHaveCount(0)

  await page.getByRole('button', { name: '清除搜索' }).click()
  const rows = page.getByTestId('session-row')
  const before = await rows.count()
  await page.getByTestId('ws-new-session').first().click()
  await expect(rows).toHaveCount(before + 1)
  await page.reload()
  await expandWorkspaces(page)
  await expect(rows).toHaveCount(before + 1)
  await expect(rows.first().locator('.pin-mark')).toBeVisible()
})

test('session overflow menu anchors to the clicked row', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `menu-anchor ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.getByTestId('session-row').first()).toBeVisible()
  const plus = page.getByTestId('ws-new-session').first()
  for (let i = 0; i < 4; i++) await plus.click()
  const trigger = page.getByTestId('session-row').last().locator('button[aria-label="会话菜单"]')
  await trigger.scrollIntoViewIfNeeded()
  await trigger.click()
  const menu = page.getByTestId('pop-menu')
  await expect(menu).toBeVisible()
  await expect(menu).toHaveAttribute('role', 'menu')
  await expect(trigger).toHaveAttribute('aria-haspopup', 'menu')
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
  await expect(trigger).toHaveAttribute('aria-controls', await menu.getAttribute('id') as string)
  const items = menu.getByRole('menuitem')
  await expect(items.first()).toBeFocused()
  const btnBox = await trigger.boundingBox()
  const menuBox = await menu.boundingBox()
  expect(btnBox).toBeTruthy()
  expect(menuBox).toBeTruthy()
  expect(Math.abs(menuBox!.x - btnBox!.x)).toBeLessThan(16)
  const below = menuBox!.y >= btnBox!.y + btnBox!.height - 4
  const above = menuBox!.y + menuBox!.height <= btnBox!.y + 4
  expect(below || above).toBe(true)

  await page.keyboard.press('ArrowDown')
  await expect(items.nth(1)).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(trigger).toBeFocused()
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')

  await trigger.click()
  await page.getByRole('menuitem', { name: '重命名' }).click()
  const rename = page.getByTestId('rename-dialog')
  await expect(rename).toBeVisible()
  await expect(page.getByTestId('rename-input')).toBeFocused()
  await expect(page.getByTestId('rename-input')).toHaveAccessibleName('重命名')
  await rename.getByRole('button', { name: '关闭对话框' }).click()
  await expect(rename).toHaveCount(0)
  await expect(trigger).toBeFocused()
})

test('session info lists skills and extensions; toggles live in settings', async ({ page }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const skillDir = join(home, 'skills', 'demo-skill')
  mkdirSync(skillDir, { recursive: true })
  writeFileSync(join(skillDir, 'SKILL.md'), '---\nname: demo-skill\ndescription: e2e skill\n---\n')

  await page.goto('/')
  await sendPrompt(page, `cfg-e2e ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')

  await page.getByTestId('tab-config').click()
  await expect(page.getByTestId('session-info')).toBeVisible()
  await expect(page.getByTestId('cfg-skill').filter({ hasText: 'demo-skill' })).toBeVisible()
  await expect(page.getByTestId('info-outline')).toContainText('demo-skill')
  await expect(page.getByTestId('info-outline')).toContainText('系统提示词')
  await expect(page.getByTestId('info-reload')).toBeVisible()

  await expect(page.getByTestId('cfg-system-prompt-text')).toContainText('You are a helpful assistant operating inside ki')
  await expect(page.getByTestId('cfg-system-prompt-copy')).toBeVisible()

  await page.getByTestId('info-edit').click()
  await expect(page.getByTestId('settings-tab-extensions')).toBeVisible()
})

test('session info lists extension-loaded skills, commands, and prompt with heading sizes', async ({ page, request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const dir = join(home, 'extensions', 'infox')
  mkdirSync(join(dir, 'skills', 'ext-skill'), { recursive: true })
  mkdirSync(join(dir, 'commands'), { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name: 'infox',
    version: '3.0.0',
    description: 'info page fixture',
    capabilities: ['skill', 'command', 'prompt.append', 'path'],
    skills: ['skills'],
    commands: ['commands'],
    prompt: { append: ['APPEND.md'] },
    runtime: { kind: 'none', path: ['bin', 'missing-bin'] },
  }))
  writeFileSync(join(dir, 'skills', 'ext-skill', 'SKILL.md'), '---\nname: ext-skill\ndescription: skill from infox\n---\n')
  writeFileSync(join(dir, 'commands', 'exthello.md'), '---\ndescription: hello from infox\n---\nHi\n')
  writeFileSync(join(dir, 'APPEND.md'), 'EXT-PROMPT-LAYER\n')
  mkdirSync(join(dir, 'bin'), { recursive: true })
  const realDir = realpathSync(dir)

  const headers = { Authorization: `Bearer ${serverToken()}` }
  const reload = await request.post('/v1/reload', { headers })
  expect(reload.ok()).toBe(true)

  await page.goto('/')
  await sendPrompt(page, `info-ext ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await page.getByTestId('tab-config').click()

  const card = page.getByTestId('cfg-extension').filter({ has: page.locator('.cfg-h2', { hasText: 'infox' }) })
  await expect(card).toBeVisible()
  await expect(card.getByTestId('cfg-extension-skill')).toHaveAttribute('data-name', 'ext-skill')
  await expect(card.getByTestId('cfg-extension-command')).toContainText('/exthello')
  await expect(card.getByTestId('cfg-extension-prompt')).toHaveAttribute('data-name', 'APPEND.md')
  await expect(card.getByTestId('cfg-extension-capabilities')).toContainText('skill')
  await expect(card.getByTestId('cfg-extension-path').first()).toHaveAttribute('data-exists', 'true')
  await expect(card.getByTestId('cfg-extension-path').first()).toContainText(join(realDir, 'bin'))
  await expect(card.getByTestId('cfg-extension-path').nth(1)).toHaveAttribute('data-exists', 'false')
  await expect(card.getByTestId('cfg-extension-path').nth(1)).toContainText('missing-bin')
  await expect(page.getByTestId('info-outline')).toContainText('ext-skill')
  await expect(page.getByTestId('info-outline')).toContainText(join(realDir, 'missing-bin'))
  await expect(page.getByTestId('cfg-skill').filter({ hasText: 'ext-skill' })).toBeVisible()

  const sizes = await page.evaluate(() => {
    const section = document.querySelector('#info-extensions.cfg-block .cfg-h, #info-extensions .cfg-h')
    const ext = document.querySelector('#info-extensions .cfg-h2')
    const group = document.querySelector('#info-extensions .cfg-h3')
    const body = document.querySelector('#info-extensions .cfg-desc')
    const sizeOf = (el: Element | null) => el ? Number.parseFloat(getComputedStyle(el).fontSize) : 0
    return { section: sizeOf(section), ext: sizeOf(ext), group: sizeOf(group), body: sizeOf(body) }
  })
  expect(sizes.section, `heading sizes ${JSON.stringify(sizes)}`).toBeGreaterThan(sizes.ext)
  expect(sizes.ext, `heading sizes ${JSON.stringify(sizes)}`).toBeGreaterThan(sizes.group)
  expect(sizes.group, `heading sizes ${JSON.stringify(sizes)}`).toBeGreaterThanOrEqual(sizes.body - 0.5)
})

test('command palette is opaque, one-line, and inserts without sending', async ({ page }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const skillDir = join(home, 'skills', 'long-skill')
  const promptDir = join(home, 'prompts')
  mkdirSync(skillDir, { recursive: true })
  mkdirSync(promptDir, { recursive: true })
  writeFileSync(join(skillDir, 'SKILL.md'), '---\nname: long-skill\ndescription: "Create, read, edit, or manipulate Word documents (docx files) or Word templates (dotx files). Triggers include any mention of Word, docx, or word document."\n---\nbody\n')
  writeFileSync(join(promptDir, 'ro.md'), '---\ndescription: rewrite the supplied text\n---\n$@\n')

  await page.goto('/')
  await sendPrompt(page, `palette-e2e ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  const bubbles = page.getByTestId('user-bubble')
  const before = await bubbles.count()
  const requests = trackComposerRequests(page)
  const composerInput = page.getByTestId('composer-input')
  await composerInput.fill('输入法候选词')
  await pressComposingEnter(composerInput)
  await expect(composerInput).toHaveValue('输入法候选词')
  await expect(bubbles).toHaveCount(before)
  expect(requests).toEqual({ prompt: 0, abort: 0 })
  await composerInput.fill('')

  await page.getByTestId('command-btn').click()
  const palette = page.getByTestId('command-palette')
  await expect(palette).toBeVisible()
  const paletteID = await palette.getAttribute('id')
  expect(paletteID).toBeTruthy()
  await expect(palette).toHaveAccessibleName('命令')
  await expect(composerInput).toHaveAttribute('role', 'combobox')
  await expect(composerInput).toHaveAttribute('aria-expanded', 'true')
  await expect(composerInput).toHaveAttribute('aria-controls', paletteID!)
  await expect(composerInput).toHaveAttribute('aria-activedescendant', /.+/)
  const activeOptionID = await composerInput.getAttribute('aria-activedescendant')
  expect(activeOptionID).toBeTruthy()
  await expect(page.locator(`[id="${activeOptionID}"]`)).toHaveAttribute('role', 'option')
  const palBox = await palette.boundingBox()
  const cardBox = await page.getByTestId('composer-card').boundingBox()
  expect(palBox).toBeTruthy()
  expect(cardBox).toBeTruthy()
  expect(palBox!.y + palBox!.height).toBeLessThanOrEqual(cardBox!.y + 2)
  const bg = await palette.evaluate(el => getComputedStyle(el).backgroundColor)
  const rgba = bg.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/)
  expect(rgba, `palette background ${bg}`).toBeTruthy()
  expect(Number(rgba![4] ?? 1)).toBeGreaterThan(0.9)

  const longItem = page.getByTestId('command-item-skill:long-skill')
  await expect(longItem).toBeVisible()
  const metrics = await longItem.locator('.command-desc').evaluate(el => {
    const style = getComputedStyle(el)
    return { whiteSpace: style.whiteSpace, height: el.getBoundingClientRect().height, lineHeight: parseFloat(style.lineHeight) }
  })
  expect(metrics.whiteSpace).toBe('nowrap')
  expect(metrics.height).toBeLessThanOrEqual(metrics.lineHeight + 2)

  await page.getByTestId('command-item-reload').click()
  await expect(palette).toHaveCount(0)
  await expect(page.getByTestId('composer-input')).toHaveValue('/reload')
  await expect(bubbles).toHaveCount(before)

  const body = '保留这段输入内容'
  await composerInput.fill(body)
  await page.getByTestId('command-btn').click()
  await expect(composerInput).toHaveValue(body)
  await page.getByTestId('command-item-ro').click()
  await expect(composerInput).toHaveValue(`/ro ${body}`)
  await expect(bubbles).toHaveCount(before)

  await composerInput.fill('/ro')
  await expect(page.getByTestId('command-palette')).toBeVisible()
  const activeBeforeComposition = await composerInput.getAttribute('aria-activedescendant')
  expect(activeBeforeComposition).toBeTruthy()
  await pressComposingEnter(composerInput)
  await expect(palette).toBeVisible()
  await expect(composerInput).toHaveAttribute('aria-expanded', 'true')
  await expect(composerInput).toHaveAttribute('aria-activedescendant', activeBeforeComposition!)
  await expect(composerInput).toHaveValue('/ro')
  await composerInput.press('Enter')
  await expect(page.getByTestId('command-palette')).toHaveCount(0)
  await expect(composerInput).toHaveValue('/ro')
  await expect(bubbles).toHaveCount(before)

  await composerInput.fill('/rel')
  await expect(page.getByTestId('command-item-reload')).toBeVisible()
  await expect(palette.locator('[role="option"]')).toHaveCount(1)
  await composerInput.press('Tab')
  await expect(composerInput).toHaveValue('/reload')
  await expect(page.getByTestId('command-palette')).toHaveCount(0)
  await expect(bubbles).toHaveCount(before)

  expect(requests).toEqual({ prompt: 0, abort: 0 })
  await composerInput.fill('/reload')
  await composerInput.press('Enter')
  await composerInput.press('Enter')
  const toast = page.getByTestId('toast')
  await expect(toast).toContainText('reloaded')
  await expect(toast).toHaveAttribute('data-kind', 'info')
  const box = await page.getByTestId('toaster').boundingBox()
  const view = page.viewportSize()
  expect(box).toBeTruthy()
  expect(view).toBeTruthy()
  expect(box!.y).toBeLessThan(48)
  expect(box!.x + box!.width).toBeGreaterThan((view!.width) - 400)
  await expect(bubbles).toHaveCount(before)
  expect(requests).toEqual({ prompt: 1, abort: 0 })
})

test('slash command Tab completion drives a live compaction row', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `compact-e2e ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')

  const input = page.getByTestId('composer-input')
  await input.fill('/com')
  await expect(page.getByTestId('command-palette')).toBeVisible()
  await input.press('Tab')
  await expect(input).toHaveValue('/compact')
  await expect(page.getByTestId('command-palette')).toHaveCount(0)

  await input.press('Enter')
  const row = page.getByTestId('compact-row')
  await expect(row).toBeVisible()
  await expect(page.getByTestId('compact-btn')).toHaveClass(/empty/)
  await expect(page.getByTestId('compact-btn')).toContainText('无需压缩')
  await page.reload()
  await openSessionRow(page)
  await expect(row).toHaveCount(1)
  await expect(page.getByTestId('compact-btn')).toHaveClass(/empty/)
  await expect(page.getByTestId('compact-btn')).toContainText('无需压缩')
})

test('composer clears before a slow slash command returns', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `compact-clear ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')

  const input = page.getByTestId('composer-input')
  let parked: Route | undefined
  await page.route('**/v1/sessions/*/prompt', route => { parked = route })
  await input.fill('/compact')
  await input.press('Tab')
  await expect(input).toHaveValue('/compact')
  await input.press('Enter')
  await expect(input).toHaveValue('')
  await expect.poll(() => !!parked).toBe(true)
  await parked!.continue()
  await expect(page.getByTestId('compact-row')).toBeVisible()
})

test('info reload shows progress then completion', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `reload-ui ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await page.getByTestId('tab-config').click()
  const btn = page.getByTestId('info-reload')
  await expect(btn).toBeVisible()

  await page.route('**/v1/reload', async route => {
    await new Promise(resolve => setTimeout(resolve, 500))
    await route.continue()
  })
  await btn.click()
  await expect(page.getByTestId('reload-spin')).toBeVisible()
  await expect(btn).toContainText('正在重新加载')
  await expect(btn).toContainText('已重新加载')
})

test('info reload failure shows a toast on the info page', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, `reload-err ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await page.getByTestId('tab-config').click()
  await expect(page.getByTestId('session-info')).toBeVisible()
  await page.route('**/v1/reload', route => route.fulfill({ status: 500, body: 'reload failed' }))
  await page.getByTestId('info-reload').click()
  const toast = page.getByTestId('toast')
  await expect(toast).toHaveAttribute('data-kind', 'error')
  await expect(toast).toContainText('reload failed')
  await expect(page.getByTestId('session-info')).toBeVisible()
})

async function sessionQueue(page: Page): Promise<{ running: boolean; texts: string[] }> {
  return page.evaluate(async () => {
    const list = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ id: string; running?: boolean; title?: string }>
    const s = list.find(x => x.running) || list.find(x => (x.title ?? '').includes('e2e-hold')) || list[0]
    const d = await fetch(`/v1/sessions/${s.id}`, { credentials: 'same-origin' }).then(r => r.json()) as { running?: boolean; queued?: Array<{ content?: Array<{ text?: string }> }> }
    return {
      running: !!d.running,
      texts: (d.queued ?? []).map(q => (q.content ?? []).map(c => c.text ?? '').join(' ')),
    }
  })
}

test('busy enter queues and ctrl+enter promotes the tail', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('settings-tab-message').click()
  await page.getByTestId('busy-queue').click()
  await expect(page.getByTestId('busy-queue')).toHaveAttribute('aria-checked', 'true')
  await page.getByTestId('settings-mask').click({ position: { x: 4, y: 4 } })
  await newSession(page)
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill('e2e-hold')
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await expect.poll(async () => (await sessionQueue(page)).running).toBe(true)
  const requests = trackComposerRequests(page)
  try {
    await expect(input).toHaveValue('')
    await pressComposingEnter(input)
    await expect(page.getByTestId('composer-stop')).toBeVisible()
    await expect.poll(async () => (await sessionQueue(page)).running).toBe(true)
    expect(requests).toEqual({ prompt: 0, abort: 0 })
    await input.fill('queued-keep')
    await pressComposingEnter(input)
    await expect(input).toHaveValue('queued-keep')
    await expect.poll(async () => (await sessionQueue(page)).texts).toEqual([])
    expect(requests).toEqual({ prompt: 0, abort: 0 })
    await input.press('Enter')
    await expect(input).toHaveValue('')
    await expect.poll(async () => (await sessionQueue(page)).texts).toEqual(['queued-keep'])
    await expect(page.getByTestId('queued-list')).toContainText('queued-keep')
    await input.fill('queued-promote')
    await input.press('Enter')
    await expect(input).toHaveValue('')
    await expect.poll(async () => (await sessionQueue(page)).texts).toEqual(['queued-keep', 'queued-promote'])
    await expect(page.getByTestId('queued-item')).toHaveCount(2)
    await expect(page.getByTestId('queued-steer')).toHaveCount(2)
    await expectMinTarget(page.getByTestId('queued-steer').first(), 'queued steer')
    await expectMinTarget(page.getByTestId('queued-remove').first(), 'queued remove')
    await expect(page.getByTestId('queued-item').last()).toContainText('Ctrl+Enter')
    await input.press('Control+Enter')
    await expect.poll(async () => (await sessionQueue(page)).texts).toEqual(['queued-keep'])
    await expect(page.getByTestId('queued-item')).toHaveCount(1)
    await expect(page.getByTestId('queued-list')).not.toContainText('queued-promote')
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-promote' }).first()).toBeVisible()
    expect(requests).toEqual({ prompt: 3, abort: 0 })
  } finally {
    const stop = page.getByTestId('composer-stop')
    if (await stop.count()) await stop.click()
  }
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect.poll(async () => (await sessionQueue(page)).texts).toEqual([])
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-keep' })).toBeVisible()
  await page.reload()
  await openSessionRow(page)
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-promote' })).toBeVisible()
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-keep' })).toBeVisible()
})
