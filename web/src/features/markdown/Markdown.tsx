import { createElement, isValidElement, lazy, memo, Suspense, useEffect, useRef, useState, type JSX, type ReactNode } from 'react'
import { cjk } from '@streamdown/cjk'
import { Streamdown, parseMarkdownIntoBlocks, useIsCodeFenceIncomplete, type ExtraProps } from 'streamdown'
import { useI18n } from '../../i18n/index'
import { ICheck, ICopy } from '../../components/icons'
import { normalizeMarkdown } from './markdown-normalize'
import { copyText } from '../../lib/clipboard'
import { textHeightHint } from '../../lib/rowHeight'

const plugins = { cjk }
const linkSafety = { enabled: false }
const DiagramBlock = lazy(() => import('./DiagramBlock'))

// Fences rendered by DiagramBlock: mermaid renders in-browser, plantuml via a
// PlantUML server (plantuml.ts). `puml` is plantuml's common short alias.
const DIAGRAM_KINDS: Record<string, 'mermaid' | 'plantuml'> = {
  mermaid: 'mermaid',
  plantuml: 'plantuml',
  puml: 'plantuml',
}

type MdProps<T extends keyof JSX.IntrinsicElements> = JSX.IntrinsicElements[T] & ExtraProps

// Streamdown's defaults ship Tailwind/shadcn chrome (code toolbar, table
// copy, link-safety modal, strong-as-span). Those classes do nothing here
// (this app is not on Tailwind) and would not match the chat bubble.
// Semantic tags let `.md` CSS own the look. Mermaid and plantuml fences go
// through DiagramBlock instead of Streamdown's mermaid chrome, so both share
// the diagram/source toggle, copy, and download on the same design tokens.
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
        void copyText(text).then(ok => {
          if (!ok) return
          setCopied(true)
          window.clearTimeout(timer.current)
          timer.current = window.setTimeout(() => setCopied(false), 1500)
        })
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
  const kind = DIAGRAM_KINDS[languageOf(className)]
  if (kind) {
    return (
      <Suspense fallback={<pre><code className={className}>{children}</code></pre>}>
        <DiagramBlock kind={kind} code={code} isIncomplete={isIncomplete} />
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

/**
 * Block lexing is the first pass over a message body, and Streamdown runs it per
 * mount. Caching the split per text means a body that left the window and came
 * back (scrolling up is mostly revisits) is lexed once.
 */
const BLOCK_CACHE_LIMIT = 300
const blockCache = new Map<string, string[]>()
function blocksFor(text: string): string[] {
  const hit = blockCache.get(text)
  if (hit) {
    // Refresh the insertion order so the limit evicts the coldest text.
    blockCache.delete(text)
    blockCache.set(text, hit)
    return hit
  }
  const blocks = parseMarkdownIntoBlocks(text)
  blockCache.set(text, blocks)
  if (blockCache.size > BLOCK_CACHE_LIMIT) {
    const oldest = blockCache.keys().next().value
    if (oldest !== undefined) blockCache.delete(oldest)
  }
  return blocks
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
  // Why a placeholder first: mounting a row parses its whole markdown (marked,
  // then remark/rehype and the plugins) on the main thread, and scrolling mounts
  // several rows at once, so the parses land in one frame as a long block. The
  // row's height is only known after that render, which is also what makes the
  // virtual list correct the scroll position under the reader's finger. Rendering
  // the body on the next idle callback keeps the parse out of the frame that
  // handles the gesture; the placeholder reserves the estimated height, so the
  // list does not resize when the body arrives one frame later.
  const [ready, setReady] = useState(streaming)
  useEffect(() => {
    if (ready) return
    const w = window as Window & {
      requestIdleCallback?: (cb: () => void, opts?: { timeout: number }) => number
      cancelIdleCallback?: (id: number) => void
    }
    if (!w.requestIdleCallback) {
      const id = window.setTimeout(() => setReady(true), 16)
      return () => window.clearTimeout(id)
    }
    const id = w.requestIdleCallback(() => setReady(true), { timeout: 200 })
    return () => w.cancelIdleCallback?.(id)
  }, [ready])
  if (!ready) {
    return (
      <div
        className="md-pending"
        data-md-pending="1"
        aria-busy="true"
        style={{ minHeight: `${textHeightHint(text)}px` }}
      />
    )
  }
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
      // Settled text is lexed once per text: a message scrolled out of the
      // window and back reuses its blocks instead of re-splitting the whole
      // body. Streaming text changes every delta, so it stays uncached.
      parseMarkdownIntoBlocksFn={streaming ? undefined : blocksFor}
      components={components}
    >
      {normalizeMarkdown(text)}
    </Streamdown>
  )
})
