import { createElement, isValidElement, lazy, memo, Suspense, useEffect, useRef, useState, type JSX, type ReactNode } from 'react'
import { cjk } from '@streamdown/cjk'
import { Streamdown, useIsCodeFenceIncomplete, type ExtraProps } from 'streamdown'
import { useI18n } from './i18n'
import { ICheck, ICopy } from './icons'
import { normalizeMarkdown } from './markdown-normalize'

const plugins = { cjk }
const linkSafety = { enabled: false }
const MermaidBlock = lazy(() => import('./MermaidBlock'))

type MdProps<T extends keyof JSX.IntrinsicElements> = JSX.IntrinsicElements[T] & ExtraProps

// Streamdown's defaults ship Tailwind/shadcn chrome (code toolbar, table
// copy, link-safety modal, strong-as-span). Those classes do nothing here
// (this app is not on Tailwind) and would not match the chat bubble.
// Semantic tags let `.md` CSS own the look. Mermaid uses @streamdown/mermaid
// from MermaidBlock instead of Streamdown's mermaid chrome, so we can keep
// diagram/source toggle and copy on the same design tokens.
function md<T extends keyof JSX.IntrinsicElements>(tag: T) {
  return function MdEl({ node: _node, children, ...rest }: MdProps<T>) {
    return createElement(tag, rest, children)
  }
}

function nodeText(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(nodeText).join('')
  if (isValidElement(node)) return nodeText((node.props as { children?: ReactNode }).children)
  return ''
}

function languageOf(className?: string): string {
  const m = /(?:^|\s)language-([^\s]+)/.exec(className ?? '')
  return m?.[1] ?? ''
}

function tableToMarkdown(table: HTMLTableElement): string {
  const rows = Array.from(table.rows, row =>
    Array.from(row.cells, cell => cell.innerText.replace(/\u00a0/g, ' ').replace(/\|/g, '\\|').replace(/\n+/g, ' ').trim()),
  )
  if (rows.length === 0) return ''
  const width = Math.max(...rows.map(row => row.length))
  const pad = (row: string[]) => Array.from({ length: width }, (_, i) => row[i] ?? '')
  const line = (row: string[]) => `| ${pad(row).join(' | ')} |`
  const header = pad(rows[0])
  const sep = `| ${header.map(() => '---').join(' | ')} |`
  return [line(header), sep, ...rows.slice(1).map(row => line(pad(row)))].join('\n')
}

function CopyBtn({ getText, testid }: { getText: () => string; testid: string }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  return (
    <button
      type="button"
      className="md-copy"
      data-testid={testid}
      aria-label={copied ? t('md.copied') : t('chat.copy')}
      onClick={() => {
        const text = getText()
        if (!text) return
        void navigator.clipboard?.writeText(text)
        setCopied(true)
        window.clearTimeout(timer.current)
        timer.current = window.setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? <ICheck /> : <ICopy />}
    </button>
  )
}

function MdCode({ node: _node, children, className, ...rest }: MdProps<'code'>) {
  const isIncomplete = useIsCodeFenceIncomplete()
  if (!('data-block' in rest)) {
    return <code className={className}>{children}</code>
  }
  const code = nodeText(children)
  if (languageOf(className) === 'mermaid') {
    return (
      <Suspense fallback={<pre><code className={className}>{children}</code></pre>}>
        <MermaidBlock code={code} isIncomplete={isIncomplete} />
      </Suspense>
    )
  }
  return (
    <pre>
      <code className={className}>{children}</code>
    </pre>
  )
}

function MdTable({ node: _node, children, ...rest }: MdProps<'table'>) {
  const tableRef = useRef<HTMLTableElement>(null)
  return (
    <div className="md-table" data-testid="md-table">
      <div className="md-block-bar">
        <CopyBtn
          getText={() => {
            const el = tableRef.current
            return el ? tableToMarkdown(el) : ''
          }}
          testid="md-table-copy"
        />
      </div>
      <div className="md-table-scroll">
        <table ref={tableRef} {...rest}>{children}</table>
      </div>
    </div>
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
  table: MdTable,
  thead: md('thead'),
  tbody: md('tbody'),
  tr: md('tr'),
  th: md('th'),
  td: md('td'),
  a: MdA,
  img: MdImg,
  code: MdCode,
}

export const Markdown = memo(function Markdown({
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
      isAnimating={streaming}
      parseIncompleteMarkdown
      controls={false}
      lineNumbers={false}
      linkSafety={linkSafety}
      components={components}
    >
      {normalizeMarkdown(text)}
    </Streamdown>
  )
})
