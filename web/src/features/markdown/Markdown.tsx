import { createElement, isValidElement, lazy, memo, Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, type JSX, type ReactNode } from 'react'
import { code, type HighlightResult } from '@streamdown/code'
import { cjk } from '@streamdown/cjk'
import { math } from '@streamdown/math'
import { Streamdown, parseMarkdownIntoBlocks, useIsCodeFenceIncomplete, type ExtraProps, type PluginConfig } from 'streamdown'
import { useI18n } from '../../i18n/index'
import { ICheck, ICopy } from '../../components/icons'
import { MarkdownBlockSplitter, MarkdownDocument } from './streamText'
import { recordStreamCommit, type DisplayRevision } from '../../lib/stream-metrics'
import { copyText } from '../../lib/clipboard'
import { queueMarkdown } from './parseQueue'
import { useTranscriptRowMeasurement } from '../chat/TranscriptRow'
import { MarkdownImage, MarkdownLink } from './links'
import { normalizeMathDelimiters } from './math-delimiters'
import { markdownRehypePlugins } from './rehype'

const codePlugin = code
const codeThemes = codePlugin.getThemes()
const plugins = { cjk, code: codePlugin, math } satisfies PluginConfig
const linkSafety = { enabled: false }
const DiagramBlock = lazy(() => import('./DiagramBlock'))

const DIAGRAM_KINDS: Record<string, 'mermaid' | 'plantuml'> = {
  mermaid: 'mermaid',
  plantuml: 'plantuml',
  puml: 'plantuml',
}

type MdProps<T extends keyof JSX.IntrinsicElements> = JSX.IntrinsicElements[T] & ExtraProps

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

function tokenStyle(token: HighlightResult['tokens'][number][number]): Record<string, string> | undefined {
  const html = token.htmlStyle ?? {}
  const style: Record<string, string> = {}
  const light = html.color ?? token.color
  if (light) style['--shiki-light'] = light
  if (html['--shiki-dark']) style['--shiki-dark'] = html['--shiki-dark']
  if (html['background-color']) {
    style['--shiki-light-bg'] = html['background-color']
    style['--shiki-dark-bg'] = html['--shiki-dark-bg'] ?? html['background-color']
  }
  return Object.keys(style).length ? style : undefined
}

type CodeLanguage = Parameters<typeof codePlugin.supportsLanguage>[0]

function HighlightedCode({ text, language }: { text: string; language: string }) {
  const [result, setResult] = useState<HighlightResult | null>(null)
  useEffect(() => {
    const lang = language as CodeLanguage
    if (!codePlugin.supportsLanguage(lang)) {
      setResult(null)
      return
    }
    let current = true
    const ready = codePlugin.highlight({ code: text, language: lang, themes: codeThemes }, next => {
      if (current) setResult(next)
    })
    setResult(ready)
    return () => { current = false }
  }, [text, language])
  if (!result) return <>{text}</>
  return result.tokens.map((line, index) => (
    <span key={index} className="md-code-line">
      {line.length === 0
        ? '\n'
        : line.map((token, tokenIndex) => (
          <span key={tokenIndex} style={tokenStyle(token)}>{token.content}</span>
        ))}
    </span>
  ))
}

function MdCode({ node: _node, children, className, ...rest }: MdProps<'code'>) {
  const isIncomplete = useIsCodeFenceIncomplete()
  const classes = (className ?? '').split(/\s+/)
  const mathNode = classes.includes('math-inline') || classes.includes('math-display')
  const language = mathNode ? '' : languageOf(className)
  if (!('data-block' in rest)) {
    return <code className={className}>{children}</code>
  }
  const source = nodeText(children)
  const kind = DIAGRAM_KINDS[language]
  if (kind) {
    return (
      <Suspense fallback={<pre><code className={className}>{children}</code></pre>}>
        <DiagramBlock kind={kind} code={source} isIncomplete={isIncomplete} />
      </Suspense>
    )
  }
  return (
    <pre>
      <code className={className}><HighlightedCode text={source} language={language} /></code>
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

function MdA({ node: _node, href, title, children }: MdProps<'a'>) {
  return <MarkdownLink href={href} title={title}>{children}</MarkdownLink>
}

function MdImg({ node: _node, alt, src, title, ...rest }: MdProps<'img'>) {
  return <MarkdownImage alt={alt} src={src} title={title} {...rest} />
}

const BLOCK_CACHE_LIMIT = 300
const BLOCK_CACHE_BYTES = 8 * 1024 * 1024
let blockCacheBytes = 0
const blockCache = new Map<string, string[]>()
const blockBytes = (text: string, blocks: string[]) => 2 * (text.length + blocks.reduce((n, b) => n + b.length, 0))
function blocksFor(text: string): string[] {
  const hit = blockCache.get(text)
  if (hit) {
    blockCache.delete(text)
    blockCache.set(text, hit)
    return hit
  }
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
  const source = useMemo(() => normalizeMathDelimiters(text), [text])
  const parts = useMemo(() => document.update(source, !streaming), [document, source, streaming])
  useLayoutEffect(() => recordStreamCommit(revision, root.current), [revision, text])
  const pieces = parts.segments.map((segment, index) => <MarkdownPiece key={index} text={segment} deferOffscreen={streaming} className={className} />)
  if (parts.tail) pieces.push(<MarkdownPiece key={parts.segments.length} text={parts.tail} live={streaming} className={className} />)
  return <div ref={root} data-stream-seq={revision?.seq}>{pieces}</div>
})

const MarkdownPiece = memo(function MarkdownPiece({ text, live = false, deferOffscreen = false, className }: { text: string; live?: boolean; deferOffscreen?: boolean; className?: string }) {
  const { t } = useI18n()
  const [blocks] = useState(() => new MarkdownBlockSplitter())
  const root = useRef<HTMLDivElement>(null)
  const wasLive = useRef(live)
  if (live) wasLive.current = true
  const [admitted, setAdmitted] = useState(false)
  const small = text.length <= 4096
  useEffect(() => {
    if (!live || admitted || !small) return
    return queueMarkdown(() => setAdmitted(true), () => root.current, 1)
  }, [live, admitted, small])
  const immediate = wasLive.current && admitted && small
  const [formatted, setFormatted] = useState('')
  const [explicit, setExplicit] = useState(false)
  const allowed = text.length <= 64 * 1024 || explicit
  const ready = immediate || !live && formatted === text
  useTranscriptRowMeasurement([ready, allowed])
  useEffect(() => {
    if (immediate || live || !allowed || formatted === text) return
    const enqueue = () => queueMarkdown(() => setFormatted(text), () => root.current, wasLive.current ? 1 : 0)
    if (!deferOffscreen || !root.current) return enqueue()
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
      if (end < text.length && text.charCodeAt(end - 1) >= 0xd800 && text.charCodeAt(end - 1) <= 0xdbff) end--
      result.push(text.slice(at, end))
      at = end
    }
    return result
  }, [text])
  return <div className="md md-tail" data-md-tail="1">{chunks.map((chunk, i) => <TextChunk key={i} text={chunk} />)}</div>
}

const shared = {
  parseIncompleteMarkdown: true,
  controls: false,
  lineNumbers: false,
  linkSafety,
  rehypePlugins: markdownRehypePlugins,
  components,
} as const

function segClass(className?: string): string {
  return className ? `md md-seg ${className}` : 'md md-seg'
}
