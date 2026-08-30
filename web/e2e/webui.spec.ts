import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { applyFollowTail } from '../src/follow-tail.ts'
import { nodeTypes, nodeValues, parseMarkdown } from './markdown-parse.ts'
import { statePath } from './global-setup.ts'

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

test.describe.configure({ mode: 'serial' })

test('settings navigation and controls are consistent', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await expect(page.getByRole('heading', { name: '开始对话' })).toBeVisible()
  await expect(page.getByTestId('composer-input')).toBeVisible()
  await page.getByTestId('open-settings').click()
  await expect(page.getByTestId('settings-tab-providers')).toHaveText('模型供应商')
  await expect(page.getByTestId('settings-tab-skills')).toHaveText('Skills')
  await expect(page.getByTestId('settings-tab-extensions')).toHaveText('Extensions')
  await expect(page.getByTestId('settings-tab-message')).toHaveText('Message')
  await expect(page.getByTestId('settings-tab-appearance')).toHaveText('主题和语言')
  await expect(page.getByTestId('provider-settings')).toContainText('Anthropic')
  await expect(page.locator('.provider-nav [data-provider-id="anthropic"]')).toHaveText('Anthropic')
  await expect(page.locator('.provider-nav')).not.toContainText('缺少密钥')
  await expect(page.locator('.provider-nav')).not.toContainText('API key needed')
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
  await page.getByTestId('settings-tab-extensions').click()
  await expect(page.getByTestId('extensions-settings')).toBeVisible()
  await page.getByTestId('settings-tab-message').click()
  await expect(page.getByTestId('message-settings')).toBeVisible()
  await expect(page.getByTestId('busy-steer')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('busy-steer')).toHaveText('Steer')
  await expect(page.getByTestId('busy-queue')).toHaveText('Queue')
  await page.getByTestId('busy-queue').click()
  await expect(page.getByTestId('busy-queue')).toHaveAttribute('aria-checked', 'true')
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

test('provider settings supports a complete add and edit flow', async ({ page }) => {
  const providerID = `pw-provider-${Date.now()}`
  const modelID = `pw-model-${Date.now()}`
  await page.goto('/')
  await page.getByTestId('open-settings').click()
  await page.getByTestId('add-provider').click()

  await expect(page.getByTestId('new-provider-dialog')).toBeVisible()
  await expect(page.getByTestId('new-provider-form').getByLabel('供应商 ID')).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('new-provider-dialog')).toHaveCount(0)
  await expect(page.getByTestId('settings')).toBeVisible()
  await page.getByTestId('add-provider').click()

  const create = page.getByTestId('new-provider-form')
  await create.getByLabel('供应商 ID').fill(providerID)
  await create.getByLabel('显示名称').fill('Playwright Provider')
  await create.getByLabel('Base URL').fill('https://example.test/v1')
  await create.getByRole('combobox', { name: 'API 协议' }).click()
  await page.getByRole('option', { name: 'Responses' }).click()
  await create.getByLabel('首个模型 ID').fill('starter-model')
  await create.getByRole('button', { name: '创建供应商' }).click()

  await expect(page.getByTestId('new-provider-dialog')).toHaveCount(0)
  await expect(page.locator(`.provider-nav [data-provider-id="${providerID}"]`)).toHaveText('Playwright Provider')
  const connection = page.getByTestId('provider-connection-form')
  await expect(connection.getByLabel('供应商 ID')).toHaveValue(providerID)
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

  // CommonMark drops emphasis next to CJK punctuation; @streamdown/cjk keeps it.
  const cjk = parseMarkdown('这是**强调。**结尾')
  expect(nodeTypes(cjk)).toContain('strong')
  expect(nodeValues(cjk, 'text')).toEqual(['这是', '强调。', '结尾'])

  const streaming = parseMarkdown('before **bold', true)
  expect(nodeTypes(streaming)).toContain('strong')
  expect(nodeValues(streaming, 'text')).toEqual(['before ', 'bold'])

  const leftover = parseMarkdown('before **bold', false)
  expect(nodeTypes(leftover)).not.toContain('strong')
  expect(nodeValues(leftover, 'text')).toEqual(['before **bold'])
})

