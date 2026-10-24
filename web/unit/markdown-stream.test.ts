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
  expect(splitSettled('one\n\ntwo\n\nthree')).toEqual({ sealed: 'one\n\ntwo\n\n', live: 'three' })
  expect(splitSettled('one\ntwo')).toEqual({ sealed: '', live: 'one\ntwo' })
  expect(splitSettled('```\ncode\n\nmore\n```\n\nafter')).toEqual({ sealed: '```\ncode\n\nmore\n```\n\n', live: 'after' })
  expect(splitSettled('```\ncode\n\nmore\n\nstill')).toEqual({ sealed: '', live: '```\ncode\n\nmore\n\nstill' })
  expect(splitSettled('1. a\n\n1. b\n\n')).toEqual({ sealed: '', live: '1. a\n\n1. b\n\n' })
  expect(splitSettled('1. a\n\n1. b\n\nprose\n\n')).toEqual({ sealed: '1. a\n\n1. b\n\nprose\n\n', live: '' })
  expect(splitSettled('- a\n\n- b\n\n')).toEqual({ sealed: '', live: '- a\n\n- b\n\n' })
  expect(splitSettled('> a\n\n> still quoted\n\n')).toEqual({ sealed: '', live: '> a\n\n> still quoted\n\n' })
  expect(splitSettled('> a\n\nprose\n\n')).toEqual({ sealed: '> a\n\nprose\n\n', live: '' })
  expect(splitSettled('    a\n\n    b\n')).toEqual({ sealed: '', live: '    a\n\n    b\n' })
  expect(splitSettled('    a\n\nprose\n\n')).toEqual({ sealed: '    a\n\nprose\n\n', live: '' })
  expect(splitSettled('<div>\n\ntext\n\n')).toEqual({ sealed: '<div>\n\ntext\n\n', live: '' })
  expect(splitSettled('<pre>\n\ncode\n\nmore\n\n')).toEqual({ sealed: '', live: '<pre>\n\ncode\n\nmore\n\n' })
  expect(splitSettled('<!-- note\n\ntext\n\n')).toEqual({ sealed: '', live: '<!-- note\n\ntext\n\n' })
  expect(splitSettled('<pre>a</pre>\n\ntext\n\n')).toEqual({ sealed: '<pre>a</pre>\n\ntext\n\n', live: '' })
  expect(splitSettled('# title\n\nbody\n\n')).toEqual({ sealed: '# title\n\nbody\n\n', live: '' })
  expect(splitSettled('| a | b |\n| --- | --- |\n| 1 | 2 |\n\nnext')).toEqual({
    sealed: '| a | b |\n| --- | --- |\n| 1 | 2 |\n\n',
    live: 'next',
  })
  for (const text of ['a\n\nb', '```\nx\n```\n\ny', '- one\n\n- two\n\nthree', '']) {
    const { sealed, live } = splitSettled(text)
    expect(sealed + live).toBe(text)
  }
})
