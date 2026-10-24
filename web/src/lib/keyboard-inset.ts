export type ViewportSample = {
  innerHeight: number
  height: number
  scale: number
}

export function keyboardInset(sample: ViewportSample, editableFocused: boolean): number {
  if (!editableFocused || sample.scale > 1.05) return 0
  return Math.max(0, Math.round(sample.innerHeight - sample.height))
}

export function fieldOutsideBand(top: number, bottom: number, bandTop: number, bandBottom: number, margin = 8): boolean {
  if (bottom <= top) return false
  if (bottom - top >= bandBottom - bandTop) return top < bandTop + margin || top > bandBottom - margin
  return top < bandTop + margin || bottom > bandBottom - margin
}
