import { expect, type Page } from '@playwright/test'

// newSession clicks the sidebar's new-session button and waits for the created
// session to become current.
//
// Why: the sidebar only switches once the create call returns, so a prompt sent
// right after the click can run in the previous session — the view then shows a
// different session than the one holding the run, and assertions about that run
// (stop button, streaming) never see it.
export async function newSession(page: Page): Promise<void> {
  const rows = page.locator('.session-row')
  const before = await rows.count()
  await page.getByTestId('new-session').click()
  await expect(rows).toHaveCount(before + 1)
  await expect(page.locator('.session-row.active')).toHaveCount(1)
}
