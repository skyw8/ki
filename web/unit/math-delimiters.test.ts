import { expect, test } from 'bun:test'
import { normalizeMathDelimiters } from '../src/features/markdown/math-delimiters.ts'

test('latex delimiters become dollar math', () => {
  expect(normalizeMathDelimiters('令 \\(q_t\\) 为分量')).toBe('令 $$q_t$$ 为分量')
  expect(normalizeMathDelimiters('\\[\nL=\\frac{2}{\\beta}\n\\]')).toBe('$$\nL=\\frac{2}{\\beta}\n$$')
})

test('single dollar inline math becomes paired dollars', () => {
  expect(normalizeMathDelimiters('把“一次删除请求 $i$”写成映射')).toBe('把“一次删除请求 $$i$$”写成映射')
  expect(normalizeMathDelimiters('其中：$\\theta\\in\\mathbb{R}^n$ 是权重')).toBe('其中：$$\\theta\\in\\mathbb{R}^n$$ 是权重')
  expect(normalizeMathDelimiters('$0.02236$ 与 $0.00559$')).toBe('$$0.02236$$ 与 $$0.00559$$')
  expect(normalizeMathDelimiters('**$x$** 加粗')).toBe('**$$x$$** 加粗')
  expect(normalizeMathDelimiters('$5$ 维问题')).toBe('$$5$$ 维问题')
})

test('prices and shell variables stay literal', () => {
  expect(normalizeMathDelimiters('价格 $5 和 $10，总共 $15')).toBe('价格 $5 和 $10，总共 $15')
  expect(normalizeMathDelimiters('价格是 $5，不是 $10')).toBe('价格是 $5，不是 $10')
  expect(normalizeMathDelimiters('use $HOME and $PATH')).toBe('use $HOME and $PATH')
  expect(normalizeMathDelimiters('a $ b $ c')).toBe('a $ b $ c')
  expect(normalizeMathDelimiters('成本$5')).toBe('成本$5')
})

test('display math on one line gets its own fence lines', () => {
  expect(normalizeMathDelimiters('$$x=1$$')).toBe('$$\nx=1\n$$\n'.trimEnd())
  expect(normalizeMathDelimiters('$$a=1\nb=2$$')).toBe('$$\na=1\nb=2\n$$')
  expect(normalizeMathDelimiters('$$x$$ 是分量')).toBe('$$x$$ 是分量')
  expect(normalizeMathDelimiters('$$a=1\nb=2\n$$')).toBe('$$a=1\nb=2\n$$')
})

test('code fences and inline code keep latex untouched', () => {
  expect(normalizeMathDelimiters('```tex\n\\(q_t\\)\n```')).toBe('```tex\n\\(q_t\\)\n```')
  expect(normalizeMathDelimiters('```tex\n$$x$$\n```')).toBe('```tex\n$$x$$\n```')
  expect(normalizeMathDelimiters('`\\(q_t\\)`')).toBe('`\\(q_t\\)`')
  expect(normalizeMathDelimiters('`$x$` 和 $y$')).toBe('`$x$` 和 $$y$$')
  expect(normalizeMathDelimiters('~~~\n\\[x\\]\n~~~')).toBe('~~~\n\\[x\\]\n~~~')
})

test('unclosed delimiters stay literal', () => {
  expect(normalizeMathDelimiters('broken \\(q_t here')).toBe('broken \\(q_t here')
  expect(normalizeMathDelimiters('closing only \\)')).toBe('closing only \\)')
  expect(normalizeMathDelimiters('lone $ here')).toBe('lone $ here')
})

test('adjacent delimiters convert without swallowing', () => {
  expect(normalizeMathDelimiters('\\(a\\)\\(b\\)')).toBe('$$a$$ $$b$$')
  expect(normalizeMathDelimiters('\\(f(x)=[1,2]\\)')).toBe('$$f(x)=[1,2]$$')
})

test('markdown structure without delimiters is returned as-is', () => {
  const text = '| a | b |\n|---|---|\n| 1 | 2 |\n\n- [ ] task\n\n~~strike~~'
  expect(normalizeMathDelimiters(text)).toBe(text)
})
