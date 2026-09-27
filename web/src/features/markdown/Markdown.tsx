import { createElement, isValidElement, lazy, memo, Suspense, useEffect, useMemo, useRef, useState, type JSX, type ReactNode } from 'react'
import { cjk } from '@streamdown/cjk'
import { Streamdown, parseMarkdownIntoBlocks, useIsCodeFenceIncomplete, type ExtraProps } from 'streamdown'
import { useI18n } from '../../i18n/index'
import { ICheck, ICopy } from '../../components/icons'
import { normalizeMarkdown } from './markdown-normalize'
import { settleBoundaries } from './streamText'
import { copyText } from '../../lib/clipboard'
import { textHeightHint } from '../../lib/rowHeight'
import { queueMarkdown } from './parseQueue'

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
  const blocks = parseMarkdownIntoBlocks(text)
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
}: {
  text: string
  streaming?: boolean
  className?: string
}) {
  // Why a placeholder first: mounting a row parses its whole markdown (marked,
  // then remark/rehype and the plugins) on the main thread, and scrolling mounts
  // several rows at once, so the parses land in one frame as a long block. The
  // row's height is only known after that render. The shared queue admits one
  // parse per frame and prioritizes visible content; an idle callback per row
  // merely moved the same burst later. Estimates reserve space until the
  // virtualizer measures the actual body and compensates above the reader.
  const [ready, setReady] = useState(streaming)
  const pendingRef = useRef<HTMLDivElement>(null)
  const shown = useStreamingText(text, streaming)
  const source = useMemo(() => ready ? normalizeMarkdown(shown) : '', [shown, ready])
  const boundaries = useMemo(() => settleBoundaries(source), [source])
  const staticSegments = useMemo(() => {
    if (streaming || source.length <= TAIL_MAX) return null
    // Reference definitions have document scope; splitting them changes links.
    const ends = /^ {0,3}\[[^\]]+\]:/m.test(source) ? [] : boundaries
    const parts: string[] = []
    let start = 0
    for (const end of ends) if (end - start >= SEAL_MIN) { parts.push(source.slice(start, end)); start = end }
    if (start < source.length) parts.push(source.slice(start))
    return parts
  }, [source, boundaries, streaming])

  /**
   * Sealed segments: slices of this message whose rendering can no longer
   * change, each rendered by its own Streamdown and never re-parsed.
   *
   * Why segments rather than one growing prefix: Streamdown re-lexes whatever
   * text it is handed, so feeding it the settled prefix would re-parse the whole
   * message every time another block settled — the quadratic cost this split
   * exists to remove. A segment is immutable, so it parses once (and its blocks
   * come from the shared cache when the row is remounted by scrolling).
   */
  const sealRef = useRef<{ text: string; segments: string[] }>({ text: '', segments: [] })
  const [sealedState, setSealedState] = useState(sealRef.current)
  useEffect(() => {
    // Keep what this text still starts with, segment by segment: an edit, a
    // regeneration, a virtual-list row reused for another node — or the final
    // text with its trailing blank line trimmed — must only drop the segments
    // that no longer match, not force a re-parse of the whole message.
    const prev = sealRef.current
    let len = 0
    let kept = prev.segments
    for (let i = 0; i < prev.segments.length; i++) {
      if (!source.startsWith(prev.segments[i], len)) {
        kept = prev.segments.slice(0, i)
        break
      }
      len += prev.segments[i].length
    }
    if (kept !== prev.segments) {
      sealRef.current = { text: source.slice(0, len), segments: kept }
      setSealedState(sealRef.current)
    }
    if (!streaming && sealRef.current.segments.length === 0) return // a message loaded whole stays whole
    // Seal every chunk that is ready, not just the last boundary: a burst can
    // settle tens of KiB between two renders, and handing one Streamdown all of it
    // would put the quadratic parse back for that piece.
    let grown = sealRef.current
    for (const at of boundaries) {
      if (at - grown.text.length < SEAL_MIN) continue
      grown = { text: source.slice(0, at), segments: [...grown.segments, source.slice(grown.text.length, at)] }
    }
    if (grown === sealRef.current) return
    sealRef.current = grown
    setSealedState(grown)
  }, [source, boundaries, streaming])

  const rest = source.slice(sealedState.text.length)
  // What is left over is the part still being written. A short remainder is
  // parsed the way it always was (incomplete markdown closed up, deltas faded
  // in); a long one — one huge paragraph, a fence still streaming, a table — is
  // painted as source instead, because that is where re-parsing per delta used
  // to cost hundreds of milliseconds. A finished message always parses fully.
  const liveMode: 'streaming' | 'static' | 'plain' = !streaming ? 'static' : rest.length > TAIL_MAX ? 'plain' : 'streaming'
  useEffect(() => {
    if (ready) return
    return queueMarkdown(() => setReady(true), () => pendingRef.current)
  }, [ready])
  if (!ready) {
    return (
      <div
        ref={pendingRef}
        className="md-pending"
        data-md-pending="1"
        aria-busy="true"
        style={{ minHeight: `${textHeightHint(text)}px` }}
      />
    )
  }
  if (staticSegments) return <>{staticSegments.map((segment, index) => <StaticSegment key={index} text={segment} className={className} />)}</>
  return (
    <>
      {sealedState.segments.map((seg, i) => (
        <Streamdown key={`seg${i}`} className={segClass(className)} plugins={plugins} mode="static" {...shared}>
          {seg}
        </Streamdown>
      ))}
      {rest === '' ? null : liveMode === 'plain' ? (
        <div key="live" className="md md-tail" data-md-tail="1">
          {rest}
        </div>
      ) : (
        <Streamdown
          key="live"
          className={segClass(className)}
          plugins={plugins}
          mode={liveMode}
          isAnimating={streaming}
          {...shared}
          // Only the open tail changes per delta, so it is the one part worth
          // re-lexing; a settled segment is immutable and cached by its text.
          parseMarkdownIntoBlocksFn={liveMode === 'static' ? blocksFor : undefined}
        >
          {rest}
        </Streamdown>
      )}
    </>
  )
})

