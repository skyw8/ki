import { expect, type Locator, type Page } from '@playwright/test'

export function contextCategory(page: Page, category: string): Locator {
  return page.locator(`.context-category[data-category="${category}"]`)
}

export async function openContextCategory(page: Page, category: string): Promise<Locator> {
  const section = contextCategory(page, category)
  await expect(section).toBeVisible()
  if (await section.getAttribute('open') === null) await section.locator(':scope > summary').click()
  return section
}

export async function openContextItem(item: Locator): Promise<void> {
  if (await item.getAttribute('open') === null) await item.locator(':scope > summary').click()
}

export async function openSystemSources(page: Page, kind: string): Promise<Locator> {
  const sources = page.locator(`.context-system-source[data-source-kind="${kind}"]`)
  await expect(sources.first()).toBeVisible()
  for (const source of await sources.all()) await openContextItem(source)
  return sources
}

export async function expectContextItemCount(page: Page, category: string, count: number): Promise<void> {
  const section = await openContextCategory(page, category)
  // Closed native details mount no items. The model's count must agree too,
  // otherwise a historical exclusion could pass before the body renders.
  await expect(section.locator('.context-category-count')).toHaveText(new RegExp(`^${count} (项|items)$`))
  await expect(section.locator('.context-item')).toHaveCount(count)
}

export async function expectColoredContextSegments(page: Page, requestId: string, categories: string[]): Promise<void> {
  // Scope by the request identity itself: other points may have full bodies
  // already, which must not hide a blank metadata-only historical column.
  const column = page.locator(`[data-testid="context-step"][data-request-id="${requestId}"]`)
  await expect(column).toBeVisible()
  const colors: string[] = []
  for (const category of categories) {
    const segment = column.locator(`[data-testid="context-category-segment"][data-category="${category}"]`)
    await expect(segment).toHaveCount(1)
    await expect(segment).toBeVisible()
    await expect.poll(async () => Number(await segment.getAttribute('data-tokens')),
      { message: `${category} must retain its full-body estimate without downloading history` }).toBeGreaterThan(0)
    await expect.poll(async () => (await segment.boundingBox())?.height ?? 0,
      { message: `${category} must be a visible colored segment, not a hairline under the reported-input scale` }).toBeGreaterThan(1)
    const color = await segment.evaluate(el => getComputedStyle(el).backgroundColor)
    const channels = color.match(/[\d.]+/g)?.slice(0, 3).map(Number) ?? []
    expect(channels).toHaveLength(3)
    expect(Math.max(...channels) - Math.min(...channels),
      `${category} must be colored, not an opaque gray fill with a category label`).toBeGreaterThan(12)
    colors.push(color)
  }
  expect(new Set(colors).size, 'categories must use distinct visible colors').toBe(categories.length)
}

export async function expectConversationPosition(page: Page, entryId: string): Promise<void> {
  const chat = page.getByTestId('chat')
  await expect(chat).toHaveAttribute('data-anchor-key', entryId)
  const row = page.locator(`[data-item-key="${entryId}"]`)
  await expect(row).toBeVisible()
  // A mounted virtual row or a visible chat tab does not prove navigation:
  // the bug left the transcript at the tail with the requested ID pending.
  await expect.poll(async () => {
    const target = await row.boundingBox()
    const viewport = await page.getByTestId('chat-scroll').boundingBox()
    return !!target && !!viewport && target.y >= viewport.y - 2
      && target.y + target.height <= viewport.y + viewport.height + 2
  }, { message: `conversation entry ${entryId} must land inside the reading viewport` }).toBe(true)
  await expect.poll(() => page.getByTestId('chat-scroll').evaluate(el =>
    el.scrollHeight - el.clientHeight - el.scrollTop
  ), { message: 'an early event must not leave conversation at the tail' }).toBeGreaterThan(500)
}
