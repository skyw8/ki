/**
 * Splitting a streaming message into the part that can no longer change and the
 * part still being written.
 *
 * Why: Streamdown re-lexes its whole text into markdown blocks on every change,
 * and the block cache is off while streaming (every delta is a new text), so a
 * reply costs more per delta the longer it gets. The parse of a prefix stops
 * changing as soon as a blank line closes its last block, and that is the split
 * this module finds: Markdown.tsx seals the settled prefix into segments that
 * are never re-parsed, parses only the open tail, and paints the tail as plain
 * text when even that grows long (one huge paragraph, a code fence still
 * streaming, a table). Measured on the block lexer alone (JavaScriptCore), 6 KiB
 * of paragraphs cost 16 ms per pass, 12 KiB 46 ms, 24 KiB 154 ms and 49 KiB
 * 623 ms, so at tens of KiB per delta the old path lost seconds per render.
 *
 * The rule is deliberately conservative: a blank line only settles the block
 * before it when appending more text cannot join that block. A fence that is
 * still open swallows blank lines; an HTML block ends only at a blank line it
 * does not itself contain; an ordered list, a blockquote and an indented code
 * block all continue past a blank line when the next line looks like more of
 * them. One block like that also stops the split for good, because the settled
 * part is a prefix: sealing past it would render its continuation as a separate
 * fragment. Lists and quotes are also the reason the split is allowed to turn one
 * list into two: splitting a `ul` only adds the 10px gap between blocks, while
 * leaving a long list unsettled would mean painting the whole thing as source.
 * An `ol` is *not* split at a blank line unless the next line has ended it — two
 * `<ol>`s would restart the numbering.
 */

export type SettledSplit = {
  /** Prefix whose rendering can no longer change; never empty while streaming. */
  sealed: string
  /** The rest: still being written, so it may change with the next delta. */
  live: string
}

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/
const ORDERED_ITEM = /^ {0,3}\d{1,9}[.)][ \t]/
const QUOTE = /^ {0,3}>/
const INDENTED = /^(?: {4}|\t)/
// HTML blocks that a blank line does not end: they run to their closing tag or
// comment terminator, so anything after them may still be inside them. Every
// other HTML block ends at a blank line like any other block.
const SPANNING_HTML = /^ {0,3}(?:<(pre|script|style|textarea)[\s>/]|<!--)/i
const SPANNING_TERMINATOR: Record<string, RegExp> = {
  '<!--': /-->/,
  pre: /<\/pre\s*>/i,
  script: /<\/script\s*>/i,
  style: /<\/style\s*>/i,
  textarea: /<\/textarea\s*>/i,
}

/** What the block ending at a blank line was, as far as appending cares. */
type Kind = 'ok' | 'ol' | 'quote' | 'indented' | 'html'

/** classify reads the kind of a block from its first line and full text. */
function classify(line: string, block: string): Kind {
  if (ORDERED_ITEM.test(line)) return 'ol'
  if (QUOTE.test(line)) return 'quote'
  if (INDENTED.test(line)) return 'indented'
  const html = SPANNING_HTML.exec(line)
  if (html) {
    const tag = (html[1] ?? '<!--').toLowerCase()
    if (!SPANNING_TERMINATOR[tag]?.test(block)) return 'html'
  }
  return 'ok'
}

/**
 * canJoin reports whether text that arrives after a blank line can still become
 * part of this block. `next` is the first non-blank line that follows, or null
 * when nothing follows yet: without a lookahead only a block that cannot grow at
 * all is safe, which is why an ordered list or a quote at the end of the text
 * stays live.
 */
function canJoin(kind: Kind, next: string | null): boolean {
  if (kind === 'ok') return false
  if (kind === 'html') return true
  if (next === null) return true
  switch (kind) {
    case 'ol':
      return ORDERED_ITEM.test(next) || INDENTED.test(next)
    case 'quote':
      return QUOTE.test(next) || INDENTED.test(next)
    default:
      return INDENTED.test(next)
  }
}

/** skipBlanks returns the start and text of the first line after blank lines. */
function skipBlanks(text: string, from: number): { at: number; line: string | null } {
  let i = from
  while (i < text.length) {
    const nl = text.indexOf('\n', i)
    const end = nl === -1 ? text.length : nl
    const line = text.slice(i, end)
    if (line.trim() !== '') return { at: i, line }
    i = nl === -1 ? text.length : nl + 1
  }
  return { at: i, line: null }
}

/**
 * settleBoundaries lists the offsets a settled prefix may end at, ascending.
 * Each one is right after the blank lines that closed a block which later text
 * cannot join. Markdown.tsx seals between them in chunks, so no single Streamdown
 * is ever handed the growing whole, and renders everything after the last one as
 * the moving part.
 */
export function settleBoundaries(text: string): number[] {
  const sealed: number[] = []
  let pos = 0
  while (pos < text.length) {
    // One block: from here to the blank line that ends it, or to the end of the
    // text. Fences are tracked across lines because a blank line inside one does
    // not end the block.
    let kind: Kind | null = null
    let fence: string | null = null
    let blank = -1
    let i = pos
    while (i < text.length) {
      const nl = text.indexOf('\n', i)
      const end = nl === -1 ? text.length : nl
      const line = text.slice(i, end)
      const next = nl === -1 ? text.length : nl + 1
      if (fence === null && line.trim() === '') {
        blank = i
        break
      }
      if (fence !== null) {
        const close = FENCE_CLOSE.exec(line)
        if (close && close[1][0] === fence[0] && close[1].length >= fence.length) fence = null
      } else {
        const open = FENCE_OPEN.exec(line)
        // A backtick fence's info string cannot contain a backtick.
        if (open && !(open[1][0] === '`' && line.slice(open[0].length).includes('`'))) fence = open[1]
      }
      i = next
    }
    const block = text.slice(pos, blank === -1 ? text.length : blank)
    if (kind === null && block.trim() !== '') {
      kind = classify(block.slice(0, block.indexOf('\n') === -1 ? block.length : block.indexOf('\n')), block)
    }
    if (blank === -1) break // the last block is still open
    const after = skipBlanks(text, blank)
    if (fence !== null || kind === null) break
    if (kind === 'html') {
      // The rest of the message may still be inside this block, so nothing after
      // it can be sealed either.
      break
    }
    if (canJoin(kind, after.line)) {
      // More text can join this block (an ordered list gaining an item, a quote
      // gaining a line), so this boundary is not one. The *next* boundary can
      // still settle the whole prefix once the block has ended — that is why this
      // continues instead of stopping: sealing here would render the continuation
      // as a separate fragment (an <ol> restarting its numbering, a quote losing
      // its border).
      pos = after.at
      continue
    }
    sealed.push(after.at)
    pos = after.at
  }
  return sealed
}

/** splitSettled is settleBoundaries from the caller's side: prefix and tail. */
export function splitSettled(text: string): SettledSplit {
  const boundaries = settleBoundaries(text)
  const at = boundaries.length ? boundaries[boundaries.length - 1] : 0
  return { sealed: text.slice(0, at), live: text.slice(at) }
}
