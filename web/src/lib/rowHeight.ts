/**
 * Row geometry for the chat transcript.
 *
 * A virtual list only knows the size of the rows it has mounted: everything
 * else sits at `estimateSize`. The first time a row scrolls into view, its real
 * height replaces that estimate, which moves every row below it — and the
 * virtualizer answers with an equal `scrollTop` adjustment. Measured against a
 * real transcript (rows 100–1400px, estimate 96px) that correction is up to
 * ~1200px per row, and while the reader scrolls up it lands on the rows they are
 * heading towards, so the gesture is swallowed: the list shifts the scroll
 * position by more than the wheel/finger moved, and the reader sees the view
 * stand still and then jump.
 *
 * Heights are therefore remembered per (item key, list width). A row that comes
 * back — after a fold-mode branch switch, a session switch, a reload — starts at
 * its real size instead of the estimate, so no correction is owed. Width is part
 * of the key because a row reflows when the viewport changes; a stale sample
 * from another width is ignored and re-measured on mount.
 *
 * Rows nobody has measured yet fall back to a px-per-character ratio learned
 * from every measurement, which keeps the first estimate in the right order of
 * magnitude instead of 14x too small. Both paths are clamped so one bad sample
 * cannot reserve a screen of empty space.
 */

/** Measured heights, keyed by item key: the value is only valid at its width. */
const heights = new Map<string, { w: number; h: number; chars: number }>()
const CACHE_LIMIT = 2000
const MIN_ESTIMATE = 64
const MAX_ESTIMATE = 2400
/** Heights are measured at a specific list width; 0 means "unknown width". */
export const UNKNOWN_WIDTH = 0

/** Learned height of one character of message text, with its sample count. */
let pxPerChar = 0.75
let samples = 0

function clamp(n: number): number {
  return Math.min(MAX_ESTIMATE, Math.max(MIN_ESTIMATE, Math.round(n)))
}

/** rememberRowHeight stores one measurement, evicting the oldest key. */
export function rememberRowHeight(key: string, width: number, height: number, chars: number): void {
  if (!key || !Number.isFinite(height) || height <= 0) return
  const h = height
  heights.delete(key)
  heights.set(key, { w: width, h, chars })
  if (heights.size > CACHE_LIMIT) {
    const oldest = heights.keys().next().value
    if (oldest !== undefined) heights.delete(oldest)
  }
  if (chars > 0) {
    // A weathered average rather than a plain mean: the ratio only has to keep
    // unmeasured rows in the right ballpark, and one huge code block should not
    // redefine it.
    const sample = Math.min(6, Math.max(0.2, h / chars))
    pxPerChar = samples === 0 ? sample : pxPerChar * 0.97 + sample * 0.03
    samples += 1
  }
}

/** rowHeightEstimate is a measured height when there is one for this width. */
export function rowHeightEstimate(key: string, width: number, chars: number): number {
  const hit = heights.get(key)
  if (hit && hit.chars === chars && (width === UNKNOWN_WIDTH || hit.w === width)) return hit.h
  return clamp(56 + chars * pxPerChar)
}

/**
 * textHeightHint reserves roughly the space a message body will need, for the
 * placeholder that stands in while its markdown is parsed.
 */
export function textHeightHint(text: string): number {
  return clamp(56 + text.length * pxPerChar)
}

/** forgetRowHeights drops every sample; used by tests. */
export function forgetRowHeights(): void {
  heights.clear()
  pxPerChar = 0.75
  samples = 0
}
