import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { serverToken, statePath } from './global-setup.ts'
import { goBinary } from './go-toolchain.ts'

const repo = join(dirname(fileURLToPath(import.meta.url)), '../..')
const fixtureName = 'responsive-fixture'
const previewName = 'responsive-preview-long-name.txt'
const previewImageName = 'responsive-preview.png'
const previewPDFName = 'responsive-preview.pdf'

function responsivePDF(): Buffer {
  const stream = (text: string) => `<< /Length ${Buffer.byteLength(text)} >>\nstream\n${text}\nendstream`
  const pageOne = 'BT /F1 18 Tf 72 720 Td (Responsive page 1) Tj ET'
  const pageTwo = 'BT /F1 18 Tf 72 720 Td (Responsive page 2) Tj ET'
  const objects = [
    '1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n',
    '2 0 obj\n<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>\nendobj\n',
    '3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>\nendobj\n',
    `4 0 obj\n${stream(pageOne)}\nendobj\n`,
    '5 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 7 0 R >> >> /Contents 6 0 R >>\nendobj\n',
    `6 0 obj\n${stream(pageTwo)}\nendobj\n`,
    '7 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n',
  ]
  let body = '%PDF-1.4\n'
  const offsets = [0]
  for (const object of objects) {
    offsets.push(Buffer.byteLength(body))
    body += object
  }
  const xref = Buffer.byteLength(body)
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`
  body += offsets.slice(1).map(offset => `${String(offset).padStart(10, '0')} 00000 n \n`).join('')
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`
  return Buffer.from(body)
}

type Profile = {
  name: string
  width: number
  height: number
  compact: boolean
  mobile: boolean
  touch: boolean
  scale: number
}

// Keep the dimensions in the test titles so a slow or failing slice can be
// rerun directly with --grep without weakening the complete device matrix.
const profiles: readonly Profile[] = [
  { name: 'phone-320x568', width: 320, height: 568, compact: true, mobile: true, touch: true, scale: 2 },
  { name: 'phone-390x844', width: 390, height: 844, compact: true, mobile: true, touch: true, scale: 3 },
  { name: 'phone-landscape-844x390', width: 844, height: 390, compact: true, mobile: true, touch: true, scale: 3 },
  { name: 'tablet-768x1024', width: 768, height: 1024, compact: true, mobile: true, touch: true, scale: 2 },
  { name: 'tablet-820x1180', width: 820, height: 1180, compact: true, mobile: true, touch: true, scale: 2 },
  { name: 'laptop-1024x768', width: 1024, height: 768, compact: false, mobile: false, touch: false, scale: 1 },
  { name: 'desktop-1440x900', width: 1440, height: 900, compact: false, mobile: false, touch: false, scale: 1 },
]

async function expectNoPageOverflow(page: Page, surface: string): Promise<void> {
  const metrics = await page.evaluate(() => ({
    innerWidth: window.innerWidth,
    visualWidth: window.visualViewport?.width ?? window.innerWidth,
    scrollX: window.scrollX,
    rootClientWidth: document.documentElement.clientWidth,
    rootScrollWidth: document.documentElement.scrollWidth,
    bodyClientWidth: document.body.clientWidth,
    bodyScrollWidth: document.body.scrollWidth,
    scrollingClientWidth: document.scrollingElement?.clientWidth ?? 0,
    scrollingScrollWidth: document.scrollingElement?.scrollWidth ?? 0,
  }))
  expect(metrics.scrollX, `${surface}: page should not be horizontally shifted`).toBe(0)
  expect(
    metrics.rootScrollWidth,
    `${surface}: root width ${JSON.stringify(metrics)}`,
  ).toBeLessThanOrEqual(metrics.rootClientWidth + 1)
  expect(
    metrics.bodyScrollWidth,
    `${surface}: body width ${JSON.stringify(metrics)}`,
  ).toBeLessThanOrEqual(metrics.bodyClientWidth + 1)
  expect(
    metrics.scrollingScrollWidth,
    `${surface}: scrolling element width ${JSON.stringify(metrics)}`,
  ).toBeLessThanOrEqual(metrics.scrollingClientWidth + 1)
  expect(
    metrics.rootClientWidth,
    `${surface}: layout viewport ${JSON.stringify(metrics)}`,
  ).toBeLessThanOrEqual(Math.ceil(metrics.innerWidth) + 1)
  expect(metrics.visualWidth, `${surface}: visual viewport should be usable`).toBeGreaterThan(0)
}