const StaticSegment = memo(function StaticSegment({ text, className }: { text: string; className?: string }) {
  const { t } = useI18n()
  const root = useRef<HTMLDivElement>(null)
  const [ready, setReady] = useState(false)
  const [format, setFormat] = useState(text.length <= 64 * 1024)
  useEffect(() => queueMarkdown(() => setReady(true), () => root.current), [])
  return <div ref={root} data-md-block="1" data-md-pending={ready ? undefined : '1'} style={ready ? undefined : { minHeight: textHeightHint(text) }}>
    {ready && (format ? <Streamdown className={segClass(className)} plugins={plugins} mode="static" {...shared} parseMarkdownIntoBlocksFn={blocksFor}>{text}</Streamdown>
      : <><button type="button" className="body-load" onClick={() => setFormat(true)}>{t('chat.formatMarkdown')}</button><div className="md md-tail">{text}</div></>)}
  </div>
})

// A settled segment is sealed once this much of the message can no longer change.
const SEAL_MIN = 4096
// A moving part longer than this is painted, not parsed.
const TAIL_MAX = 4096

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

/**
 * How long a streaming message may lag behind its newest delta.
 *
 * Attaching to a run replays its chunks, and every delta used to re-render the
 * message. The render itself is cheap now that only the open tail is parsed (see
 * streamText.ts), so this only has to keep a slow device from re-rendering tens of
 * times a second; the text still moves ~12 times a second.
 *
 * Why the deadline is measured from the last render rather than armed per delta:
 * a timer that is reset on every change is a debounce, and with deltas arriving
 * every few milliseconds it never fires — the text froze for the whole stream and
 * jumped when the provider paused. Tests read the rendered length over time; see
 * "keeps the text moving while it streams".
 */
const STREAM_RENDER_MS = 80

function useStreamingText(text: string, streaming: boolean): string {
  const [shown, setShown] = useState(text)
  const renderedAt = useRef(0)
  useEffect(() => {
    if (!streaming || text === shown) return
    const wait = Math.max(0, STREAM_RENDER_MS - (performance.now() - renderedAt.current))
    const id = window.setTimeout(() => {
      renderedAt.current = performance.now()
      setShown(text)
    }, wait)
    return () => window.clearTimeout(id)
  }, [text, streaming, shown])
  return streaming ? shown : text
}
