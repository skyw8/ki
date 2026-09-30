import { expect, test } from 'bun:test'
import { loadHistory } from '../src/lib/model'

const original = (text: string, n: number) => {
  const normalized = text.replace(/\s+/g, ' ').trim()
  return normalized.length > n ? normalized.slice(0, n) + '…' : normalized
}
function preview(text: string) {
  const view = loadHistory({ id: 'preview', entries: [{ type: 'message', id: 'u', message: { role: 'user', content: [{ type: 'text', text }] } }] })
  return { record: view.records[0].preview, title: view.title }
}

test('bounded previews retain replace/trim whitespace and UTF-16 boundary semantics', () => {
  const whitespace = [' ', '\t', '\n', '\r', '\v', '\f', '\u00a0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff']
  const texts = ['', whitespace.join(''), 'a'.repeat(160), 'a'.repeat(160) + whitespace.join(''), 'a'.repeat(160) + '\nnext', 'a'.repeat(159) + '\nnext', 'a'.repeat(79) + '🙂', 'a'.repeat(159) + '🙂', '\u0085\u180e\u200bbody', 'prefix '.repeat(40) + 'suffix'.repeat(200_000)]
  for (const space of whitespace) texts.push(`${space} leading${space}${space}inner ${'文🙂e\u0301'.repeat(40)} trailing${space}`)
  let seed = 71
  const alphabet = ['a', '文', '🙂', '\u200b', ...whitespace]
  for (let trial = 0; trial < 100; trial++) {
    let text = ''
    for (let i = 0; i < 400; i++) { seed = (seed * 1664525 + 1013904223) >>> 0; text += alphabet[seed % alphabet.length] }
    texts.push(text)
  }
  for (const text of texts) {
    const result = preview(text)
    expect(result.record).toBe(original(text, 160))
    expect(result.title).toBe(original(text, 80))
  }
})
