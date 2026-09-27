import { expect, test, type Page } from '@playwright/test'
import { splitSettled } from '../src/features/markdown/streamText.ts'

/**
 * Streaming render budget.
 *
 * Streamdown re-lexes its whole text into markdown blocks whenever that text
 * changes, and the block cache is off while streaming (every delta is a new
 * text), so a reply used to cost more per delta the longer it got. `streamText.ts`
 * splits the text into a settled prefix (sealed into segments that are parsed
 * once, ever) and a moving tail (parsed while it is short, painted as source
 * while it is long). These tests pin that split, the rendering it drives, and the
 * fact that the text still moves while it streams.
 */

test('splitSettled settles whole blocks and leaves the open tail alone', () => {
  // A blank line closes a paragraph for good.
  expect(splitSettled('one\n\ntwo\n\nthree')).toEqual({ sealed: 'one\n\ntwo\n\n', live: 'three' })
  // No blank line yet: nothing is settled.
  expect(splitSettled('one\ntwo')).toEqual({ sealed: '', live: 'one\ntwo' })
  // A fence swallows blank lines until it closes.
  expect(splitSettled('```\ncode\n\nmore\n```\n\nafter')).toEqual({ sealed: '```\ncode\n\nmore\n```\n\n', live: 'after' })
  expect(splitSettled('```\ncode\n\nmore\n\nstill')).toEqual({ sealed: '', live: '```\ncode\n\nmore\n\nstill' })
  // An ordered list can be continued by the next line, so it stays live until its
  // list ends (two <ol>s would restart the numbering).
  expect(splitSettled('1. a\n\n1. b\n\n')).toEqual({ sealed: '', live: '1. a\n\n1. b\n\n' })
  expect(splitSettled('1. a\n\n1. b\n\nprose\n\n')).toEqual({ sealed: '1. a\n\n1. b\n\nprose\n\n', live: '' })
  // Blank lines inside lists affect tightness and nested blocks. Keep the whole
  // list mutable until the following block proves it has ended.
  expect(splitSettled('- a\n\n- b\n\n')).toEqual({ sealed: '', live: '- a\n\n- b\n\n' })
  // A blockquote can be continued by a `>` line after a blank line, so it stays
  // live until a block that cannot join it ends it.
  expect(splitSettled('> a\n\n> still quoted\n\n')).toEqual({ sealed: '', live: '> a\n\n> still quoted\n\n' })
  expect(splitSettled('> a\n\nprose\n\n')).toEqual({ sealed: '> a\n\nprose\n\n', live: '' })
  // Indented code blocks span blank lines when the next line stays indented.
  expect(splitSettled('    a\n\n    b\n')).toEqual({ sealed: '', live: '    a\n\n    b\n' })
  expect(splitSettled('    a\n\nprose\n\n')).toEqual({ sealed: '    a\n\nprose\n\n', live: '' })
  // Most HTML blocks end at a blank line, like any other block...
  expect(splitSettled('<div>\n\ntext\n\n')).toEqual({ sealed: '<div>\n\ntext\n\n', live: '' })
  // ...but <pre>, <script>, <style>, <textarea> and comments run to their own
  // terminator, so the rest of the message may still be inside them: nothing after
  // such a block is settled either.
  expect(splitSettled('<pre>\n\ncode\n\nmore\n\n')).toEqual({ sealed: '', live: '<pre>\n\ncode\n\nmore\n\n' })
  expect(splitSettled('<!-- note\n\ntext\n\n')).toEqual({ sealed: '', live: '<!-- note\n\ntext\n\n' })
  // A terminated one is an ordinary block again.
  expect(splitSettled('<pre>a</pre>\n\ntext\n\n')).toEqual({ sealed: '<pre>a</pre>\n\ntext\n\n', live: '' })
  // A heading, a thematic break and a table all close at a blank line.
  expect(splitSettled('# title\n\nbody\n\n')).toEqual({ sealed: '# title\n\nbody\n\n', live: '' })
  expect(splitSettled('| a | b |\n| --- | --- |\n| 1 | 2 |\n\nnext')).toEqual({
    sealed: '| a | b |\n| --- | --- |\n| 1 | 2 |\n\n',
    live: 'next',
  })
  // The sealed part is always a prefix of the live text.
  for (const text of ['a\n\nb', '```\nx\n```\n\ny', '- one\n\n- two\n\nthree', '']) {
    const { sealed, live } = splitSettled(text)
    expect(sealed + live).toBe(text)
  }
})

async function sendPrompt(page: Page, text: string) {
  const input = page.getByTestId('composer-input')
  await expect(input).toBeEnabled()
  await input.fill(text)
  await page.getByTestId('composer-send').click()
}

