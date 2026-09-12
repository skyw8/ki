// Pure helpers for rendering tool rows. Kept out of Chat.tsx so the copy
// payload can be unit-tested without importing the React/markdown tree.
import type { ChatNode } from './types'

export function argStr(args: unknown, key: string): string {
  if (!args || typeof args !== 'object') return ''
  const v = (args as Record<string, unknown>)[key]
  return v == null ? '' : String(v)
}

export function firstLine(s: string): string {
  return s.split('\n').find(l => l.trim()) ?? s
}

export function prettyArgs(args: unknown): string {
  if (args == null) return ''
  if (typeof args === 'string') return args
  return JSON.stringify(args, null, 2)
}

// toolCopyText builds the row-level copy payload: the tool's full input and
// output rather than the collapsed one-line preview, which is only a summary.
// Labels mirror the expanded io-card so the pasted text stays self-describing.
export function toolCopyText(node: Extract<ChatNode, { kind: 'tool' }>, summary: string): string {
  const name = node.name
  const desc = argStr(node.args, 'description')
  const cmd = argStr(node.args, 'command')
  const content = argStr(node.args, 'content')
  const editDiff = node.details && typeof node.details === 'object'
    ? String((node.details as Record<string, unknown>).diff ?? '')
    : ''
  const patchDiff = name === 'apply_patch' && node.details && typeof node.details === 'object'
    ? (((node.details as Record<string, unknown>).changes as Array<Record<string, unknown>> | undefined) ?? []).map(c => String(c.unified_diff ?? '')).filter(Boolean).join('\n')
    : ''
  const fail = !node.running && node.isError && node.result ? firstLine(node.result) : ''
  const bodyIn = name === 'Write' ? content : name === 'Bash' ? cmd : prettyArgs(node.args)
  const bodyOut = name === 'Edit' && editDiff
    ? editDiff
    : name === 'apply_patch' && patchDiff
      ? patchDiff
      : node.result
  return [
    bodyIn ? `IN\n${bodyIn}` : '',
    bodyOut ? `OUT\n${bodyOut}` : '',
  ].filter(Boolean).join('\n\n') || desc || fail || summary
}
