/** Fixed tenths for short durations, whole seconds for every longer duration. */
export function durationText(milliseconds: number): string {
  const ms = Number.isFinite(milliseconds) ? Math.max(0, milliseconds) : 0
  if (ms < 1000) return `${ms.toFixed(1)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const seconds = Math.floor(ms / 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  const tail = `${pad(seconds % 60)}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m${tail}`
  const minutes = `${pad(Math.floor(seconds / 60) % 60)}m${tail}`
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h${minutes}`
  return `${Math.floor(seconds / 86_400)}d${pad(Math.floor(seconds / 3600) % 24)}h${minutes}`
}

/** Reserve only the current format's digits, not a days-wide slot for seconds. */
export function durationColumns(text: string): number {
  if (text.includes('d')) return text.length
  const columns = text.endsWith('ms') ? 7 : text.includes('h') ? 9 : text.includes('m') ? 6 : 5
  return Math.max(columns, text.length)
}