async function expectInsideViewport(page: Page, locator: Locator, surface: string): Promise<void> {
  await expect(locator, `${surface} should be visible`).toBeVisible()
  const metrics = await locator.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const visual = window.visualViewport
    const left = visual?.offsetLeft ?? 0
    const top = visual?.offsetTop ?? 0
    const width = visual?.width ?? window.innerWidth
    const height = visual?.height ?? window.innerHeight
    return {
      rect: { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom },
      viewport: { left, top, right: left + width, bottom: top + height },
    }
  })
  const outliers = metrics.rect.left < metrics.viewport.left - 1 || metrics.rect.right > metrics.viewport.right + 1
    ? await horizontalOutliers(page)
    : []
  const diagnostic = outliers.length ? `; horizontal outliers ${JSON.stringify(outliers)}` : ''
  expect(metrics.rect.left, `${surface} left edge${diagnostic}`).toBeGreaterThanOrEqual(metrics.viewport.left - 1)
  expect(metrics.rect.top, `${surface} top edge`).toBeGreaterThanOrEqual(metrics.viewport.top - 1)
  expect(metrics.rect.right, `${surface} right edge${diagnostic}`).toBeLessThanOrEqual(metrics.viewport.right + 1)
  expect(metrics.rect.bottom, `${surface} bottom edge`).toBeLessThanOrEqual(metrics.viewport.bottom + 1)
}

async function expectTouchTarget(profile: Profile, locator: Locator, surface: string): Promise<void> {
  if (!profile.touch) return
  await expect(locator, `${surface} should be visible`).toBeVisible()
  const box = await locator.boundingBox()
  expect(box, `${surface} should have a layout box`).toBeTruthy()
  expect(box!.width, `${surface} touch width`).toBeGreaterThanOrEqual(40)
  expect(box!.height, `${surface} touch height`).toBeGreaterThanOrEqual(40)
}

async function expectTouchButtons(profile: Profile, root: Locator, surface: string): Promise<void> {
  const unnamed = await root.locator('button, a[href], [role="option"], [role="menuitem"], [role="tab"], [role="radio"], [role="switch"]').evaluateAll(elements => elements.flatMap(control => {
    const element = control as HTMLElement
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    if (
      !element.isConnected
      || element.closest('[inert]')
      || style.display === 'none'
      || style.visibility === 'hidden'
      || rect.width <= 0
      || rect.height <= 0
    ) return []
    const labelledBy = (element.getAttribute('aria-labelledby') ?? '')
      .split(/\s+/)
      .filter(Boolean)
      .map(id => document.getElementById(id)?.textContent?.trim() ?? '')
      .join(' ')
      .trim()
    const name = element.getAttribute('aria-label')?.trim()
      || labelledBy
      || element.textContent?.trim()
      || element.getAttribute('title')?.trim()
    if (name) return []
    return [element.dataset.testid || element.className || element.tagName.toLowerCase()]
  }))
  expect(unnamed, `${surface}: visible interactive controls need accessible names`).toEqual([])
  if (!profile.touch) return
  const controls = [
    'button',
    '[role="option"]',
    '[role="menuitem"]',
    'a[href]',
    'input:not([type="hidden"]):not([type="checkbox"]):not([type="radio"])',
    'select',
    'textarea',
    'label:has(input[type="checkbox"])',
    'label:has(input[type="radio"])',
  ].join(', ')
  const undersized = await root.locator(controls).evaluateAll(elements => elements.flatMap(control => {
    const element = control as HTMLElement
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    if (
      !element.isConnected
      || element.closest('[inert]')
      || style.display === 'none'
      || style.visibility === 'hidden'
      || style.pointerEvents === 'none'
      || rect.width <= 0
      || rect.height <= 0
    ) return []
    if (rect.width >= 39.5 && rect.height >= 39.5) return []
    return [{
      label: element.getAttribute('aria-label') || element.dataset.testid || element.className || element.textContent?.trim().slice(0, 40) || 'button',
      width: Math.round(rect.width * 10) / 10,
      height: Math.round(rect.height * 10) / 10,
    }]
  }))
  expect(undersized, `${surface}: visible interactive controls smaller than 40px`).toEqual([])
}

async function horizontalOutliers(page: Page): Promise<Array<Record<string, string | number>>> {
  return page.evaluate(() => {
    const viewportRight = (window.visualViewport?.offsetLeft ?? 0) + (window.visualViewport?.width ?? window.innerWidth)
    const label = (element: Element): string => {
      const html = element as HTMLElement
      if (html.dataset.testid) return `[data-testid="${html.dataset.testid}"]`
      if (element.id) return `${element.tagName.toLowerCase()}#${element.id}`
      const classes = Array.from(element.classList).slice(0, 3).join('.')
      return `${element.tagName.toLowerCase()}${classes ? `.${classes}` : ''}`
    }
    return Array.from(document.body.querySelectorAll<HTMLElement>('*')).flatMap(element => {
      const rect = element.getBoundingClientRect()
      if (!rect.width || !rect.height || rect.right <= viewportRight + 1) return []
      const style = getComputedStyle(element)
      return [{
        selector: `${element.parentElement ? `${label(element.parentElement)} > ` : ''}${label(element)}`,
        left: Math.round(rect.left * 10) / 10,
        right: Math.round(rect.right * 10) / 10,
        width: Math.round(rect.width * 10) / 10,
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
        cssWidth: style.width,
        minWidth: style.minWidth,
        display: style.display,
        columns: style.gridTemplateColumns,
        overflowX: style.overflowX,
      }]
    }).sort((a, b) => b.right - a.right || b.width - a.width).slice(0, 12)
  })
}

