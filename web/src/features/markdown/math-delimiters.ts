const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/
const DISPLAY_ONE_LINE = /^([ \t]*)\$\$((?:(?!\$\$)[^\n])+?)\$\$([ \t]*)$/
const DISPLAY_OPEN = /^([ \t]*)\$\$(?=\S)/
const DISPLAY_CLOSE = /\S\$\$[ \t]*$/
const DELIMITERS: Record<string, { close: string; inline: boolean }> = {
  '(': { close: '\\)', inline: true },
  '[': { close: '\\]', inline: false },
}

const CJK = /[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3000-\u303f\uff00-\uffef]/
const OPEN_CONTEXT = /[\s(（《“‘[*_~]/

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

function splitFence(source: string): Array<{ text: string; fenced: boolean }> {
  const blocks: Array<{ text: string; fenced: boolean }> = []
  let fence: string | undefined
  let buffer: string[] = []
  let fenced = false
  const flush = () => {
    if (buffer.length > 0) blocks.push({ text: buffer.join('\n'), fenced })
    buffer = []
  }
  for (const line of source.split('\n')) {
    if (fence) {
      buffer.push(line)
      const close = FENCE_CLOSE.exec(line)
      if (close && close[1][0] === fence[0] && close[1].length >= fence.length) {
        fence = undefined
        flush()
        fenced = false
      }
      continue
    }
    const open = FENCE_OPEN.exec(line)
    if (open && !(open[1][0] === '`' && line.slice(open[0].length).includes('`'))) {
      flush()
      fence = open[1]
      fenced = true
      buffer.push(line)
      continue
    }
    buffer.push(line)
  }
  flush()
  return blocks
}

function codeSpanRanges(text: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = []
  let index = 0
  while (index < text.length) {
    if (text[index] !== '`') {
      index += 1
      continue
    }
    const run = backticks(text, index)
    const close = text.indexOf('`'.repeat(run), index + run)
    if (close < 0) {
      index += run
      continue
    }
    ranges.push([index, close + run])
    index = close + run
  }
  return ranges
}

function inRanges(ranges: Array<[number, number]>, at: number): boolean {
  return ranges.some(([start, end]) => at >= start && at < end)
}

function dollarPairs(text: string, code: Array<[number, number]>): Array<[number, number]> {
  const pairs: Array<[number, number]> = []
  let index = 0
  while (index < text.length) {
    if (text[index] !== '$') {
      index += 1
      continue
    }
    if (text[index + 1] === '$' || (index > 0 && text[index - 1] === '\\') || inRanges(code, index)) {
      index += 1
      continue
    }
    if (index > 0 && !OPEN_CONTEXT.test(text[index - 1]) && !CJK.test(text[index - 1])) {
      index += 1
      continue
    }
    const close = text.indexOf('$', index + 1)
    if (close < 0) break
    const body = text.slice(index + 1, close)
    const usable = body.length > 0
      && !body.includes('\n')
      && body === body.trim()
      && text[close + 1] !== '$'
      && !inRanges(code, close)
    if (!usable) {
      index = close + 1
      continue
    }
    pairs.push([index, close])
    index = close + 1
  }
  return pairs
}

function convertInlineDollars(text: string): string {
  const code = codeSpanRanges(text)
  const pairs = dollarPairs(text, code)
  if (pairs.length === 0) return text
  let out = ''
  let index = 0
  let next = 0
  while (index < text.length) {
    const span = code.find(([start]) => start === index)
    if (span) {
      out += text.slice(span[0], span[1])
      index = span[1]
      continue
    }
    const pair = pairs[next]
    if (pair && pair[0] === index) {
      out += `$$${text.slice(pair[0] + 1, pair[1])}$$`
      index = pair[1] + 1
      next += 1
      continue
    }
    out += text[index]
    index += 1
  }
  return out
}

function convertDelimiters(text: string): string {
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
          else out += `${out.endsWith('$$') || out.endsWith('\u0001') ? ' ' : ''}\u0001${body}\u0001`
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

function displayOpen(line: string): boolean {
  const pad = /^[ \t]*/.exec(line)![0]
  const rest = line.slice(pad.length)
  if (!rest.startsWith('$$') || rest.length < 4) return false
  let count = 0
  for (let index = 0; index < rest.length; index += 1) {
    if (rest[index] === '$' && rest[index + 1] === '$') {
      count += 1
      index += 1
    }
  }
  return count % 2 === 1
}

function splitDisplayBlocks(text: string): string {
  const lines = text.split('\n')
  const out: string[] = []
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index]
    const oneLine = DISPLAY_ONE_LINE.exec(line)
    if (oneLine) {
      out.push(`${oneLine[1]}$$`, oneLine[2], `$$${oneLine[3]}`)
      continue
    }
    if (!displayOpen(line)) {
      out.push(line)
      continue
    }
    let close = index
    while (close < lines.length && !DISPLAY_CLOSE.test(lines[close])) close += 1
    if (close >= lines.length) {
      out.push(line)
      continue
    }
    const pad = /^[ \t]*/.exec(line)![0]
    if (close === index) {
      out.push(`${pad}$$`, line.slice(pad.length + 2), '$$')
    } else {
      out.push(`${pad}$$`, line.slice(pad.length + 2))
      for (let inner = index + 1; inner < close; inner += 1) out.push(lines[inner])
      out.push(lines[close].replace(/[ \t]*$/, '').slice(0, -2), '$$')
    }
    index = close
  }
  return out.join('\n')
}

export function normalizeMathDelimiters(source: string): string {
  if (!source.includes('$') && !source.includes('\\(') && !source.includes('\\[')) return source
  const blocks = splitFence(source)
  const converted = blocks.map(block => {
    if (block.fenced) return block.text
    return splitDisplayBlocks(convertInlineDollars(convertDelimiters(block.text)))
  })
  return converted.join('\n').replaceAll('\u0001', () => '$$')
}
