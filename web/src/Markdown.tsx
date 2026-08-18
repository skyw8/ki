import { createElement, type JSX } from 'react'
import { cjk } from '@streamdown/cjk'
import { Streamdown, type ExtraProps } from 'streamdown'
import { normalizeMarkdown } from './markdown-normalize'

const plugins = { cjk }
const linkSafety = { enabled: false }

type MdProps<T extends keyof JSX.IntrinsicElements> = JSX.IntrinsicElements[T] & ExtraProps

// Streamdown's defaults ship Tailwind/shadcn chrome (code toolbar, table
// copy, link-safety modal, strong-as-span). Those classes do nothing here
// (this app is not on Tailwind) and would not match the chat bubble.
// Semantic tags let `.md` CSS own the look.
function md<T extends keyof JSX.IntrinsicElements>(tag: T) {
  return function MdEl({ node: _node, children, ...rest }: MdProps<T>) {
    return createElement(tag, rest, children)
  }
}

function MdCode({ node: _node, children, className, ...rest }: MdProps<'code'>) {
  if (!('data-block' in rest)) {
    return <code className={className}>{children}</code>
  }
  return (
    <pre>
      <code className={className}>{children}</code>
    </pre>
  )
}

function MdA({ node: _node, href, children }: MdProps<'a'>) {
  return <a href={href} target="_blank" rel="noreferrer">{children}</a>
}

function MdImg({ node: _node, alt, ...rest }: MdProps<'img'>) {
  return <img alt={alt ?? ''} {...rest} />
}

const components = {
  h1: md('h1'),
  h2: md('h2'),
  h3: md('h3'),
  h4: md('h4'),
  h5: md('h5'),
  h6: md('h6'),
  ul: md('ul'),
  ol: md('ol'),
  li: md('li'),
  hr: md('hr'),
  strong: md('strong'),
  blockquote: md('blockquote'),
  table: md('table'),
  thead: md('thead'),
  tbody: md('tbody'),
  tr: md('tr'),
  th: md('th'),
  td: md('td'),
  a: MdA,
  img: MdImg,
  code: MdCode,
}

export function Markdown({
  text,
  streaming = false,
  className,
}: {
  text: string
  streaming?: boolean
  className?: string
}) {
  return (
    <Streamdown
      className={className ? `md ${className}` : 'md'}
      plugins={plugins}
      mode={streaming ? 'streaming' : 'static'}
      parseIncompleteMarkdown
      controls={false}
      lineNumbers={false}
      linkSafety={linkSafety}
      components={components}
    >
      {normalizeMarkdown(text)}
    </Streamdown>
  )
}
