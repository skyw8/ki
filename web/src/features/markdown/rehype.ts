import { defaultSchema, type Schema } from 'hast-util-sanitize'
import rehypeRaw from 'rehype-raw'
import rehypeSanitize from 'rehype-sanitize'
import type { PluggableList } from 'unified'

const baseAttributes = { ...defaultSchema.attributes }

export const markdownSanitizeSchema: Schema = {
  ...defaultSchema,
  attributes: {
    ...baseAttributes,
    code: [
      ...(baseAttributes.code ?? []),
      'metastring',
      ['className', 'math-inline'],
      ['className', 'math-display'],
    ],
  },
  clobberPrefix: '',
  protocols: {
    ...defaultSchema.protocols,
    href: [...(defaultSchema.protocols?.href ?? []), 'tel'],
  },
}

export const markdownRehypePlugins: PluggableList = [
  rehypeRaw,
  [rehypeSanitize, markdownSanitizeSchema],
]
