import { expect, test } from 'bun:test'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import remarkRehype from 'remark-rehype'
import { unified } from 'unified'
import { visit } from 'unist-util-visit'
import { markdownRehypePlugins } from '../src/features/markdown/rehype.ts'

type Properties = Record<string, unknown>
type Element = { type: string; tagName: string; properties: Properties; children: unknown[] }

function render(src: string): { text: string[]; elements: Element[] } {
  const processor = unified().use(remarkParse).use(remarkGfm).use(remarkRehype)
  for (const plugin of markdownRehypePlugins) {
    if (Array.isArray(plugin)) processor.use(plugin[0], plugin[1])
    else processor.use(plugin)
  }
  const tree = processor.runSync(processor.parse(src))
  const text: string[] = []
  const elements: Element[] = []
  visit(tree, node => {
    if (node.type === 'text' && typeof node.value === 'string') text.push(node.value)
    if (node.type === 'element') elements.push(node as unknown as Element)
  })
  return { text, elements }
}

test('relative document links survive sanitizing without a blocked marker', () => {
  const { text, elements } = render('[方案文档](docs/async-user-input.md)')
  expect(text.join('')).toBe('方案文档')
  expect(text.join('')).not.toContain('blocked')
  expect(elements.find(node => node.tagName === 'a')?.properties.href).toBe('docs/async-user-input.md')
})

test('dangerous link protocols lose the href instead of gaining a marker', () => {
  for (const src of ['[x](javascript:alert(1))', '[x](file:///etc/passwd)', '[x](vbscript:msgbox(1))']) {
    const { text, elements } = render(src)
    expect(text.join('')).toBe('x')
    expect(text.join('')).not.toContain('blocked')
    const anchor = elements.find(node => node.tagName === 'a')
    expect(anchor?.properties.href).toBeUndefined()
  }
})

test('images keep relative sources and drop data or file sources without a marker', () => {
  const relative = render('![shot](assets/shot.svg)')
  expect(relative.text.join('')).toBe('')
  expect(relative.elements.find(node => node.tagName === 'img')?.properties.src).toBe('assets/shot.svg')

  for (const src of ['![shot](data:image/png;base64,AAAA)', '![shot](file:///etc/passwd)']) {
    const { text, elements } = render(src)
    expect(text.join('')).not.toContain('blocked')
    expect(elements.find(node => node.tagName === 'img')?.properties.src).toBeUndefined()
  }
})

test('anchors, absolute paths, and external links are preserved', () => {
  const anchor = render('[s](#section)')
  expect(anchor.elements.find(node => node.tagName === 'a')?.properties.href).toBe('#section')

  const absolute = render('[f](/abs/path/app.py:12)')
  expect(absolute.elements.find(node => node.tagName === 'a')?.properties.href).toBe('/abs/path/app.py:12')

  const external = render('[e](https://example.com/x)')
  expect(external.elements.find(node => node.tagName === 'a')?.properties.href).toBe('https://example.com/x')
})

test('fenced code keeps its language class so highlighting still works', () => {
  const { elements } = render('```go\nfmt.Println("hi")\n```')
  const code = elements.find(node => node.tagName === 'code')
  expect(code?.properties.className).toEqual(['language-go'])
})
