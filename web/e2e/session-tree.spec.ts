import { expect, test } from '@playwright/test'
import { buildSessionTree, orderedChildren } from '../src/session-tree.ts'
import type { SessionInfo, WorkspaceInfo } from '../src/types.ts'
import { serverToken } from './global-setup.ts'

function session(id: string, over: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id,
    cwd: '/project',
    provider: 'openai',
    model: 'gpt',
    title: id,
    workspaceId: 'ws',
    forkMode: 'flat',
    ...over,
  }
}

const workspace: WorkspaceInfo = { id: 'ws', path: '/project', title: 'Project', sessionIds: ['root', 'a', 'b', 'sibling'] }

test('session tree keeps deep paths in a two-direction graph', () => {
  const root = session('root')
  const a = session('a', { parentSessionId: root.id, forkMode: 'tree' })
  const b = session('b', { parentSessionId: a.id, forkMode: 'tree' })
  const sibling = session('sibling', { parentSessionId: root.id, forkMode: 'tree' })
  const model = buildSessionTree([root, a, b, sibling], [workspace], b.id)

  expect(model?.root.id).toBe(root.id)
  expect(model?.path.map(item => item.id)).toEqual(['root', 'a', 'b'])
  expect(orderedChildren(model!, root.id).map(item => item.id)).toEqual(['a', 'sibling'])
  expect(orderedChildren(model!, a.id).map(item => item.id)).toEqual(['b'])
})

test('session tree isolates orphan, cross-workspace, and cyclic edges', () => {
  const root = session('root')
  const orphan = session('orphan', { parentSessionId: 'missing', forkMode: 'tree' })
  const cross = session('cross', { parentSessionId: root.id, forkMode: 'tree', workspaceId: 'other' })
  const cycleA = session('cycle-a', { parentSessionId: 'cycle-b', forkMode: 'tree' })
  const cycleB = session('cycle-b', { parentSessionId: 'cycle-a', forkMode: 'tree' })
  const model = buildSessionTree([root, orphan, cross, cycleA, cycleB], [workspace], root.id)

  expect(model?.unresolved.has(orphan.id)).toBe(true)
  expect(model?.unresolved.has(cross.id)).toBe(true)
  expect(model?.unresolved.has(cycleA.id)).toBe(true)
  expect(model?.unresolved.has(cycleB.id)).toBe(true)
  expect(model?.unresolvedRoots.map(item => item.id)).toEqual(['orphan', 'cycle-a', 'cycle-b'])
  expect(orderedChildren(model!, root.id)).toEqual([])
})

test('Tree browser opens deep children and reveals selected sidebar rows', async ({ page, request }) => {
  await page.goto('/')
  const token = serverToken()
  const headers = { Authorization: `Bearer ${token}` }
  const create = async (title: string, parent?: string) => {
    const response = parent
      ? await request.post(`/v1/sessions/${parent}/fork`, { headers, data: { forkMode: 'tree' } })
      : await request.post('/v1/sessions', { headers, data: {} })
    expect(response.ok()).toBe(true)
    const out = await response.json() as { id: string }
    const patched = await request.patch(`/v1/sessions/${out.id}`, { headers, data: { title } })
    expect(patched.ok()).toBe(true)
    return out.id
  }

  const rootTitle = `tree-root-${Date.now()}`
  const childATitle = `tree-a-${Date.now()}`
  const childBTitle = `tree-b-${Date.now()}`
  const root = await create(rootTitle)
  const childA = await create(childATitle, root)
  await create(childBTitle, childA)

  await page.reload()
  await page.getByTestId('session-title').filter({ hasText: rootTitle }).click()
  await page.getByTestId('tab-config').click()
  await page.getByTestId('info-tree').click()
  await expect(page.getByTestId('session-tree-browser')).toBeVisible()
  await page.getByTestId('session-tree-row').filter({ hasText: childATitle }).click()
  await page.locator('.session-tree-col').nth(1).getByTestId('session-tree-row').filter({ hasText: childBTitle }).click()
  await expect(page.locator('.dir-crumb-trail')).toContainText(rootTitle)
  await expect(page.locator('.dir-crumb-trail')).toContainText(childATitle)
  await expect(page.locator('.dir-crumb-trail')).toContainText(childBTitle)
  await page.getByTestId('session-tree-confirm').click()
  await expect(page.getByTestId('tab-conversation')).toHaveClass(/active/)
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(1)
  await expect(page.locator('[data-tree-focus="true"] .session-main')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('session-title').filter({ hasText: childATitle })).toHaveCount(0)
  await expect(page.getByTestId('session-title').filter({ hasText: childBTitle })).toHaveCount(1)

  // The reveal is App-lifetime navigation state: ordinary session changes and
  // search hide it temporarily, but do not discard it.
  await page.getByTestId('session-title').filter({ hasText: rootTitle }).click()
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(1)
  await page.getByTestId('tab-config').click()
  await page.getByTestId('info-tree').click()
  await page.getByTestId('session-tree-row').filter({ hasText: childATitle }).click()
  await page.getByTestId('session-tree-confirm').click()
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(2)
  const childARow = page.locator('[data-tree-focus="true"]').filter({ has: page.locator('.title').filter({ hasText: childATitle }) })
  await childARow.locator('button[aria-label="会话菜单"]').click()
  await expect(page.getByTestId('pop-menu').getByRole('menuitem', { name: '置顶' })).toHaveCount(0)
  await page.keyboard.press('Escape')

  await page.getByTestId('session-title').filter({ hasText: rootTitle }).click()
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(2)
  await page.locator('.search-btn').click()
  await page.getByTestId('session-search').fill(rootTitle)
  await expect(page.getByTestId('search-hit').filter({ hasText: rootTitle })).toHaveCount(1)
  await page.locator('.search-clear').click()
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(2)

  await page.reload()
  await expect(page.getByTestId('session-title').filter({ hasText: rootTitle })).toBeVisible()
  await expect(page.locator('[data-tree-focus="true"]')).toHaveCount(0)
})
