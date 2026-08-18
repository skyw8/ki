function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

// Models sometimes emit fullwidth backticks; treat them as ASCII so
// `code` spans are not left visible in the bubble.
function normalize(src: string): string {
  return src.replace(/\r\n/g, '\n').replace(/\uFF40/g, '`')
}

function decorate(escaped: string): string {
  return escaped
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1<em>$2</em>')
    .replace(/~~([^~]+)~~/g, '<del>$1</del>')
    .replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>')
}

// Tokenize code spans first (CommonMark: matching backtick runs) so
// emphasis and links cannot rewrite the inside of `code`.
function inline(src: string): string {
  let out = ''
  let i = 0
  while (i < src.length) {
    if (src[i] === '`') {
      let n = 0
      while (i + n < src.length && src[i + n] === '`') n++
      const fence = src.slice(i, i + n)
      const close = src.indexOf(fence, i + n)
      if (close !== -1 && src[close + n] !== '`') {
        let body = src.slice(i + n, close)
        if (body.length >= 2 && body.startsWith(' ') && body.endsWith(' ')) body = body.slice(1, -1)
        out += `<code>${escapeHtml(body)}</code>`
        i = close + n
        continue
      }
      out += escapeHtml(fence)
      i += n
      continue
    }
    let j = i + 1
    while (j < src.length && src[j] !== '`') j++
    out += decorate(escapeHtml(src.slice(i, j)))
    i = j
  }
  return out
}

const OPEN_FENCE = /^( {0,3})(`{3,}|~{3,})(.*)$/
const CLOSE_FENCE = /^( {0,3})(`{3,}|~{3,})\s*$/
const ATX = /^( {0,3})(#{1,4})\s+(.*)$/
const HR = /^( {0,3})(?:[-*_]\s*){3,}$/
const QUOTE = /^( {0,3})>\s?(.*)$/

export function renderMarkdown(src: string): string {
  const lines = normalize(src).split('\n')
  const out: string[] = []
  let i = 0
  let para: string[] = []
  const flushP = () => {
    if (!para.length) return
    out.push(`<p>${inline(para.join('\n'))}</p>`)
    para = []
  }
  while (i < lines.length) {
    const line = lines[i]
    const open = OPEN_FENCE.exec(line)
    if (open) {
      const marker = open[2][0]
      const len = open[2].length
      const info = open[3].trim()
      // A backtick fence cannot have backticks in the info string.
      if (marker !== '`' || !info.includes('`')) {
        flushP()
        const buf: string[] = []
        i++
        while (i < lines.length) {
          const close = CLOSE_FENCE.exec(lines[i])
          if (close && close[2][0] === marker && close[2].length >= len) break
          buf.push(lines[i])
          i++
        }
        const lang = escapeHtml((info.split(/\s+/)[0] || ''))
        out.push(`<pre><code class="lang-${lang}">${escapeHtml(buf.join('\n'))}</code></pre>`)
        if (i < lines.length) i++
        continue
      }
    }
    const heading = ATX.exec(line)
    if (heading) {
      flushP()
      const lv = heading[2].length
      out.push(`<h${lv}>${inline(heading[3])}</h${lv}>`)
      i++
      continue
    }
    if (HR.test(line) && line.trim().length >= 3) {
      flushP()
      out.push('<hr />')
      i++
      continue
    }
    if (QUOTE.test(line)) {
      flushP()
      const buf: string[] = []
      while (i < lines.length && QUOTE.test(lines[i])) {
        buf.push(QUOTE.exec(lines[i])![2])
        i++
      }
      out.push(`<blockquote>${inline(buf.join('\n'))}</blockquote>`)
      continue
    }
    if (/^\s*\|/.test(line) && i + 1 < lines.length && /^\s*\|?\s*:?-/.test(lines[i + 1])) {
      flushP()
      const rows: string[][] = []
      while (i < lines.length && /^\s*\|/.test(lines[i])) {
        const cells = lines[i].trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim())
        if (!/^\s*:?-/.test(cells.join(''))) rows.push(cells)
        i++
      }
      if (rows.length) {
        const head = rows[0]
        out.push('<table><thead><tr>' + head.map(c => `<th>${inline(c)}</th>`).join('') + '</tr></thead>')
        if (rows.length > 1) {
          out.push('<tbody>')
          for (const row of rows.slice(1)) {
            out.push('<tr>' + row.map(c => `<td>${inline(c)}</td>`).join('') + '</tr>')
          }
          out.push('</tbody>')
        }
        out.push('</table>')
      }
      continue
    }
    if (/^\s*[-*]\s+/.test(line)) {
      flushP()
      out.push('<ul>')
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
        out.push(`<li>${inline(lines[i].replace(/^\s*[-*]\s+/, ''))}</li>`)
        i++
      }
      out.push('</ul>')
      continue
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      flushP()
      out.push('<ol>')
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        out.push(`<li>${inline(lines[i].replace(/^\s*\d+\.\s+/, ''))}</li>`)
        i++
      }
      out.push('</ol>')
      continue
    }
    if (line.trim() === '') {
      flushP()
      i++
      continue
    }
    para.push(line)
    i++
  }
  flushP()
  return out.join('')
}
