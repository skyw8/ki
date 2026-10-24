import { expect, test } from 'bun:test'
import { classifyMarkdownHref, isAbsoluteLocalPath, isMarkdownImagePath, normalizeLocalPath, resolveLocalPath, splitLocation } from '../src/lib/localPath.ts'

test('local paths resolve against the session cwd and keep host-absolute paths intact', () => {
  expect(resolveLocalPath('docs/a.md', '/repo')).toBe('/repo/docs/a.md')
  expect(resolveLocalPath('./docs/a.md', '/repo')).toBe('/repo/docs/a.md')
  expect(resolveLocalPath('../b.md', '/repo/app')).toBe('/repo/b.md')
  expect(resolveLocalPath('/abs/x.md', '/repo')).toBe('/abs/x.md')
  expect(resolveLocalPath('docs/./a/../b.md', '/repo')).toBe('/repo/docs/b.md')
  expect(resolveLocalPath('docs\\a.md', 'C:\\repo')).toBe('C:/repo/docs/a.md')
  expect(resolveLocalPath('C:\\repo\\a.md', '/ignored')).toBe('C:/repo/a.md')
  expect(resolveLocalPath('docs/a.md', '')).toBe('docs/a.md')
  expect(isAbsoluteLocalPath('/a')).toBe(true)
  expect(isAbsoluteLocalPath('C:\\a')).toBe(true)
  expect(isAbsoluteLocalPath('docs/a')).toBe(false)
  expect(normalizeLocalPath('/a//b/')).toBe('/a/b')
})

test('line and column suffixes stay separate from the file name', () => {
  expect(splitLocation('/repo/app.py:12')).toEqual({ path: '/repo/app.py', line: 12, column: undefined })
  expect(splitLocation('/repo/app.py:12:3')).toEqual({ path: '/repo/app.py', line: 12, column: 3 })
  expect(splitLocation('C:/repo/app.py')).toEqual({ path: 'C:/repo/app.py' })
  expect(splitLocation('/repo/a:b')).toEqual({ path: '/repo/a:b' })
  expect(splitLocation('/repo/plain.md')).toEqual({ path: '/repo/plain.md' })
})

test('href classification separates external, anchor, local, and rejected targets', () => {
  expect(classifyMarkdownHref('https://example.com/x', '/repo')).toEqual({ kind: 'external', url: 'https://example.com/x' })
  expect(classifyMarkdownHref('mailto:a@b.c', '/repo')).toEqual({ kind: 'external', url: 'mailto:a@b.c' })
  expect(classifyMarkdownHref('#section', '/repo')).toEqual({ kind: 'anchor', hash: '#section' })
  expect(classifyMarkdownHref('docs/a.md', '/repo')).toEqual({ kind: 'local', path: '/repo/docs/a.md', line: undefined, column: undefined })
  expect(classifyMarkdownHref('./docs/a.md#42-x', '/repo')).toEqual({ kind: 'local', path: '/repo/docs/a.md', line: undefined, column: undefined })
  expect(classifyMarkdownHref('app.py:12', '/repo')).toEqual({ kind: 'local', path: '/repo/app.py', line: 12, column: undefined })
  expect(classifyMarkdownHref('docs/%E4%B8%AD.md', '/repo')).toEqual({ kind: 'local', path: '/repo/docs/中.md', line: undefined, column: undefined })
  expect(classifyMarkdownHref('C:/repo/a.md', '/repo')).toEqual({ kind: 'local', path: 'C:/repo/a.md', line: undefined, column: undefined })
  expect(classifyMarkdownHref('javascript:alert(1)', '/repo')).toEqual({ kind: 'invalid' })
  expect(classifyMarkdownHref('file:///etc/passwd', '/repo')).toEqual({ kind: 'invalid' })
  expect(classifyMarkdownHref('', '/repo')).toEqual({ kind: 'invalid' })
})

test('image detection only covers common raster and svg extensions', () => {
  expect(isMarkdownImagePath('/a/b.svg')).toBe(true)
  expect(isMarkdownImagePath('/a/b.PNG')).toBe(true)
  expect(isMarkdownImagePath('/a/b.md')).toBe(false)
  expect(isMarkdownImagePath('/a/b')).toBe(false)
})
