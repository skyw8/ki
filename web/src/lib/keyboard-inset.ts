// Why: iOS Safari ignores `interactive-widget=resizes-content` and never
// shrinks the layout viewport for the software keyboard, so a height-locked
// `100dvh` app leaves its bottom composer and dialog footers under the
// keyboard. The visible band is only reported through `visualViewport`; the
// difference between it and the layout viewport is the inset the app must
// pad away. On Chrome (which honors resizes-content) both heights shrink
// together, so the same formula yields 0 and stays a no-op there.
export type ViewportSample = {
  innerHeight: number
  height: number
  scale: number
}

export function keyboardInset(sample: ViewportSample, editableFocused: boolean): number {
  // The keyboard only exists while an editable control holds focus; gating on
  // it keeps pinch-zoom (which also shrinks the visual viewport) from lifting
  // the composer. A zoomed page (scale > 1) is panned by the browser itself.
  if (!editableFocused || sample.scale > 1.05) return 0
  // Why: offsetTop is deliberately NOT subtracted. On platforms without
  // resizes-content the browser answers the keyboard by panning the visual
  // viewport over a full-height layout; crediting that pan would leave inset
  // at 0 and full-screen dialogs taller than the visible band (the user ends
  // up dragging the dialog around). Shrink into the band instead, and let the
  // reveal converge the pan back to zero.
  return Math.max(0, Math.round(sample.innerHeight - sample.height))
}

/**
 * Reports whether a focused field sits outside the visible band above the
 * keyboard (band edges in client coordinates). A field taller than the band
 * counts as visible while its top edge is in view: the caret is what matters,
 * and yanking a tall editor to fit its bottom edge would be worse.
 */
export function fieldOutsideBand(top: number, bottom: number, bandTop: number, bandBottom: number, margin = 8): boolean {
  if (bottom <= top) return false
  if (bottom - top >= bandBottom - bandTop) return top < bandTop + margin || top > bandBottom - margin
  return top < bandTop + margin || bottom > bandBottom - margin
}
