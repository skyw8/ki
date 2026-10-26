const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/

const DELIMITERS: Record<string, { close: string; inline: boolean }> = {
  '(': { close: '\\)', inline: true },
  '[': { close: '\\]', inline: false },
}

function backticks(text: string, at: number): number {
  let end = at
  while (text[end] === '`') end += 1
  return end - at
}

function lineStartIsBlank(text: string, at: number): boolean {
  for (let index = at - 1; index >= 0; index -= 1) {
    const char = text[index]
    if (char === '\n') return true
    if (char !== ' ' && char !== '\t') return false
  }
  return true
}

function lineEndIsBlank(text: string, at: number): boolean {
  for (let index = at; index < text.length; index += 1) {
    const char = text[index]
    if (char === '\n') return true
    if (char !== ' ' && char !== '\t') return false
  }
  return true
}

function replaceInline(text: string): string {
  let out = ''
  let index = 0
  let ticks = 0
  while (index < text.length) {
    const char = text[index]
    if (char === '`') {
      const run = backticks(text, index)
      if (ticks === 0) ticks = run
      else if (run === ticks) ticks = 0
      out += text.slice(index, index + run)
      index += run
      continue
    }
    if (char === '\\' && index + 1 < text.length) {
      const next = text[index + 1]
      const spec = DELIMITERS[next]
      if (spec && ticks === 0) {
        const end = text.indexOf(spec.close, index + 2)
        if (end >= 0) {
          const body = text.slice(index + 2, end).replace(/^\n+|\n+$/g, '')
          const block = !spec.inline && (body.includes('\n') || lineStartIsBlank(text, index) && lineEndIsBlank(text, end + 2))
          if (block) out += `$$\n${body}\n$$`
          else out += `${out.endsWith('$$') ? ' ' : ''}$$${body}$$`
          index = end + 2
          continue
        }
      }
      out += text.slice(index, index + 2)
      index += 2
      continue
    }
    out += char
    index += 1
  }
  return out
}

export function normalizeMathDelimiters(source: string): string {
  if (!source.includes('\\(') && !source.includes('\\[')) return source
  const lines = source.split('\n')
  const out: string[] = []
  let fence: string | undefined
  let pending: string[] = []
  const flush = () => {
    if (pending.length === 0) return
    out.push(replaceInline(pending.join('\n')))
    pending = []
  }
  for (const line of lines) {
    if (fence) {
      out.push(line)
      const close = FENCE_CLOSE.exec(line)
      if (close && close[1][0] === fence[0] && close[1].length >= fence.length) fence = undefined
      continue
    }
    const open = FENCE_OPEN.exec(line)
    if (open && !(open[1][0] === '`' && line.slice(open[0].length).includes('`'))) {
      flush()
      out.push(line)
      fence = open[1]
      continue
    }
    pending.push(line)
  }
  flush()
  return out.join('\n')
}