test('chat and trajectory talk to the fake runtime', async ({ page }) => {
  const prompt = `hello from playwright ${Date.now()}`
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(prompt)
  await input.press('Enter')
  await expect(page.locator('.session-row.active .dot.on')).toBeVisible()

  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message').locator('.md')).toContainText('ok')
  await expect(page.getByTestId('session-stats')).toContainText('1 轮 · 1 步')
  await expect(page.getByTestId('session-stats')).toContainText('输入 8 · 输出 2')
  await expect(page.getByTestId('session-title').filter({ hasText: prompt })).toBeVisible()
  const asstActions = page.getByTestId('assistant-message').getByTestId('asst-actions')
  await expect(asstActions.getByTestId('copy-msg')).toBeVisible()
  await expect(asstActions.getByTestId('fork-msg')).toBeVisible()
  await expect(asstActions.getByTestId('regen-msg')).toBeVisible()
  const userActions = page.getByTestId('user-actions')
  await expect(userActions.getByTestId('copy-msg')).toBeVisible()
  await expect(userActions.getByTestId('edit-msg')).toBeVisible()

  const listed = await page.evaluate(async () => {
    const res = await fetch('/v1/sessions', { credentials: 'same-origin' })
    if (!res.ok) throw new Error(`list ${res.status}`)
    return res.json() as Promise<Array<{ title?: string }>>
  })
  expect(listed.some(s => (s.title ?? '').includes(prompt))).toBeTruthy()

  await page.getByTestId('tab-trajectory').click()
  await expect(page.getByTestId('trajectory')).toBeVisible()
  await expect(page.locator('[data-testid="traj-row"][data-kind="user"]')).toContainText(prompt)
  await expect(page.locator('[data-testid="traj-row"][data-kind="assistant"]')).toContainText('ok')
  await page.locator('[data-testid="traj-row"][data-kind="assistant"]').first().click()
  await expect(page.getByTestId('insp-loc')).toContainText('Turn')
  await expect(page.getByTestId('insp-loc')).toContainText('Step')
  await expect(page.getByTestId('insp-tab-summary')).toBeVisible()
  await expect(page.getByTestId('insp-tab-preview')).toBeVisible()
  await expect(page.getByTestId('insp-tab-raw')).toBeVisible()
  await page.locator('[data-testid="traj-row"][data-kind="system"]').first().click()
  await expect(page.getByTestId('system-prompt')).not.toHaveText('—')
  await page.getByTestId('insp-tab-tools').click()
  await expect(page.getByTestId('system-tools')).toContainText('Read')
  const readTool = page.getByTestId('system-tools').locator('.tool-cat-item').filter({ has: page.locator('.tool-cat-name', { hasText: /^Read$/ }) })
  await readTool.locator('summary').click()
  await expect(readTool.getByTestId('copy-tool-desc')).toBeVisible()
  await expect(readTool.locator('.tool-cat-desc')).toContainText('Read a file')
  await page.getByTestId('insp-tab-context').click()
  await expect(page.getByTestId('system-diff')).toBeVisible()

  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('traj-follow').click()
  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'false')
  await page.getByTestId('traj-follow').click()
  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'true')
  const atTail = await page.getByTestId('traj-table-wrap').evaluate(el =>
    Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop) < 2)
  expect(atTail).toBeTruthy()
  await page.getByTestId('traj-table-wrap').evaluate(el => {
    const node = el as HTMLElement
    node.style.maxHeight = '48px'
    node.scrollTop = node.scrollHeight
  })
  await expect.poll(async () => page.getByTestId('traj-table-wrap').evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(40)
  await page.getByTestId('traj-table-wrap').evaluate(el => {
    el.scrollTop = 0
    el.dispatchEvent(new Event('scroll'))
  })
  await expect(page.getByTestId('traj-follow')).toHaveAttribute('aria-pressed', 'false')

  await page.reload()
  await page.getByTestId('session-row').first().click()
  await expect(page.getByTestId('user-bubble')).toHaveText(prompt)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
})

