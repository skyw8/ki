import { expect, test } from 'bun:test'
import { normalizeMathDelimiters } from '../src/features/markdown/math-delimiters.ts'

test('latex delimiters become dollar math', () => {
  expect(normalizeMathDelimiters('令 \\(q_t\\) 为分量')).toBe('令 $$q_t$$ 为分量')
  expect(normalizeMathDelimiters('\\[\nL=\\frac{2}{\\beta}\n\\]')).toBe('$$\nL=\\frac{2}{\\beta}\n$$')
})

test('code fences and inline code keep latex untouched', () => {
  expect(normalizeMathDelimiters('```tex\n\\(q_t\\)\n```')).toBe('```tex\n\\(q_t\\)\n```')
  expect(normalizeMathDelimiters('`\\(q_t\\)`')).toBe('`\\(q_t\\)`')
  expect(normalizeMathDelimiters('~~~\n\\[x\\]\n~~~')).toBe('~~~\n\\[x\\]\n~~~')
})

test('single dollars and unclosed delimiters stay literal', () => {
  expect(normalizeMathDelimiters('costs $5 and $g_F$ stays')).toBe('costs $5 and $g_F$ stays')
  expect(normalizeMathDelimiters('broken \\(q_t here')).toBe('broken \\(q_t here')
  expect(normalizeMathDelimiters('closing only \\)')).toBe('closing only \\)')
})

test('adjacent delimiters convert without swallowing', () => {
  expect(normalizeMathDelimiters('\\(a\\)\\(b\\)')).toBe('$$a$$ $$b$$')
  expect(normalizeMathDelimiters('\\(f(x)=[1,2]\\)')).toBe('$$f(x)=[1,2]$$')
})

test('text without delimiters is returned as-is', () => {
  const text = 'plain markdown with `code` and a table'
  expect(normalizeMathDelimiters(text)).toBe(text)
})
