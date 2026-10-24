import { memo } from 'react'
import { useI18n } from '../../../i18n/index'

export type NestedCall = { text: string; isError: boolean }

const PREFIXES = ['Script completed', 'Script running'] as const

export function nestedCalls(result: string): NestedCall[] | null {
  const text = result.trim()
  let rest = text
  for (const prefix of PREFIXES) {
    if (rest.startsWith(prefix)) {
      rest = rest.slice(prefix.length).trimStart()
      break
    }
  }
  if (!rest.startsWith('{') && !rest.startsWith('[')) return null
  const calls: NestedCall[] = []
  let index = 0
  while (index < rest.length) {
    while (index < rest.length && /\s/.test(rest[index])) index++
    if (index >= rest.length) break
    const end = jsonValueEnd(rest, index)
    if (end < 0) return null
    let value: unknown
    try {
      value = JSON.parse(rest.slice(index, end))
    } catch {
      return null
    }
    index = end
    const envelope = value as { content?: { text?: string }[]; isError?: boolean }
    const body = Array.isArray(envelope.content)
      ? envelope.content.map(block => block?.text ?? '').join('')
      : typeof value === 'string' ? value : JSON.stringify(value)
    calls.push({ text: body, isError: !!envelope.isError })
  }
  return calls.length ? calls : null
}

function jsonValueEnd(text: string, start: number): number {
  const open = text[start]
  if (open !== '{' && open !== '[') return -1
  const stack: string[] = [open === '{' ? '}' : ']']
  let inString = false
  let escaped = false
  for (let i = start + 1; i < text.length; i++) {
    const ch = text[i]
    if (inString) {
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === '"') inString = false
      continue
    }
    if (ch === '"') {
      inString = true
      continue
    }
    if (ch === '{') stack.push('}')
    else if (ch === '[') stack.push(']')
    else if (ch === '}' || ch === ']') {
      if (stack.pop() !== ch) return -1
      if (stack.length === 0) return i + 1
    }
  }
  return -1
}

export const CodeModeResult = memo(function CodeModeResult({ result, testId }: { result: string; testId?: string }) {
  const { t } = useI18n()
  const calls = nestedCalls(result)
  if (!calls) return null
  return (
    <div className="cell-result" data-testid={testId ?? 'cell-result'}>
      {calls.map((call, index) => (
        <div className={`cell-call${call.isError ? ' err' : ''}`} key={index} data-testid="cell-call">
          <div className="cell-call-head">
            <span className="cell-call-index">#{index + 1}</span>
            <span className={call.isError ? 'cell-call-state err' : 'cell-call-state'}>{t(call.isError ? 'tool.callFailed' : 'tool.callOk')}</span>
          </div>
          <pre className="cell-call-body">{call.text || t('tool.emptyResult')}</pre>
        </div>
      ))}
    </div>
  )
})
