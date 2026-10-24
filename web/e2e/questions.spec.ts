import { expect, test, type Page } from '@playwright/test'
import { installQuestions } from './question-fixture'
import { expandWorkspaces, openSessionRow } from './sidebar'

test.describe.configure({ mode: 'parallel' })

async function open(page: Page) {
  await page.goto('/')
  await openSessionRow(page, '发布策略与上线准备')
  const rail = page.getByTestId('questions-rail')
  await expect(rail).toBeVisible()
  if (await rail.locator('.questions-mobile-toggle').isVisible()) await rail.locator('.questions-mobile-toggle').click()
  await page.getByTestId('question-card').click()
  await expect(page.getByTestId('question-dialog')).toBeVisible()
}

test('workspaces render before delayed sessions, with independent session retry and invalidation', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.addInitScript(() => { window.questionFixture.holdList = true })
  await page.goto('/')
  await expect(page.getByTestId('workspace-row')).toContainText('产品工作区')
  await expect(page.getByTestId('sessions-loading')).toBeVisible()
  await expect(page.getByTestId('session-list')).not.toHaveAttribute('data-ready', 'true')
  await page.evaluate(() => { window.questionFixture.failLists = true; window.questionFixture.releaseLists() })
  await expect(page.getByTestId('sessions-retry')).toBeVisible()
  await expect(page.getByTestId('workspace-row')).toContainText('产品工作区')
  const workspaceRequests = await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/workspaces').length)
  await page.evaluate(() => { window.questionFixture.failLists = false })
  await page.getByTestId('sessions-retry').click()
  await expandWorkspaces(page)
  await expect(page.getByTestId('session-row')).toContainText('发布策略与上线准备')
  await openSessionRow(page, '发布策略与上线准备')
  await expect(page.getByTestId('questions-rail')).toHaveCount(1)
  expect(await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/workspaces').length)).toBe(workspaceRequests)
  await page.evaluate(() => {
    window.questionFixture.holdList = true
    window.questionFixture.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect.poll(() => page.evaluate(() => window.questionFixture.pendingLists)).toBe(1)
  await expect(page.getByTestId('session-row')).toContainText('发布策略与上线准备')
  expect(await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/workspaces').length)).toBe(workspaceRequests)
  await page.evaluate(() => { window.questionFixture.failLists = true; window.questionFixture.releaseLists() })
  await expect(page.getByTestId('sessions-retry')).toBeVisible()
  await expect(page.getByTestId('questions-rail')).toHaveCount(1)
  await expect(page.getByTestId('session-row')).toContainText('发布策略与上线准备')
})

test('sessions render while workspaces are delayed and workspace failures retry independently', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.addInitScript(() => { window.questionFixture.holdWorkspaces = true })
  await page.goto('/')
  await expect(page.getByTestId('session-list')).toHaveAttribute('data-ready', 'true')
  await expect(page.getByTestId('workspaces-loading')).toBeVisible()
  await expect(page.getByTestId('session-row')).toContainText('发布策略与上线准备')
  await page.evaluate(() => { window.questionFixture.failWorkspaces = true; window.questionFixture.releaseWorkspaces() })
  await expect(page.getByTestId('workspaces-retry')).toBeVisible()
  await expect(page.getByTestId('session-row')).toContainText('发布策略与上线准备')
  const sessionRequests = await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/sessions').length)
  await page.evaluate(() => { window.questionFixture.failWorkspaces = false })
  await page.getByTestId('workspaces-retry').click()
  await expect(page.getByTestId('workspace-row')).toContainText('产品工作区')
  await expect(page.getByTestId('workspaces-retry')).toHaveCount(0)
  expect(await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/sessions').length)).toBe(sessionRequests)
  const workspaceRequests = await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/workspaces').length)
  await page.evaluate(() => window.questionFixture.emit('server', { type: 'invalidate', scope: 'workspaces' } as never))
  await expect.poll(() => page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/workspaces').length)).toBe(workspaceRequests + 1)
  expect(await page.evaluate(() => window.questionFixture.requests.filter(p => p === 'GET /v1/sessions').length)).toBe(sessionRequests)
})

test('question discussion loading stays expandable and accepts optimistic question updates', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.addInitScript(() => {
    window.questionFixture.sessions[0].questionLinksLoading = true
    window.questionFixture.details['question-main'].questions = []
  })
  await page.goto('/')
  await expandWorkspaces(page)
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await parent.getByTestId('session-toggle').click()
  await expect(page.getByTestId('question-links-loading')).toHaveText('Loading question discussions…')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.details['question-main'].questions = [state.question]
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await expect(page.getByTestId('question-side-link')).toHaveCount(1)
  await expect(page.getByTestId('question-links-loading')).toBeVisible()
  await page.evaluate(() => {
    window.questionFixture.sessions[0].questionLinksLoading = false
    window.questionFixture.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect(page.getByTestId('question-links-loading')).toHaveCount(0)
  await expect(page.getByTestId('question-side-link')).toHaveCount(1)
})

for (const width of [1440, 390]) test(`async question answer and independent side conversation at ${width}px`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 900 })
  await installQuestions(page)
  await open(page)
  const dialog = page.getByTestId('question-dialog')
  await expect(dialog.getByRole('radio').first()).toBeChecked()
  expect(await page.evaluate(() => window.questionFixture.prompts)).toEqual([])
  expect(await page.evaluate(() => window.questionFixture.forks)).toBe(0)
  await dialog.getByRole('textbox', { name: '补充细节，或写下自己的答案…' }).fill('必须具备明确的回滚指标。')
  await page.screenshot({ path: info.outputPath('question-answer.png') })
  if (width < 800) await dialog.getByRole('tab', { name: /旁聊/ }).click()
  const sideInput = dialog.getByRole('textbox', { name: '询问原因、比较选项，或进一步澄清…' })
  await sideInput.fill('为什么要先灰度？')
  await dialog.getByRole('button', { name: '发送旁聊消息' }).click()
  await expect(dialog.getByTestId('question-side-transcript')).toContainText('灰度发布能降低影响范围')
  expect(await page.evaluate(() => window.questionFixture.prompts.map(p => p.id))).toEqual(['question-side'])
  await expect(dialog.getByTestId('side-composer').getByRole('button')).toHaveCount(1)
  expect(await page.evaluate(() => window.questionFixture.prompts.filter(p => p.id === 'question-main'))).toEqual([])
  await page.screenshot({ path: info.outputPath('question-side.png') })
  if (width < 800) await dialog.getByRole('tab', { name: '回答问题' }).click()
  await expect(dialog.getByRole('textbox', { name: '补充细节，或写下自己的答案…' })).toHaveValue('必须具备明确的回滚指标。')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  expect(await page.evaluate(() => window.questionFixture.forks)).toBe(1)
  const rail = page.getByTestId('questions-rail')
  await expect(rail).toBeVisible()
  if (await rail.locator('.questions-mobile-toggle').isVisible()) await rail.locator('.questions-mobile-toggle').click()
  await page.getByTestId('question-card').click()
  await expect(dialog.getByRole('textbox', { name: '补充细节，或写下自己的答案…' })).toHaveValue('必须具备明确的回滚指标。')
  await dialog.getByTestId('question-submit').click()
  await expect(dialog.getByTestId('question-receipt')).toContainText('必须具备明确的回滚指标。')
  const prompts = await page.evaluate(() => window.questionFixture.prompts.filter(p => p.id === 'question-main'))
  expect(prompts).toHaveLength(1)
  expect(prompts[0].body).toHaveProperty('questionReply')
  expect(prompts[0].body).not.toHaveProperty('content')
  expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
})

test('question rail is isolated across sessions and never shifts message geometry', async ({ page }) => {
  await page.setViewportSize({ width: 1680, height: 1000 })
  await installQuestions(page)
  await page.goto('/')
  await page.evaluate(() => {
    const state = window.questionFixture
    const original = state.details['question-main']
    state.sessions.push({ ...state.sessions[0], id: 'question-other', title: '没有问题的独立会话' })
    state.details['question-other'] = { ...original, id: 'question-other', questions: [] }
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await openSessionRow(page, '发布策略与上线准备')
  const rail = page.getByTestId('questions-rail')
  await expect(rail).toBeVisible()
  const geometry = () => page.getByTestId('chat-scroll').evaluate(el => ({ x: el.getBoundingClientRect().x, width: el.getBoundingClientRect().width, padding: getComputedStyle(el).paddingLeft, contentX: el.querySelector('[data-testid="user-bubble"]')?.getBoundingClientRect().x }))
  const withRail = await geometry()
  await page.evaluate(() => { document.querySelector<HTMLElement>('[data-testid="questions-rail"]')!.style.display = 'none' })
  expect(await geometry()).toEqual(withRail)
  await page.evaluate(() => { document.querySelector<HTMLElement>('[data-testid="questions-rail"]')!.style.display = '' })
  await openSessionRow(page, '没有问题的独立会话')
  await expect(page.getByTestId('session-row').filter({ hasText: '没有问题的独立会话' }).locator('.session-main')).toHaveAttribute('aria-current', 'page')
  await expect(rail).toHaveCount(0)
  await page.evaluate(() => window.questionFixture.emit('server', { type: 'question_updated', sessionId: 'question-main', question: window.questionFixture.question }))
  await expect(rail).toHaveCount(0)
  await openSessionRow(page, '发布策略与上线准备')
  await expect(rail).toBeVisible()
  if (await rail.locator('.questions-mobile-toggle').isVisible()) await rail.locator('.questions-mobile-toggle').click()
  await page.getByTestId('question-card').click()
  await expect(page.getByTestId('question-dialog')).toBeVisible()
})

test('closing a running side preserves it, and sidebar reopens the same question', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await installQuestions(page, 'en')
  await open(page)
  await page.evaluate(() => { window.questionFixture.holdSide = true })
  const dialog = page.getByTestId('question-dialog')
  await dialog.getByRole('textbox', { name: 'Ask for an explanation, compare choices, or clarify trade-offs…' }).fill('Compare the rollout risks')
  await dialog.getByRole('button', { name: 'Send side message' }).click()
  await expect(dialog.getByRole('button', { name: 'Stop side conversation' })).toBeVisible()
  await page.keyboard.press('Escape')
  expect(await page.evaluate(() => window.questionFixture.details['question-side'].running)).toBe(true)
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await expect(parent.getByTestId('session-toggle')).toHaveAttribute('aria-expanded', 'true')
  const sideRow = page.getByTestId('session-row').filter({ hasText: '发布范围' })
  await expect(sideRow.locator('.session-type-mark')).toHaveAccessibleName('Side conversation')
  await sideRow.locator('.session-main').click()
  await expect(dialog).toBeVisible()
  await expect(parent.locator('.session-main')).toHaveAttribute('aria-current', 'page')
  await expect(dialog.getByRole('button', { name: 'Stop side conversation' })).toBeVisible()
  await dialog.getByRole('button', { name: 'Stop side conversation' }).click()
  await expect(dialog.getByRole('button', { name: 'Send side message' })).toBeVisible()
})

test('question discussions appear before side creation and repeated selection preserves one live rail', async ({ page }) => {
  await page.setViewportSize({ width: 1680, height: 1000 })
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  await installQuestions(page, 'en')
  await page.goto('/')
  await expandWorkspaces(page)
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await expect(parent.getByTestId('session-toggle')).toBeVisible()
  await parent.getByTestId('session-toggle').click()
  const link = page.getByTestId('question-side-link')
  await expect(link).toHaveCount(1)
  await link.getByRole('button').click()
  await expect(page.getByTestId('question-dialog')).toBeVisible()
  expect(await page.evaluate(() => window.questionFixture.forks)).toBe(0)
  await page.keyboard.press('Escape')
  const rail = page.getByTestId('questions-rail')
  await expect(rail).toHaveCount(1)
  await rail.evaluate(el => { el.setAttribute('data-original-instance', 'true') })
  await page.evaluate(() => { window.questionFixture.requests = [] })
  for (let i = 0; i < 4; i++) await parent.locator('.session-main').click()
  await expect(rail).toHaveCount(1)
  await expect(rail).toHaveAttribute('data-original-instance', 'true')
  expect(await page.evaluate(() => window.questionFixture.requests.filter(path => /^GET \/v1\/sessions\/question-main(?:[?]|$)/.test(path)))).toEqual([])
  for (let i = 0; i < 3; i++) {
    await page.getByTestId('tab-context').click()
    await expect(rail).toHaveCount(0)
    await page.getByTestId('tab-conversation').click()
    await expect(rail).toHaveCount(1)
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await expect(rail).toHaveCount(1)
  }
  await page.getByTestId('question-card').click()
  await expect(page.getByTestId('question-dialog')).toHaveCount(1)
  expect(errors).toEqual([])
})

test('question updates while another session is selected expose a discussion without reopening the source', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.goto('/')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.sessions.push({ ...state.sessions[0], id: 'other', title: 'Other session' })
    state.details.other = { ...state.details['question-main'], id: 'other', questions: [] }
    state.details['question-main'].questions = []
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await openSessionRow(page, 'Other session')
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await expect(parent.getByTestId('session-toggle')).toHaveCount(0)
  await page.evaluate(() => {
    const state = window.questionFixture
    state.details['question-main'].questions = [state.question]
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await expect(parent.getByTestId('session-toggle')).toBeVisible()
  await parent.getByTestId('session-toggle').click()
  await page.getByTestId('question-side-link').getByRole('button').click()
  await expect(page.getByTestId('question-dialog')).toBeVisible()
  await expect(page.getByTestId('questions-rail')).toHaveCount(1)
})

test('an older list response cannot hide a newly published discussion', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.goto('/')
  await expandWorkspaces(page)
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await expect(parent.getByTestId('session-toggle')).toBeVisible()
  await page.evaluate(() => {
    const state = window.questionFixture
    state.holdList = true
    state.details['question-main'].questions = []
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect.poll(() => page.evaluate(() => window.questionFixture.pendingLists)).toBe(1)
  await page.evaluate(() => {
    const state = window.questionFixture
    state.question = { ...state.question, revision: 3, title: '最新发布问题' }
    state.details['question-main'].questions = [state.question]
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await parent.getByTestId('session-toggle').click()
  await expect(page.getByTestId('question-side-link')).toContainText('最新发布问题')
  await page.evaluate(() => window.questionFixture.releaseLists())
  await expect.poll(() => page.evaluate(() => window.questionFixture.requests.filter(path => path === 'GET /v1/sessions').length)).toBeGreaterThanOrEqual(3)
  await expect(page.getByTestId('question-side-link')).toContainText('最新发布问题')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: { ...state.question, revision: 2, title: '旧问题' } })
  })
  await expect(page.getByTestId('question-side-link')).toContainText('最新发布问题')
})

test('pending question summaries preserve newer event links until a settled projection removes them', async ({ page }) => {
  await installQuestions(page, 'en')
  await page.goto('/')
  await expandWorkspaces(page)
  const parent = page.getByTestId('session-row').filter({ hasText: '发布策略与上线准备' })
  await parent.getByTestId('session-toggle').click()
  await page.evaluate(() => {
    const state = window.questionFixture
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: { ...state.question, revision: 5, title: '最新实时问题' } })
  })
  await expect(page.getByTestId('question-side-link')).toContainText('最新实时问题')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.sessions[0].questionLinksLoading = true
    state.details['question-main'].questions = [state.question, { ...state.question, questionItemId: 'historical-question', title: '历史旁聊问题' }]
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect(page.getByTestId('question-links-loading')).toBeVisible()
  await expect(page.getByTestId('question-side-link')).toHaveCount(2)
  await expect(page.getByTestId('question-side-link').first()).toContainText('最新实时问题')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.details['question-main'].questions = []
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect.poll(() => page.evaluate(() => window.questionFixture.requests.filter(path => path === 'GET /v1/sessions').length)).toBeGreaterThanOrEqual(3)
  await expect(page.getByTestId('question-side-link')).toHaveCount(2)
  await page.evaluate(() => {
    const state = window.questionFixture
    state.sessions[0].questionLinksLoading = false
    state.emit('server', { type: 'invalidate', scope: 'sessions' } as never)
  })
  await expect(page.getByTestId('question-links-loading')).toHaveCount(0)
  await expect(page.getByTestId('question-side-link')).toHaveCount(0)
  await expect(parent.getByTestId('session-toggle')).toHaveCount(0)
})

test('conflicting answer is first-wins and does not erase the local draft', async ({ page }) => {
  await installQuestions(page)
  await open(page)
  await page.evaluate(() => { window.questionFixture.conflict = true })
  const dialog = page.getByTestId('question-dialog')
  await dialog.getByRole('textbox', { name: '补充细节，或写下自己的答案…' }).fill('保留这份草稿')
  await dialog.getByTestId('question-submit').click()
  await expect(dialog.getByRole('alert')).toContainText('其它窗口更新')
  await expect(dialog.getByTestId('question-receipt')).toContainText('另一窗口的答案')
  expect(await page.evaluate(() => Object.keys(sessionStorage).some(k => k.startsWith('ki:question-draft:') && sessionStorage.getItem(k)?.includes('保留这份草稿')))).toBe(true)
})

test('queued answers offer explicit continue without resubmitting, and branch receipts stay immutable', async ({ page }) => {
  await installQuestions(page)
  await open(page)
  const dialog = page.getByTestId('question-dialog')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.question = { ...state.question, revision: 2, status: 'answer_queued', answer: '先灰度 10%' }
    state.details['question-main'].questions = [state.question]
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await expect(dialog.getByTestId('question-receipt')).toContainText('等待投递')
  await dialog.getByRole('button', { name: '继续主对话', exact: true }).click()
  expect(await page.evaluate(() => window.questionFixture.actions)).toEqual(['continue'])
  expect(await page.evaluate(() => window.questionFixture.prompts)).toEqual([])
  await page.evaluate(() => {
    const state = window.questionFixture
    state.question = { ...state.question, revision: 3, status: 'answered', answerBranchValid: false }
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await expect(dialog.getByRole('status')).toContainText('当前分支未包含该答案')
  await expect(dialog.getByTestId('question-submit')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '继续主对话', exact: true })).toHaveCount(0)
})

test('question drafts survive reload and wrong-source branches cannot submit', async ({ page }) => {
  await installQuestions(page)
  await open(page)
  const dialog = page.getByTestId('question-dialog')
  const input = dialog.getByRole('textbox', { name: '补充细节，或写下自己的答案…' })
  await input.fill('刷新后继续编辑')
  await page.reload()
  await open(page)
  await expect(input).toHaveValue('刷新后继续编辑')
  await page.evaluate(() => {
    const state = window.questionFixture
    state.question = { ...state.question, revision: 2, sourceBranchValid: false }
    state.emit('server', { type: 'question_updated', sessionId: 'question-main', question: state.question })
  })
  await expect(dialog.getByTestId('question-submit')).toBeDisabled()
  await expect(dialog.getByRole('status')).toContainText('其它分支')
})
