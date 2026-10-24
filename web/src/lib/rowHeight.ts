const bodyTokens = new WeakMap<object, number>()
let nextBodyToken = 1

export function rowBodyToken(body: object | undefined): number {
  if (!body) return 0
  let token = bodyTokens.get(body)
  if (token === undefined) {
    token = nextBodyToken++
    bodyTokens.set(body, token)
  }
  return token
}

export type RowHeightRevision = { body: number; decoration: string }

export function sameRowRevision(a: RowHeightRevision, b: RowHeightRevision): boolean {
  return a.body === b.body && a.decoration === b.decoration
}

const heights = new Map<string, { w: number; h: number; chars: number; revision: RowHeightRevision }>()
const CACHE_LIMIT = 2000
const MIN_ESTIMATE = 64
const MAX_ESTIMATE = 2400
export const COLLAPSED_TOOL_HEIGHT = 68
export const UNKNOWN_WIDTH = 0

let pxPerChar = 0.75
let samples = 0

function clamp(n: number): number {
  return Math.min(MAX_ESTIMATE, Math.max(MIN_ESTIMATE, Math.round(n)))
}

export function rememberRowHeight(key: string, width: number, height: number, chars: number, revision: RowHeightRevision): void {
  if (!key || !Number.isFinite(height) || height <= 0) return
  const h = height
  heights.delete(key)
  heights.set(key, { w: width, h, chars, revision })
  if (heights.size > CACHE_LIMIT) {
    const oldest = heights.keys().next().value
    if (oldest !== undefined) heights.delete(oldest)
  }
  if (chars > 0) {
    const sample = Math.min(6, Math.max(0.2, h / chars))
    pxPerChar = samples === 0 ? sample : pxPerChar * 0.97 + sample * 0.03
    samples += 1
  }
}

export function rowHeightEstimate(key: string, width: number, chars: number, revision: RowHeightRevision, fallback?: number): number {
  const hit = heights.get(key)
  if (hit && sameRowRevision(hit.revision, revision) && hit.chars === chars && width !== UNKNOWN_WIDTH && hit.w === width) return hit.h
  return fallback ?? clamp(56 + chars * pxPerChar)
}

export function textHeightHint(text: string): number {
  return clamp(56 + text.length * pxPerChar)
}

export function forgetRowHeights(): void {
  heights.clear()
  pxPerChar = 0.75
  samples = 0
}
