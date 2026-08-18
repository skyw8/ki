import remend from 'remend'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import { cjk } from '@streamdown/cjk'
import { unified, type Pluggable, type Processor } from 'unified'
import { visit } from 'unist-util-visit'
import { normalizeMarkdown } from '../src/markdown-normalize.ts'

function apply(processor: Processor, plugins: Pluggable[]) {
  for (const plugin of plugins) {
    if (Array.isArray(plugin)) processor.use(plugin[0], plugin[1])
    else processor.use(plugin)
  }
}

// Same remend → remark-cjk-friendly → remark-gfm order Streamdown uses.
export function parseMarkdown(src: string, streaming = false) {
  const text = streaming ? remend(normalizeMarkdown(src)) : normalizeMarkdown(src)
  const processor = unified().use(remarkParse)
  apply(processor, cjk.remarkPluginsBefore)
  processor.use(remarkGfm)
  apply(processor, cjk.remarkPluginsAfter)
  return processor.runSync(processor.parse(text))
}

export function nodeTypes(tree: unknown): string[] {
  const types: string[] = []
  visit(tree as Parameters<typeof visit>[0], node => {
    types.push(node.type)
  })
  return types
}

export function nodeValues(tree: unknown, type: string): string[] {
  const values: string[] = []
  visit(tree as Parameters<typeof visit>[0], type, node => {
    if ('value' in node && typeof node.value === 'string') values.push(node.value)
  })
  return values
}