/** Renders the composer's text input and reports what the stream looks like. */
function probe(page: Page) {
  return page.evaluate(() => {
    const msg = document.querySelector('[data-testid="assistant-message"]') as HTMLElement | null
    const tail = document.querySelector('[data-md-tail]') as HTMLElement | null
    return {
      len: msg?.textContent?.length ?? -1,
      segs: document.querySelectorAll('.md-seg').length,
      tail: tail ? tail.textContent?.length ?? 0 : 0,
    }
  })
}

test('keeps the text moving while it streams', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, 'e2e-blocks-300')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  // Deltas arrive every few milliseconds. A render limiter that is reset on every
  // delta is a debounce and never fires, which froze the text for the whole stream
  // (measured: the bubble jumped from 1.2 KiB to 55 KiB in one step at the end).
  const lengths = new Set<number>()
  for (let i = 0; i < 60; i++) {
    lengths.add((await probe(page)).len)
    await page.waitForTimeout(100)
  }
  const growing = [...lengths].filter(n => n > 0).sort((a, b) => a - b)
  expect(growing.length, `rendered lengths: ${growing.join(',')}`).toBeGreaterThan(4)
  // Nothing renders before the first delta, and the last one is the whole text.
  expect(growing.at(-1), 'final length').toBeGreaterThan(30_000)
})

test('stopping generation retains already visible text and its mounted root', async ({ page }) => {
  await page.goto('/')
  await sendPrompt(page, 'e2e-blocks-400')
  await expect(page.getByTestId('assistant-message')).toContainText('Paragraph 20')
  await page.evaluate(() => { (window as unknown as { stoppedRoot: Element | null }).stoppedRoot = document.querySelector('[data-stream-seq]') })
  await page.getByTestId('composer-stop').click()
  await expect(page.getByTestId('composer-stop')).toHaveCount(0)
  await expect(page.getByTestId('assistant-message')).toContainText('Paragraph 20')
  expect(await page.evaluate(() => (window as unknown as { stoppedRoot: Element }).stoppedRoot.isConnected)).toBe(true)
})

test('paragraph-shaped streams parse each block once, not per delta', async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, 'e2e-blocks-400')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  // ~54 KiB of paragraphs. Mid-stream the settled part is already sealed into
  // segments that are never re-parsed; a long moving block would be painted.
  await expect(page.getByTestId('assistant-message').first()).toContainText('Paragraph 120', { timeout: 60_000 })
  const mid = await page.evaluate(() => {
    const nodes = [...document.querySelectorAll('[data-testid="assistant-message"] .md')] as HTMLElement[]
    return nodes.map(n => n.textContent?.length ?? 0).sort((a, b) => b - a)
  })
  console.log(`paragraph pieces mid-stream (longest first): ${mid.slice(0, 8).join(',')} of ${mid.length}`)
  expect(mid.length, 'sealed segments mid-stream').toBeGreaterThan(2)
  // No piece ever holds the growing whole, so no render re-lexes more than one
  // sealed chunk plus the open block — that is the whole point of the split.
  expect(Math.max(...mid), `longest piece mid-stream: ${Math.max(...mid)}`).toBeLessThan(12_000)
  // The whole message lands without replacing the stream's mounted segments.
  await expect(page.getByTestId('assistant-message').first()).toContainText('Paragraph 399', { timeout: 60_000 })
  await expect(page.getByTestId('assistant-message').first().locator('p')).toHaveCount(400)
  await expect(page.locator('[data-md-tail]')).toHaveCount(0)
  // Splitting at blank lines must not duplicate or drop text.
  const text = (await page.getByTestId('assistant-message').first().textContent()) ?? ''
  expect(text.match(/Paragraph \d+:/g)?.length).toBe(400)
})

test('an unbroken block is painted until it closes, then parsed', async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto('/')
  await expect(page.getByTestId('hero')).toBeVisible()
  await sendPrompt(page, 'e2e-stream-200')
  await expect(page.getByTestId('chat-scroll')).toBeVisible()
  // One ~26 KiB list block with no blank line: nothing can settle while it grows,
  // so past the tail limit it is painted as source rather than re-parsed...
  await expect(page.getByTestId('assistant-message').first()).toContainText('item 60', { timeout: 60_000 })
  await expect(page.locator('[data-md-tail]')).toHaveCount(1)
  await expect(page.locator('[data-md-tail]')).toContainText('- item 60')
  // ...and it is parsed once when the run ends.
  await expect(page.getByTestId('assistant-message').first()).toContainText('item 199', { timeout: 60_000 })
  await expect(page.locator('[data-md-tail]')).toHaveCount(0)
  await expect(page.getByTestId('assistant-message').first().locator('li')).toHaveCount(200)
  const text = (await page.getByTestId('assistant-message').first().textContent()) ?? ''
  expect(text.match(/item \d+ /g)?.length).toBe(200)
})
