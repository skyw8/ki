function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function inline(s: string): string {
  return escapeHtml(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1<em>$2</em>')
    .replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>')
}

export function renderMarkdown(src: string): string {
  const lines = src.replace(/\r\n/g, '\n').split('\n')
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
    if (line.startsWith('```')) {
      flushP()
      const lang = escapeHtml(line.slice(3).trim())
      const buf: string[] = []
      i++
      while (i < lines.length && !lines[i].startsWith('```')) {
        buf.push(lines[i])
        i++
      }
      out.push(`<pre><code class="lang-${lang}">${escapeHtml(buf.join('\n'))}</code></pre>`)
      i++
      continue
    }
    if (line.startsWith('#')) {
      flushP()
      const m = /^(#{1,4})\s+(.*)$/.exec(line)
      if (m) {
        const lv = m[1].length
        out.push(`<h${lv}>${inline(m[2])}</h${lv}>`)
        i++
        continue
      }
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
