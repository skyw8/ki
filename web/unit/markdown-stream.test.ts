import { expect, test } from 'bun:test'
import { MarkdownBlockSplitter, splitSettled } from '../src/features/markdown/streamText.ts'

test('live block splitting scans each appended character once and preserves every code unit', () => {
  const splitter = new MarkdownBlockSplitter()
  let text = ''
  for (const chunk of ['one', '\n', '\n', '\n', '文🙂 **two', '**\n\n', 'three']) {
    text += chunk
    expect(splitter.split(text).join('')).toBe(text)
  }
  expect(splitter.split(text)).toEqual(['one\n\n\n', '文🙂 **two**\n\n', 'three'])
  expect(splitter.scannedCharacters).toBe(text.length)
  expect(splitter.split('rewritten\n\nbody')).toEqual(['rewritten\n\n', 'body'])
  expect(splitter.split('')).toEqual([])
})

test('live splitting keeps semantic blocks and references together', () => {
  for (const text of [
    '```js\ncode\n\nmore\n```',
    '1. a\n\n1. b\n\n',
    '- a\n\n- b\n\n',
    '> a\n\n> b\n\n',
    '    a\n\n    b\n',
    '<pre>\n\nstill inside\n\n',
    '<!-- comment\n\nstill inside\n\n',
    '[link][ref]\n\nsecond paragraph\n\n[ref]: /target',
  ]) {
    const splitter = new MarkdownBlockSplitter()
    for (let i = 1; i <= text.length; i++) expect(splitter.split(text.slice(0, i)).join('')).toBe(text.slice(0, i))
    expect(splitter.split(text)).toEqual([text])
  }
})

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

