import { expect, type Page } from '@playwright/test'

// Workspaces start collapsed, so a session row does not exist in the DOM until
// its group is open. Tests that only care about the session (transcript, tools,
// settings) use these helpers instead of depending on the default state; the
// default itself is asserted in sidebar-expand.spec.ts.
//
// Why click the toggles rather than expand every group through app state: the
// toggle is the user's own path, so a helper that goes through it fails the same
// way a user would if the row became unreachable.

// openSidebar makes the workspace list usable on a compact layout. The toggle is
// mounted by the conversation shell, not by the auth bootstrap, so it can appear
// a beat after `main`; retrying avoids a click on a node that a re-render
// detached (which surfaces as "waiting for mobile-nav-toggle" for 30s).
async function openSidebar(page: Page): Promise<void> {
  const nav = page.getByTestId('mobile-nav-toggle')
  for (let attempt = 0; attempt < 40; attempt++) {
    if (!(await nav.count())) return
    if ((await nav.getAttribute('aria-expanded')) === 'true') return
    try {
      await nav.click({ timeout: 2_000 })
    } catch {
      // The shell re-rendered between the lookup and the click; look again.
    }
  }
  throw new Error('mobile sidebar did not open')
}

export async function expandWorkspaces(page: Page): Promise<void> {
  await expect(page.locator('main.main')).toBeVisible()
  await openSidebar(page)
  const collapsed = page.locator('.ws-toggle[aria-expanded="false"]')
  // Bounded: each click removes one collapsed toggle, so this terminates even
  // if a group appears late. The bound only guards against a toggle that
  // ignores its click.
  for (let guard = 0; guard < 64; guard++) {
    const count = await collapsed.count()
    if (!count) return
    await collapsed.first().click()
    await expect(collapsed).toHaveCount(count - 1)
  }
  throw new Error('workspace toggles stayed collapsed after repeated clicks')
}

// openSessionRow opens the sidebar row for `title` (or the first row when no
// title is given) after making the row reachable.
export async function openSessionRow(page: Page, title?: string): Promise<void> {
  await expandWorkspaces(page)
  const rows = page.getByTestId('session-row')
  const row = title === undefined ? rows.first() : rows.filter({ hasText: title }).first()
  await expect(row).toBeVisible()
  await row.locator('.session-main').click()
}
