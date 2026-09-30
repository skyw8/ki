import { createElement, isValidElement, lazy, memo, Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, type JSX, type ReactNode } from 'react'
import { cjk } from '@streamdown/cjk'
import { Streamdown, parseMarkdownIntoBlocks, useIsCodeFenceIncomplete, type ExtraProps } from 'streamdown'
import { useI18n } from '../../i18n/index'
import { ICheck, ICopy } from '../../components/icons'
import { MarkdownBlockSplitter, MarkdownDocument } from './streamText'
import { recordStreamCommit, type DisplayRevision } from '../../lib/stream-metrics'
import { copyText } from '../../lib/clipboard'
import { queueMarkdown } from './parseQueue'
import { useTranscriptRowMeasurement } from '../chat/TranscriptRow'

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
  return <img alt={alt ?? ''} loading="lazy" decoding="async" {...rest} />
}

/**
 * Block lexing is the first pass over a message body, and Streamdown runs it per
 * mount. Caching the split per text means a body that left the window and came
 * back (scrolling up is mostly revisits) is lexed once.
 */
const BLOCK_CACHE_LIMIT = 300
const BLOCK_CACHE_BYTES = 8 * 1024 * 1024
let blockCacheBytes = 0
const blockCache = new Map<string, string[]>()
const blockBytes = (text: string, blocks: string[]) => 2 * (text.length + blocks.reduce((n, b) => n + b.length, 0))
function blocksFor(text: string): string[] {
  const hit = blockCache.get(text)
  if (hit) {
    // Refresh the insertion order so the limit evicts the coldest text.
    blockCache.delete(text)
    blockCache.set(text, hit)
    return hit
  }
  // A giant semantic block (list/fence/reference document) is already a safe
  // unit. Re-running marked's block splitter over it can be quadratic; the
  // underlying Markdown renderer accepts multiple blocks in one input too.
  const blocks = text.length > 4096 ? [text] : parseMarkdownIntoBlocks(text)
  const bytes = blockBytes(text, blocks)
  if (bytes > BLOCK_CACHE_BYTES) return blocks
  blockCache.set(text, blocks)
  blockCacheBytes += bytes
  while (blockCache.size > BLOCK_CACHE_LIMIT || blockCacheBytes > BLOCK_CACHE_BYTES) {
    const oldest = blockCache.keys().next().value
    if (oldest === undefined) break
    blockCacheBytes -= blockBytes(oldest, blockCache.get(oldest)!)
    blockCache.delete(oldest)
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
  revision,
}: {
  text: string
  streaming?: boolean
  className?: string
  revision?: DisplayRevision
}) {
  const [document] = useState(() => new MarkdownDocument())
  const root = useRef<HTMLDivElement>(null)
  const parts = useMemo(() => document.update(text, !streaming), [document, text, streaming])
  // This runs after actual text was committed, never at socket receipt or setState.
  useLayoutEffect(() => recordStreamCommit(revision, root.current), [revision, text])
  // Both kinds must share one sibling array for their keys to have the same
  // React scope. Promote the current piece in place when it seals; a new tail
  // gets its own plaintext-first admission instead of inheriting permission
  // to parse synchronously during a replay burst.
  const pieces = parts.segments.map((segment, index) => <MarkdownPiece key={index} text={segment} deferOffscreen={streaming} className={className} />)
  if (parts.tail) pieces.push(<MarkdownPiece key={parts.segments.length} text={parts.tail} live={streaming} className={className} />)
  return <div ref={root} data-stream-seq={revision?.seq}>{pieces}</div>
})

/** Keep content visible while an expensive block waits for its formatting turn.
 * A message finishing must not replace already-visible text with placeholders.
 * Immutable segments keep the same component through both streaming and final.
 */
const MarkdownPiece = memo(function MarkdownPiece({ text, live = false, deferOffscreen = false, className }: { text: string; live?: boolean; deferOffscreen?: boolean; className?: string }) {
  const { t } = useI18n()
  const [blocks] = useState(() => new MarkdownBlockSplitter())
  const root = useRef<HTMLDivElement>(null)
  const wasLive = useRef(live)
  if (live) wasLive.current = true
  const [admitted, setAdmitted] = useState(false)
  const small = text.length <= 4096
  // The first text layout (including cold fallback fonts) and first Markdown
  // parser initialization together exceeded a phone frame budget. Commit the
  // complete source first, then admit formatting through the existing queue.
  // New deltas do not reset this admission; subsequent small-tail updates stay
  // incremental and immediate.
  useEffect(() => {
    if (!live || admitted || !small) return
    return queueMarkdown(() => setAdmitted(true), () => root.current, 1)
  }, [live, admitted, small])
  const immediate = wasLive.current && admitted && small
  const [formatted, setFormatted] = useState('')
  const [explicit, setExplicit] = useState(false)
  const allowed = text.length <= 64 * 1024 || explicit
  const ready = immediate || !live && formatted === text
  // Formatting admission changes local DOM without changing the parent
  // message. Join its existing pre-paint row measurement: ResizeObserver alone
  // leaves a one-frame tail gap (166px in Firefox) after source becomes markup.
  useTranscriptRowMeasurement([ready, allowed])
  useEffect(() => {
    if (immediate || live || !allowed || formatted === text) return
    const enqueue = () => queueMarkdown(() => setFormatted(text), () => root.current, wasLive.current ? 1 : 0)
    if (!deferOffscreen || !root.current) return enqueue()
    // A replay burst seals many blocks above the viewport. Parsing them while
    // the newest text is still catching up consumed 28–35ms between display
    // commits on a throttled phone. Their complete source is already mounted:
    // format on visibility, or release all remaining work when streaming ends.
    let cancel: (() => void) | undefined
    const observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return
      observer.disconnect()
      cancel = enqueue()
    })
    observer.observe(root.current)
    return () => { observer.disconnect(); cancel?.() }
  }, [text, live, deferOffscreen, allowed, formatted, immediate])
  return <div ref={root} data-md-block="1" data-md-pending={!live && !ready && allowed ? '1' : undefined}>
    {!live && !allowed && <button type="button" className="body-load" onClick={() => setExplicit(true)}>{t('chat.formatMarkdown')}</button>}
    {ready ? <Streamdown className={segClass(className)} plugins={plugins} mode={live ? 'streaming' : 'static'} isAnimating={live} {...shared} parseMarkdownIntoBlocksFn={live ? blocks.split : blocksFor}>{text}</Streamdown>
      : <PlainText text={text} />}
  </div>
})

const TextChunk = memo(function TextChunk({ text }: { text: string }) { return <>{text}</> })
function PlainText({ text }: { text: string }) {
  const chunks = useMemo(() => {
    const result: string[] = []
    for (let at = 0; at < text.length;) {
      let end = Math.min(at + 4096, text.length)
      // Never split a surrogate pair into separate DOM text nodes.
      if (end < text.length && text.charCodeAt(end - 1) >= 0xd800 && text.charCodeAt(end - 1) <= 0xdbff) end--
      result.push(text.slice(at, end))
      at = end
    }
    return result
  }, [text])
  return <div className="md md-tail" data-md-tail="1">{chunks.map((chunk, i) => <TextChunk key={i} text={chunk} />)}</div>
}

/** Shared props of every piece a streaming message is rendered as. */
const shared = {
  parseIncompleteMarkdown: true,
  controls: false,
  lineNumbers: false,
  linkSafety,
  components,
} as const

function segClass(className?: string): string {
  return className ? `md md-seg ${className}` : 'md md-seg'
}