test('edit branches in place with attachments and fork opens a new session', async ({ page }) => {
  const original = `branch-original-${Date.now()}`
  const edited = `branch-edited-${Date.now()}`
  await page.goto('/')
  await sendPrompt(page, original)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')

  const before = await page.evaluate(async () => {
    const sessions = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as Array<{ id: string; cwd: string; title?: string }>
    return { count: sessions.length, cwd: sessions.find(s => (s.title ?? '').includes('branch-original'))!.cwd }
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
  await expect(page.getByTestId('user-bubble')).toContainText(edited)
  await expect(page.getByTestId('user-bubble').locator('.message-image img')).toBeVisible()
  await expect(page.getByTestId('user-bubble').locator('.message-images')).toBeVisible()
  await expect(page.getByTestId('user-bubble').locator('.user-text-bubble')).toHaveText(edited)
  await page.getByTestId('user-bubble').getByRole('button', { name: '放大查看图片' }).click()
  await expect(page.getByRole('dialog', { name: '图片预览' })).toBeVisible()
  await page.getByRole('button', { name: '关闭图片预览' }).click()
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  await expect(page.locator('.branch-nav')).toContainText('2 / 2')

  await page.locator('.branch-nav button').first().click()
  await expect(page.getByTestId('user-bubble')).toContainText(original)
  await page.locator('.branch-nav button').last().click()
  await expect(page.getByTestId('user-bubble')).toContainText(edited)

  await page.getByTestId('fork-msg').click()
  await expect.poll(async () => page.evaluate(async () => {
    return (await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as unknown[]).length
  })).toBe(before.count + 1)
  await expect(page.getByTestId('user-bubble')).toContainText(edited)

  await page.getByTestId('regen-msg').click()
  await expect(page.locator('.branch-nav')).toContainText('2 / 2')
  await expect.poll(async () => page.evaluate(async () => {
    return (await fetch('/v1/sessions', { credentials: 'same-origin' }).then(r => r.json()) as unknown[]).length
  })).toBe(before.count + 1)
})

test('new session keeps the current model and thinking effort', async ({ page }) => {
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

  await page.getByTestId('new-session').click()
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
  await expect(page.getByTestId('thinking-select')).toHaveText('high')
})

test('workspace tree, pin, search, directory picker, to-bottom', async ({ page }) => {
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
  await expect(page.getByTestId('dir-browser')).not.toContainText('/home/')
  await page.getByTestId('dir-new-folder').click()
  await expect(page.getByTestId('dir-create')).toBeVisible()
  await page.getByTestId('dir-create').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-create')).toHaveCount(0)
  await page.getByTestId('dir-path').click()
  const pathIn = page.getByLabel('编辑路径')
  await expect(pathIn).toBeVisible()
  await pathIn.fill('/tmp')
  await expect(page.getByTestId('dir-row').first()).toBeVisible({ timeout: 5000 })
  await page.getByTestId('dir-browser-mask').getByRole('button', { name: '取消' }).click()
  await expect(page.getByTestId('dir-browser')).toHaveCount(0)

  await page.getByTestId('session-row').first().locator('button[aria-label="会话菜单"]').click()
  await page.getByRole('button', { name: '置顶' }).click()
  await expect(page.locator('.pin-mark')).toBeVisible()

  await page.getByRole('button', { name: '搜索会话' }).click()
  await page.getByTestId('session-search').fill('ws-e2e')
  await expect(page.getByTestId('search-hit').first()).toBeVisible()

  const tall = 'line\n'.repeat(80)
  await page.getByTestId('composer-input').fill(tall)
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('user-bubble').nth(1)).toBeVisible()
  // Long user bubbles are clamped to 6 lines; expand so the chat overflows.
  await page.getByTestId('user-bubble-toggle').click()
  await expect(page.getByTestId('user-bubble-toggle')).toHaveAttribute('aria-label', '收起')
  const scroll = page.getByTestId('chat-scroll')
  // Expanding grows scrollHeight without firing a scroll event, so jump to the
  // bottom first, then up, to make the container report a real scroll position.
  await scroll.evaluate(el => { el.scrollTop = el.scrollHeight })
  await scroll.evaluate(el => { el.scrollTop = 0 })
  await expect(page.getByTestId('to-bottom')).toBeVisible()
  await page.getByTestId('to-bottom').click()
  await expect(page.getByTestId('to-bottom')).toHaveCount(0)
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
  const btnBox = await trigger.boundingBox()
  const menuBox = await menu.boundingBox()
  expect(btnBox).toBeTruthy()
  expect(menuBox).toBeTruthy()
  expect(Math.abs(menuBox!.x - btnBox!.x)).toBeLessThan(16)
  const below = menuBox!.y >= btnBox!.y + btnBox!.height - 4
  const above = menuBox!.y + menuBox!.height <= btnBox!.y + 4
  expect(below || above).toBe(true)
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
  await expect(page.getByTestId('info-reload')).toBeVisible()

  await page.getByTestId('info-edit').click()
  await expect(page.getByTestId('settings-tab-extensions')).toBeVisible()
})

test('command palette is opaque, one-line, and inserts without sending', async ({ page }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const skillDir = join(home, 'skills', 'long-skill')
  mkdirSync(skillDir, { recursive: true })
  writeFileSync(join(skillDir, 'SKILL.md'), '---\nname: long-skill\ndescription: "Create, read, edit, or manipulate Word documents (docx files) or Word templates (dotx files). Triggers include any mention of Word, docx, or word document."\n---\nbody\n')

  await page.goto('/')
  await sendPrompt(page, `palette-e2e ${Date.now()}`)
  await expect(page.getByTestId('assistant-message')).toContainText('ok')
  const bubbles = page.getByTestId('user-bubble')
  const before = await bubbles.count()

  await page.getByTestId('command-btn').click()
  const palette = page.getByTestId('command-palette')
  await expect(palette).toBeVisible()
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

  await page.getByTestId('composer-input').press('Enter')
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
  await page.getByTestId('new-session').click()
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill('e2e-hold')
  await page.getByTestId('composer-send').click()
  await expect(page.getByTestId('composer-stop')).toBeVisible()
  await expect.poll(async () => (await sessionQueue(page)).running).toBe(true)
  try {
    await input.fill('queued-keep')
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
    await expect(page.getByTestId('queued-item').last()).toContainText('Ctrl+Enter')
    await input.press('Control+Enter')
    await expect.poll(async () => (await sessionQueue(page)).texts).toEqual(['queued-keep'])
    await expect(page.getByTestId('queued-item')).toHaveCount(1)
    await expect(page.getByTestId('queued-list')).not.toContainText('queued-promote')
    await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-promote' })).toBeVisible()
  } finally {
    const stop = page.getByTestId('composer-stop')
    if (await stop.count()) await stop.click()
  }
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect.poll(async () => (await sessionQueue(page)).texts).toEqual([])
  await expect(page.getByTestId('user-bubble').filter({ hasText: 'queued-keep' })).toBeVisible()
})