async function expectProfileContext(page: Page, profile: Profile): Promise<void> {
  expect(page.viewportSize(), `${profile.name}: Playwright viewport`).toEqual({
    width: profile.width,
    height: profile.height,
  })
  const capabilities = await page.evaluate(() => ({
    maxTouchPoints: navigator.maxTouchPoints,
    coarsePointer: window.matchMedia('(pointer: coarse)').matches,
    screenWidth: window.screen.width,
    screenHeight: window.screen.height,
  }))
  expect(capabilities.screenWidth, `${profile.name}: emulated screen width`).toBe(profile.width)
  expect(capabilities.screenHeight, `${profile.name}: emulated screen height`).toBe(profile.height)
  if (profile.touch) {
    expect(capabilities.maxTouchPoints, `${profile.name}: touch context`).toBeGreaterThan(0)
    expect(capabilities.coarsePointer, `${profile.name}: coarse pointer media query`).toBe(true)
  }
}

async function expectDrawerContract(page: Page, profile: Profile): Promise<void> {
  const sidebar = page.locator('aside.sidebar')
  const main = page.locator('main.main')
  const toggle = page.getByTestId('mobile-nav-toggle')

  if (!profile.compact) {
    await expect(toggle).toHaveCount(0)
    await expect(sidebar).toBeVisible()
    await expect(sidebar).not.toHaveAttribute('inert', '')
    await expect(main).not.toHaveAttribute('inert', '')
    return
  }

  await expect(toggle).toBeVisible()
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(sidebar).toHaveAttribute('aria-hidden', 'true')
  await expect(sidebar).toHaveAttribute('inert', '')
  await expect(main).not.toHaveAttribute('inert', '')

  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(sidebar).toHaveAttribute('aria-hidden', 'false')
  await expect(sidebar).not.toHaveAttribute('inert', '')
  await expect(main).toHaveAttribute('aria-hidden', 'true')
  await expect(main).toHaveAttribute('inert', '')
  await expect.poll(
    () => sidebar.evaluate(element => element.getBoundingClientRect().left),
    { message: `${profile.name}: navigation drawer should finish its opening transition` },
  ).toBeGreaterThanOrEqual(-1)
  await expectInsideViewport(page, sidebar, `${profile.name} open navigation drawer`)
  await expectNoPageOverflow(page, `${profile.name} open navigation drawer`)
  await expectTouchTarget(profile, sidebar.locator('.search-btn'), `${profile.name} session-search button`)
  await expectTouchButtons(profile, sidebar, `${profile.name} navigation drawer`)

  await clickDrawerBackdrop(page)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(sidebar).toHaveAttribute('inert', '')
  await expect(main).not.toHaveAttribute('inert', '')
}

async function clickDrawerBackdrop(page: Page): Promise<void> {
  const backdrop = page.locator('.mobile-sidebar-backdrop')
  const box = await backdrop.boundingBox()
  expect(box, 'navigation drawer backdrop should have a layout box').toBeTruthy()
  // The backdrop spans beneath the higher-z-index drawer. Click its exposed
  // right edge instead of the covered center used by Locator.click by default.
  await backdrop.click({ position: { x: Math.max(1, box!.width - 4), y: Math.max(1, box!.height / 2) } })
}

async function openDrawer(page: Page, compact: boolean): Promise<void> {
  if (!compact) return
  const toggle = page.getByTestId('mobile-nav-toggle')
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.click()
  await expect(page.locator('aside.sidebar')).not.toHaveAttribute('inert', '')
}

async function closeDrawer(page: Page, compact: boolean): Promise<void> {
  if (!compact) return
  const toggle = page.getByTestId('mobile-nav-toggle')
  if (await toggle.getAttribute('aria-expanded') !== 'true') return
  await clickDrawerBackdrop(page)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
}

async function expectDialogChrome(
  page: Page,
  dialog: Locator,
  surface: string,
  header: string,
  footer?: string,
): Promise<void> {
  await expectInsideViewport(page, dialog, surface)
  await expectInsideViewport(page, dialog.locator(header).first(), `${surface} header`)
  if (footer) await expectInsideViewport(page, dialog.locator(footer).first(), `${surface} footer`)
  await expectNoPageOverflow(page, surface)
}

