/** Stateful block-boundary scanning. Only newly completed lines are inspected;
 * unterminated lines remain mutable, even when a single code fence is enormous.
 * Document-wide reference definitions disable independent segment rendering.
 */
const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/
const ORDERED = /^ {0,3}\d{1,9}[.)][ \t]/
const BULLET = /^ {0,3}[-+*][ \t]/
const QUOTE = /^ {0,3}>/
const INDENTED = /^(?: {4}|\t)/
const HTML = /^ {0,3}(?:<(pre|script|style|textarea)[\s>/]|<!--)/i
const TERMINATORS: Record<string, RegExp> = {
  '<!--': /-->/, pre: /<\/pre\s*>/i, script: /<\/script\s*>/i,
  style: /<\/style\s*>/i, textarea: /<\/textarea\s*>/i,
}
type Kind = 'ordinary' | 'ordered' | 'bullet' | 'quote' | 'indented'
function kindOf(line: string): Kind {
  if (ORDERED.test(line)) return 'ordered'
  if (BULLET.test(line)) return 'bullet'
  if (QUOTE.test(line)) return 'quote'
  if (INDENTED.test(line)) return 'indented'
  return 'ordinary'
}
function continues(kind: Kind, line: string): boolean {
  if (INDENTED.test(line)) return kind !== 'ordinary'
  if (kind === 'ordered') return ORDERED.test(line)
  if (kind === 'bullet') return BULLET.test(line)
  if (kind === 'quote') return QUOTE.test(line)
  return false
}

export class SettledScanner {
  source = ''
  boundaries: number[] = []
  hasReferences = false
  scannedCharacters = 0
  private lineStart = 0
  private searched = 0
  private kind: Kind | undefined
  private fence: string | undefined
  private html: RegExp | undefined
  private blankEnd = 0

  push(delta: string) {
    this.source += delta
    while (true) {
      const nl = this.source.indexOf('\n', this.searched)
      this.scannedCharacters += (nl < 0 ? this.source.length : nl + 1) - this.searched
      if (nl < 0) { this.searched = this.source.length; break }
      const line = this.source.slice(this.lineStart, nl)
      const end = nl + 1
      this.lineStart = this.searched = end
      this.consume(line, end)
    }
  }

  private consume(line: string, end: number) {
    if (this.html) {
      if (this.html.test(line)) this.html = undefined
      return
    }
    if (this.fence) {
      const close = FENCE_CLOSE.exec(line)
      if (close && close[1][0] === this.fence[0] && close[1].length >= this.fence.length) this.fence = undefined
      return
    }
    if (!line.trim()) {
      if (this.kind === 'ordinary') { this.boundaries.push(end); this.kind = undefined }
      else if (this.kind) this.blankEnd = end
      else if (this.boundaries.length) this.boundaries[this.boundaries.length - 1] = end
      return
    }
    if (this.blankEnd) {
      if (this.kind && !continues(this.kind, line)) {
        this.boundaries.push(this.blankEnd)
        this.kind = undefined
      }
      this.blankEnd = 0
    }
    if (/^ {0,3}\[[^\]]+\]:/.test(line)) this.hasReferences = true
    if (!this.kind) this.kind = kindOf(line)
    const html = HTML.exec(line)
    if (html) {
      const terminator = TERMINATORS[(html[1] ?? '<!--').toLowerCase()]
      if (!terminator.test(line)) this.html = terminator
    }
    const open = FENCE_OPEN.exec(line)
    if (open && !(open[1][0] === '`' && line.slice(open[0].length).includes('`'))) this.fence = open[1]
  }

  get references(): boolean {
    // An unfinished definition may already resolve a link, so invalidate before
    // its newline as well. Only inspect the uncommitted line here.
    return this.hasReferences || /^ {0,3}\[[^\]]+\]:/.test(this.source.slice(this.lineStart))
  }
}

export function settleBoundaries(text: string): number[] {
  const scanner = new SettledScanner()
  scanner.push(text)
  return scanner.boundaries
}

export type SettledSplit = { sealed: string; live: string }
export function splitSettled(text: string): SettledSplit {
  const boundaries = settleBoundaries(text)
  const at = boundaries.at(-1) ?? 0
  return { sealed: text.slice(0, at), live: text.slice(at) }
}

/** Streamdown's live block splitter only needs semantic boundaries, not a
 * second full Markdown tokenization before its actual Markdown parser. Reuse
 * the same incremental scanner as the document; keep lists, fences, HTML and
 * document-wide references intact. The caller bounds the active tail to 4K.
 */
export class MarkdownBlockSplitter {
  private scanner = new SettledScanner()

  split = (text: string): string[] => {
    if (!text.startsWith(this.scanner.source)) this.scanner = new SettledScanner()
    this.scanner.push(text.slice(this.scanner.source.length))
    if (this.scanner.references) return [text]
    const blocks: string[] = []
    let start = 0
    for (const end of this.scanner.boundaries) {
      blocks.push(text.slice(start, end))
      start = end
    }
    if (start < text.length) blocks.push(text.slice(start))
    return blocks
  }

  get scannedCharacters() { return this.scanner.scannedCharacters }
}

export type MarkdownParts = { segments: readonly string[]; tail: string; references: boolean }

/** Retain both source and semantic segments across frame commits and message_end.
 * Checking append identity is still O(prefix), but normalization and line/HTML/
 * fence scanning no longer repeat over that prefix. Rewrites reset the scanner.
 */
export class MarkdownDocument {
  private raw = ''
  private pendingCR = false
  private scanner = new SettledScanner()
  private segments: string[] = []
  private sealed = 0
  private boundary = 0

  update(raw: string, complete = false): MarkdownParts {
    if (!raw.startsWith(this.raw)) {
      this.raw = ''
      this.pendingCR = false
      this.scanner = new SettledScanner()
      this.segments = []
      this.sealed = this.boundary = 0
    }
    let delta = (this.pendingCR ? '\r' : '') + raw.slice(this.raw.length)
    this.pendingCR = !complete && delta.endsWith('\r')
    if (this.pendingCR) delta = delta.slice(0, -1)
    this.raw = raw
    this.scanner.push(delta.replace(/\r\n/g, '\n').replace(/\uFF40/g, '`'))
    const references = this.scanner.references
    if (references) return { segments: [], tail: this.scanner.source, references }
    let grown = this.segments
    while (this.boundary < this.scanner.boundaries.length) {
      const end = this.scanner.boundaries[this.boundary++]
      if (end - this.sealed < 4096) continue
      if (grown === this.segments) grown = [...grown]
      grown.push(this.scanner.source.slice(this.sealed, end))
      this.sealed = end
    }
    this.segments = grown
    return { segments: grown, tail: this.scanner.source.slice(this.sealed), references }
  }
}
