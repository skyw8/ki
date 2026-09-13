import { expect, test } from '@playwright/test'
import { ancestorsOf, buildSessionForest, orderedChildren, topLevelRoot } from '../src/lib/session-tree.ts'
import type { SessionInfo, WorkspaceInfo } from '../src/api/types.ts'
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

test('session forest nests deep subagent children', () => {
  const root = session('root')
  const a = session('a', { parentSessionId: root.id, forkMode: 'tree' })
  const b = session('b', { parentSessionId: a.id, forkMode: 'tree' })
  const sibling = session('sibling', { parentSessionId: root.id, forkMode: 'tree' })
  const forest = buildSessionForest([root, a, b, sibling], [workspace])

  expect(orderedChildren(forest, root.id).map(item => item.id)).toEqual(['a', 'sibling'])
  expect(orderedChildren(forest, a.id).map(item => item.id)).toEqual(['b'])
  expect(topLevelRoot(forest, b.id)).toBe(root.id)
  expect(ancestorsOf(forest, b.id)).toEqual(['a', 'root'])
  expect(ancestorsOf(forest, root.id)).toEqual([])
})

test('session forest isolates orphan, cross-workspace, and cyclic edges', () => {
  const root = session('root')
  const orphan = session('orphan', { parentSessionId: 'missing', forkMode: 'tree' })
  const cross = session('cross', { parentSessionId: root.id, forkMode: 'tree', workspaceId: 'other' })
  const cycleA = session('cycle-a', { parentSessionId: 'cycle-b', forkMode: 'tree' })
  const cycleB = session('cycle-b', { parentSessionId: 'cycle-a', forkMode: 'tree' })
  const forest = buildSessionForest([root, orphan, cross, cycleA, cycleB], [workspace])

  expect(forest.unresolved.has(orphan.id)).toBe(true)
  expect(forest.unresolved.has(cross.id)).toBe(true)
  expect(forest.unresolved.has(cycleA.id)).toBe(true)
  expect(forest.unresolved.has(cycleB.id)).toBe(true)
  expect(orderedChildren(forest, root.id)).toEqual([])
  // Broken edges fall back to top-level rows instead of disappearing.
  expect(topLevelRoot(forest, orphan.id)).toBe(orphan.id)
})

test('sidebar expands subagent children by arrow, opens by row', async ({ page, request }) => {
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

  const stamp = Date.now()
  const rootTitle = `tree-root-${stamp}`
  const childATitle = `tree-a-${stamp}`
  const childBTitle = `tree-b-${stamp}`
  const root = await create(rootTitle)
  const childA = await create(childATitle, root)
  await create(childBTitle, childA)

  await page.reload()
  const rowFor = (title: string) => page.locator('[data-testid="session-row"]').filter({ hasText: title })
  const rootRow = rowFor(rootTitle)

  // Subagent children start collapsed and only appear under an expanded branch.
  await expect(rootRow).toBeVisible()
  await expect(page.getByTestId('session-title').filter({ hasText: childATitle })).toHaveCount(0)
  await expect(rootRow.getByTestId('session-toggle')).toHaveAttribute('aria-expanded', 'false')

  // The arrow expands without opening the session.
  await rootRow.getByTestId('session-toggle').click()
  await expect(rootRow.getByTestId('session-toggle')).toHaveAttribute('aria-expanded', 'true')
  await expect(rootRow.locator('.session-main')).not.toHaveAttribute('aria-current', 'page')
  const childARow = rowFor(childATitle)
  await expect(childARow).toBeVisible()

  // Multi-level: expand the child to reach the grandchild.
  await childARow.getByTestId('session-toggle').click()
  const childBRow = rowFor(childBTitle)
  await expect(childBRow).toBeVisible()
  await expect(childBRow).toHaveAttribute('data-depth', '2')
  // Children follow their parent's branch; they are not reorderable.
  await expect(childBRow).toHaveAttribute('draggable', 'false')

  // The row opens the session (and pins the conversation tab).
  await page.getByTestId('tab-config').click()
  await childBRow.locator('.session-main').click()
  await expect(page.getByTestId('tab-conversation')).toHaveClass(/active/)
  await expect(rowFor(childBTitle).locator('.session-main')).toHaveAttribute('aria-current', 'page')

  // Subagent children never offer pin.
  await childBRow.locator('button[aria-label="会话菜单"]').click()
  await expect(page.getByTestId('pop-menu').getByRole('menuitem', { name: '置顶' })).toHaveCount(0)
  await page.keyboard.press('Escape')

  // Collapsing the branch hides its descendants again.
  await rootRow.getByTestId('session-toggle').click()
  await expect(page.getByTestId('session-title').filter({ hasText: childATitle })).toHaveCount(0)
  await expect(page.getByTestId('session-title').filter({ hasText: childBTitle })).toHaveCount(0)

  // Expansion is App-lifetime navigation state, not a durable preference.
  await page.reload()
  await expect(rowFor(rootTitle)).toBeVisible()
  await expect(page.getByTestId('session-title').filter({ hasText: childATitle })).toHaveCount(0)

  // The Info parent row links back to the parent session.
  await rowFor(rootTitle).getByTestId('session-toggle').click()
  await rowFor(childATitle).locator('.session-main').click()
  await page.getByTestId('tab-config').click()
  await page.getByTestId('cfg-parent').click()
  await expect(rowFor(rootTitle).locator('.session-main')).toHaveAttribute('aria-current', 'page')
})