async function expectFocusWithin(dialog: Locator, surface: string): Promise<void> {
  await expect.poll(
    () => dialog.evaluate(element => element.contains(document.activeElement)),
    { message: `${surface} should own focus` },
  ).toBe(true)
}

async function createTreeFixture(request: APIRequestContext): Promise<{ rootTitle: string; childTitle: string }> {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  const stamp = `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
  const rootTitle = `responsive-tree-root-${stamp}`
  const childTitle = `responsive-tree-child-${stamp}`
  const rootResponse = await request.post('/v1/sessions', { headers, data: {} })
  expect(rootResponse.ok()).toBe(true)
  const root = await rootResponse.json() as { id: string }
  expect((await request.patch(`/v1/sessions/${root.id}`, { headers, data: { title: rootTitle } })).ok()).toBe(true)
  const childResponse = await request.post(`/v1/sessions/${root.id}/fork`, { headers, data: { forkMode: 'tree' } })
  expect(childResponse.ok()).toBe(true)
  const child = await childResponse.json() as { id: string }
  expect((await request.patch(`/v1/sessions/${child.id}`, { headers, data: { title: childTitle } })).ok()).toBe(true)
  return { rootTitle, childTitle }
}

let previousDisabled: string[] = []

test.beforeAll(async ({ request }) => {
  const { home } = JSON.parse(readFileSync(statePath, 'utf8')) as { home: string }
  const binDir = join(home, 'playwright-bin')
  mkdirSync(binDir, { recursive: true })
  const bin = join(binDir, `responsive-sidecar${process.platform === 'win32' ? '.exe' : ''}`)
  execFileSync(goBinary(), ['build', '-o', bin, '.'], {
    cwd: join(repo, 'e2e/testdata/extensions/sidecar'),
    stdio: 'inherit',
  })

  const dir = join(home, 'extensions', fixtureName)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'extension.json'), JSON.stringify({
    name: fixtureName,
    capabilities: ['settings', 'lifecycle'],
    config: {
      schema: {
        type: 'object',
        additionalProperties: false,
        properties: {
          label: { type: 'string', description: 'Responsive fixture label' },
          retries: { type: 'number', minimum: 0, maximum: 9 },
        },
      },
      defaults: { label: 'A deliberately long extension setting value', retries: 2 },
    },
    runtime: {
      kind: 'rpc',
      command: bin,
      env: {
        KI_SET_UI: '1',
        KI_STATUS_TEXT: 'Responsive · active',
        KI_PANEL_TITLE: 'Responsive fixture',
        KI_PANEL_SUMMARY: 'Responsive extension panel with fields and actions',
      },
    },
  }))

  const headers = { Authorization: `Bearer ${serverToken()}` }
  await request.post('/v1/reload', { headers })
  const listed = await request.get('/v1/extensions', { headers })
  expect(listed.ok()).toBe(true)
  const catalog = await listed.json() as { items?: Array<{ name: string, enabled?: boolean }> }
  previousDisabled = (catalog.items ?? []).filter(item => item.enabled === false).map(item => item.name)
  const disabled = (catalog.items ?? []).map(item => item.name).filter(name => name !== fixtureName)
  const patched = await request.patch('/v1/extensions', { headers, data: { disabled } })
  expect(patched.ok()).toBe(true)
  await request.post('/v1/reload', { headers })
})

test.afterAll(async ({ request }) => {
  const headers = { Authorization: `Bearer ${serverToken()}` }
  await request.patch('/v1/extensions', {
    headers,
    data: { disabled: Array.from(new Set([...previousDisabled, fixtureName])) },
  })
  await request.post('/v1/reload', { headers })
})

for (const profile of profiles) {
  test.describe(profile.name, () => {
    // setViewportSize alone does not enable meta-viewport or coarse-pointer
    // behavior. These options create a genuine mobile/touch browser context.
    test.use({
      viewport: { width: profile.width, height: profile.height },
      contextOptions: { screen: { width: profile.width, height: profile.height } },
      isMobile: profile.mobile,
      hasTouch: profile.touch,
      deviceScaleFactor: profile.scale,
    })

    test('signed-out entry stays inside the viewport', async ({ page }) => {
      await page.context().clearCookies()
      await page.goto('/')
      await expectProfileContext(page, profile)
      await expectInsideViewport(page, page.getByTestId('auth-login'), `${profile.name} login`)
      await expectNoPageOverflow(page, `${profile.name} login`)
      await expectTouchButtons(profile, page.getByTestId('auth-login'), `${profile.name} login`)
    })

    test('shell, drawer, and dialogs keep their controls reachable', async ({ page }) => {
      test.setTimeout(60_000)
      await page.goto('/')
      await expect(page.getByTestId('hero')).toBeVisible()
      await expectProfileContext(page, profile)
      await expectInsideViewport(page, page.locator('.conv-header'), `${profile.name} conversation header`)
      await expectInsideViewport(page, page.getByTestId('composer-card'), `${profile.name} hero composer`)
      await expectNoPageOverflow(page, `${profile.name} hero`)
      await expectTouchButtons(profile, page.locator('main.main'), `${profile.name} hero`)
      await expectDrawerContract(page, profile)

      await openDrawer(page, profile.compact)
      const addWorkspace = page.getByTestId('add-workspace').first()
      await addWorkspace.click()
      const directory = page.getByTestId('dir-browser')
      await expectDialogChrome(page, directory, `${profile.name} directory dialog`, '.dir-head', '.dir-foot')
      await expectFocusWithin(directory, `${profile.name} directory dialog`)
      await expectTouchButtons(profile, directory, `${profile.name} directory dialog`)
      await page.getByTestId('dir-new-folder').click()
      const createDirectory = page.getByTestId('dir-create').getByRole('dialog')
      await expectInsideViewport(page, createDirectory, `${profile.name} create-directory dialog`)
      await expectTouchButtons(profile, createDirectory, `${profile.name} create-directory dialog`)
      await expect(page.getByTestId('dir-new-name')).toBeFocused()
      await page.keyboard.press('Escape')
      await expect(page.getByTestId('dir-create')).toHaveCount(0)
      await expect(directory).toBeVisible()
      await expect(page.getByTestId('dir-new-folder')).toBeFocused()
      await page.getByTestId('dir-browser-mask').getByRole('button', { name: /取消|Cancel/ }).click()
      await expect(profile.compact ? page.getByTestId('mobile-nav-toggle') : addWorkspace).toBeFocused()

      await openDrawer(page, profile.compact)
      const settingsTrigger = page.getByTestId('open-settings')
      await settingsTrigger.click()
      const settings = page.getByTestId('settings')
      await expectDialogChrome(page, settings, `${profile.name} settings`, '.modal-head')
      await expectFocusWithin(settings, `${profile.name} settings`)
      await page.getByTestId('add-provider').click()
      const newProvider = page.getByTestId('new-provider-dialog')
      await expectDialogChrome(page, newProvider, `${profile.name} new-provider dialog`, '.provider-dialog-head', '.provider-dialog-actions')
      await expectTouchTarget(profile, newProvider.locator('.provider-dialog-close'), `${profile.name} new-provider close`)
      await expectTouchButtons(profile, newProvider, `${profile.name} new-provider dialog`)
      await page.keyboard.press('Escape')
      await expect(newProvider).toHaveCount(0)
      await expect(settings).toBeVisible()
      await expect(page.getByTestId('add-provider')).toBeFocused()

      const editModel = page.getByTestId('edit-model').first()
      await expect(editModel).toBeVisible()
      await editModel.click()
      const advancedModel = page.getByTestId('model-advanced')
      await expectDialogChrome(page, advancedModel, `${profile.name} advanced-model dialog`, '.provider-dialog-head', '.provider-dialog-actions')
      await expectTouchTarget(profile, advancedModel.locator('.provider-dialog-close'), `${profile.name} advanced-model close`)
      await expectTouchButtons(profile, advancedModel, `${profile.name} advanced-model dialog`)
      await page.keyboard.press('Escape')
      await expect(advancedModel).toHaveCount(0)
      await expect(settings).toBeVisible()
      await expect(editModel).toBeFocused()

      for (const [tab, content] of [
        ['providers', 'provider-settings'],
        ['skills', 'skills-settings'],
        ['extensions', 'extensions-settings'],
        ['message', 'message-settings'],
        ['appearance', 'appearance-settings'],
      ] as const) {
        await page.getByTestId(`settings-tab-${tab}`).click()
        await expect(page.getByTestId(content)).toBeVisible()
        await expectInsideViewport(page, settings.locator('.modal-head'), `${profile.name} settings ${tab} header`)
        await expectNoPageOverflow(page, `${profile.name} settings ${tab}`)
        await expectTouchButtons(profile, settings, `${profile.name} settings ${tab}`)
      }
      await settings.getByRole('button', { name: /关闭对话框|Close dialog/ }).click()
      await expect(profile.compact ? page.getByTestId('mobile-nav-toggle') : settingsTrigger).toBeFocused()

      await page.getByTestId('open-model').click()
      const model = page.getByTestId('model-dialog')
      await expectDialogChrome(page, model, `${profile.name} model picker`, '.modal-head')
      await expectTouchButtons(profile, model, `${profile.name} model picker`)
      await model.getByRole('button', { name: /关闭对话框|Close dialog/ }).click()

      await page.getByRole('button', { name: /添加图片或文件|Add image or file/ }).click()
      const attachments = page.getByRole('dialog', { name: /选择图片或文件|Choose an image or file/ })
      await expectDialogChrome(page, attachments, `${profile.name} attachment dialog`, '.modal-head', '.modal-actions')
      await expectTouchTarget(profile, attachments.locator('.attachment-crumb button').first(), `${profile.name} attachment breadcrumb`)
      await expectTouchButtons(profile, attachments, `${profile.name} attachment dialog`)
      await attachments.getByRole('button', { name: /取消|Cancel/ }).click()
    })

    test('chat, secondary views, and extension inspector remain usable', async ({ page, request }) => {
      test.setTimeout(60_000)
      await page.goto('/')
      const input = page.getByTestId('composer-input')
      await expect(input).toBeEnabled({ timeout: 15_000 })
      await input.fill(`responsive ${profile.name} ${Date.now()} ${'touch-target-content '.repeat(40)}`)
      await page.getByTestId('composer-send').click()
      await expect(page.getByTestId('assistant-message')).toContainText('ok')
      const bubbleToggle = page.getByTestId('user-bubble-toggle')
      await expect(bubbleToggle).toBeVisible()
      await expectTouchTarget(profile, bubbleToggle, `${profile.name} long-message toggle`)
      const sessionCwd = await page.evaluate(async () => {
        const sessions = await fetch('/v1/sessions', { credentials: 'same-origin' }).then(response => response.json()) as Array<{ cwd: string }>
        return sessions[0]?.cwd ?? ''
      })
      expect(sessionCwd).not.toBe('')
      writeFileSync(join(sessionCwd, previewName), `responsive preview\n${'unbroken-preview-content-'.repeat(80)}\n`)
      writeFileSync(join(sessionCwd, previewImageName), Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64'))
      writeFileSync(join(sessionCwd, previewPDFName), responsivePDF())
      await expectInsideViewport(page, page.locator('.conv-header'), `${profile.name} chat header`)
      await expectInsideViewport(page, page.getByTestId('composer-card'), `${profile.name} chat composer`)
      await expectNoPageOverflow(page, `${profile.name} chat`)
      await expectTouchButtons(profile, page.locator('main.main'), `${profile.name} chat`)

      await page.getByTestId('command-btn').click()
      const palette = page.getByTestId('command-palette')
      await expectInsideViewport(page, palette, `${profile.name} command palette`)
      await expectNoPageOverflow(page, `${profile.name} command palette`)
      await expectTouchButtons(profile, palette, `${profile.name} command palette`)
      const paletteID = await palette.getAttribute('id')
      expect(paletteID).toBeTruthy()
      await expect(page.getByTestId('composer-input')).toHaveAttribute('aria-expanded', 'true')
      await expect(page.getByTestId('composer-input')).toHaveAttribute('aria-controls', paletteID!)
      await expect(page.getByTestId('composer-input')).toHaveAttribute('aria-activedescendant', /.+/)
      const activeOptionID = await page.getByTestId('composer-input').getAttribute('aria-activedescendant')
      expect(activeOptionID).toBeTruthy()
      await expect(page.locator(`[id="${activeOptionID}"]`)).toHaveAttribute('role', 'option')

      const thinking = page.getByTestId('thinking-select')
      if (await thinking.count()) {
        // Keyboard focus moving to a sibling popup must retire the command
        // palette before that popup receives its own Enter/Escape handling.
        await thinking.focus()
        await thinking.press('Enter')
        await expect(palette).toHaveCount(0)
        const thinkingMenu = page.getByRole('listbox', { name: 'Thinking effort' })
        await expectInsideViewport(page, thinkingMenu, `${profile.name} thinking menu`)
        await expectNoPageOverflow(page, `${profile.name} thinking menu`)
        await expectTouchButtons(profile, thinkingMenu, `${profile.name} thinking menu`)
        await page.keyboard.press('Escape')
        await expect(thinkingMenu).toHaveCount(0)
      } else {
        await page.keyboard.press('Escape')
        await expect(palette).toHaveCount(0)
      }

      // The command button seeds `/`; clear it so remounting the composer on
      // another tab does not intentionally reopen the palette.
      await page.getByTestId('composer-input').fill('')

      await page.getByRole('button', { name: /添加图片或文件|Add image or file/ }).click()
      const attachments = page.getByRole('dialog', { name: /选择图片或文件|Choose an image or file/ })
      const previewRow = attachments.locator('.attachment-row').filter({ hasText: previewName })
      await expect(previewRow).toBeVisible()
      await previewRow.click()
      await expect(previewRow).toHaveAttribute('aria-pressed', 'true')
      if (profile.height <= 540 && profile.width <= 900) {
        await expect(attachments.locator('.attachment-preview')).toBeHidden()
      } else {
        await expect(attachments.locator('.attachment-text-preview pre')).toContainText('responsive preview')
        await expectInsideViewport(page, attachments.locator('.attachment-preview'), `${profile.name} attachment preview`)
        await attachments.locator('.attachment-row').filter({ hasText: previewPDFName }).click()
        const pdf = attachments.locator('.pdf-preview')
        await expect(pdf).toBeVisible({ timeout: 15_000 })
        const pdfToolbar = pdf.locator('.pdf-preview-toolbar')
        await expect(pdfToolbar).toBeVisible({ timeout: 15_000 })
        const nextPDFPage = pdfToolbar.getByRole('button', { name: /下一页|Next page/ })
        await expectTouchTarget(profile, nextPDFPage, `${profile.name} PDF next-page`)
        await nextPDFPage.click()
        await expect(pdfToolbar).toContainText('2')
      }
      await expectInsideViewport(page, attachments.locator('.modal-actions'), `${profile.name} attachment footer after preview`)
      await expectNoPageOverflow(page, `${profile.name} attachment preview`)
      await expectTouchButtons(profile, attachments, `${profile.name} attachment preview`)
      await attachments.getByRole('button', { name: /取消|Cancel/ }).click()

      await page.getByRole('button', { name: /添加图片或文件|Add image or file/ }).click()
      const imageAttachments = page.getByRole('dialog', { name: /选择图片或文件|Choose an image or file/ })
      await imageAttachments.locator('.attachment-row').filter({ hasText: previewImageName }).click()
      await imageAttachments.getByRole('button', { name: /添加|Add/ }).click()
      const openImage = page.getByRole('button', { name: /放大查看图片|Open image preview/ })
      await expect(openImage).toBeVisible()
      await openImage.click()
      const lightbox = page.getByRole('dialog', { name: /图片预览|Image preview/ })
      await expectInsideViewport(page, lightbox, `${profile.name} image lightbox`)
      await expectFocusWithin(lightbox, `${profile.name} image lightbox`)
      await expectNoPageOverflow(page, `${profile.name} image lightbox`)
      await expectTouchButtons(profile, lightbox, `${profile.name} image lightbox`)
      await lightbox.getByRole('button', { name: /关闭图片预览|Close image preview/ }).click()
      await expect(openImage).toBeFocused()
      await page.getByRole('button', { name: /移除附件|Remove attachment/ }).click()

      await page.getByTestId('tab-trajectory').click()
      await expect(page.getByTestId('trajectory')).toBeVisible()
      await expectTouchTarget(profile, page.getByTestId('traj-zoom-out'), `${profile.name} trajectory zoom-out`)
      await expectTouchTarget(profile, page.locator('.tl-bar').first(), `${profile.name} trajectory timeline record`)
      await page.locator('[data-testid="traj-row"][data-kind="assistant"]').first().click()
      await expectInsideViewport(page, page.getByTestId('traj-inspector'), `${profile.name} trajectory inspector`)
      await expectNoPageOverflow(page, `${profile.name} trajectory`)
      await expectTouchButtons(profile, page.getByTestId('trajectory'), `${profile.name} trajectory`)

      await page.getByTestId('tab-config').click()
      await expect(page.getByTestId('session-info')).toBeVisible()
      await expectNoPageOverflow(page, `${profile.name} info`)
      await expectTouchButtons(profile, page.getByTestId('session-info'), `${profile.name} info`)

      if (profile.compact) {
        // Compact layouts intentionally hide/reduce individual status chips;
        // the supported entry point is the aggregate inspector button.
        await expect(page.getByTestId('ext-chips-more')).toBeVisible({ timeout: 15_000 })
        await expectTouchTarget(profile, page.getByTestId('ext-chips-more'), `${profile.name} extension aggregate`)
        await page.getByTestId('ext-chips-more').click()
      } else {
        await expect(page.getByTestId(`ext-chip-${fixtureName}`)).toBeVisible({ timeout: 15_000 })
        await page.getByTestId(`ext-chip-${fixtureName}`).click()
      }
      const extension = page.getByTestId('ext-panel')
      await expectDialogChrome(page, extension, `${profile.name} extension dialog`, '.modal-head')
      await expect(page.getByTestId(`ext-nav-${fixtureName}`)).toBeVisible()
      await page.getByTestId(`ext-nav-${fixtureName}`).click()
      const detailsTab = page.getByTestId('ext-inspector-details-tab')
      await expect(detailsTab).toHaveAttribute('aria-selected', 'true')
      await expect(detailsTab).toHaveAttribute('aria-controls', 'ext-inspector-details-panel')
      await expect(detailsTab).toHaveAttribute('tabindex', '0')
      await expect(extension.locator('#ext-inspector-details-panel')).toHaveCount(1)
      await expect(extension.locator('#ext-inspector-config-panel')).toHaveAttribute('hidden', '')
      await expect(extension.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'ext-inspector-details-tab')
      await detailsTab.focus()
      await detailsTab.press('ArrowRight')
      const configTab = page.getByTestId('ext-inspector-config-tab')
      await expect(configTab).toBeFocused()
      await expect(configTab).toHaveAttribute('aria-selected', 'true')
      await expect(extension.getByRole('tabpanel')).toHaveAttribute('id', 'ext-inspector-config-panel')
      await expect(extension.locator('#ext-inspector-details-panel')).toHaveAttribute('hidden', '')
      await configTab.press('ArrowLeft')
      await expect(detailsTab).toBeFocused()
      await expect(detailsTab).toHaveAttribute('aria-selected', 'true')
      const panelField = page.getByTestId('ext-field-note')
      const panelAction = page.getByTestId('ext-action-ack')
      await expect(panelField).toBeVisible()
      await expect(panelAction).toBeVisible()
      await expectTouchTarget(profile, panelField, `${profile.name} extension field`)
      await expectTouchTarget(profile, panelAction, `${profile.name} extension action`)
      await panelField.scrollIntoViewIfNeeded()
      await expectInsideViewport(page, panelField, `${profile.name} extension detail field`)
      await panelAction.scrollIntoViewIfNeeded()
      await expectInsideViewport(page, panelAction, `${profile.name} extension detail action`)
      await expectInsideViewport(page, page.locator('.ext-inspector-head'), `${profile.name} extension header`)
      await expectNoPageOverflow(page, `${profile.name} extension details`)
      await expectTouchButtons(profile, extension, `${profile.name} extension details`)

      await configTab.click()
      const config = page.getByTestId(`extension-config-${fixtureName}`)
      await expectInsideViewport(page, config, `${profile.name} extension config`)
      const configSave = page.getByTestId('extension-config-save')
      await configSave.scrollIntoViewIfNeeded()
      await expectInsideViewport(page, configSave, `${profile.name} extension config footer action`)
      await expectInsideViewport(page, page.locator('.ext-inspector-head'), `${profile.name} extension config header`)
      await expectNoPageOverflow(page, `${profile.name} extension config`)
      await expectTouchButtons(profile, extension, `${profile.name} extension config`)
      await extension.getByRole('button', { name: /关闭对话框|Close dialog/ }).click()

      await openDrawer(page, profile.compact)
      const sessionMenuTrigger = page.locator('button[aria-label="会话菜单"], button[aria-label="Session menu"]').first()
      await expect(sessionMenuTrigger).toBeVisible()
      await sessionMenuTrigger.click()
      const popupMenu = page.getByTestId('pop-menu')
      await expectInsideViewport(page, popupMenu, `${profile.name} session popup menu`)
      await expect(popupMenu).toHaveAttribute('role', 'menu')
      await expect(sessionMenuTrigger).toHaveAttribute('aria-expanded', 'true')
      await expectTouchButtons(profile, popupMenu, `${profile.name} session popup menu`)
      if (profile.compact) {
        const layers = await page.evaluate(() => ({
          menu: Number(getComputedStyle(document.querySelector<HTMLElement>('.menu-mask')!).zIndex),
          drawer: Number(getComputedStyle(document.querySelector<HTMLElement>('aside.sidebar')!).zIndex),
        }))
        expect(layers.menu, `${profile.name} popup should be above the drawer`).toBeGreaterThan(layers.drawer)
      }
      await page.keyboard.press('Escape')
      await expect(popupMenu).toHaveCount(0)
      await expect(sessionMenuTrigger).toBeFocused()
      await closeDrawer(page, profile.compact)

      const treeFixture = await createTreeFixture(request)
      await page.reload()
      await openDrawer(page, profile.compact)
      const treeRoot = page.getByTestId('session-title').filter({ hasText: treeFixture.rootTitle })
      await expect(treeRoot).toBeVisible()
      await treeRoot.click()
      await page.getByTestId('tab-config').click()
      await expect(page.getByTestId('info-tree')).toBeVisible()
      await page.getByTestId('info-tree').click()
      const tree = page.getByTestId('session-tree-browser')
      await expectDialogChrome(page, tree, `${profile.name} session-tree dialog`, '.dir-head', '.dir-foot')
      await expect(tree.getByTestId('session-tree-row').filter({ hasText: treeFixture.childTitle })).toBeVisible()
      await expectTouchButtons(profile, tree, `${profile.name} session-tree dialog`)
      await page.keyboard.press('Escape')
      await expect(tree).toHaveCount(0)
    })
  })
}
