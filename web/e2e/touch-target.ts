// Chromium rounds layout boxes to 1/64px, so a control styled at exactly 40px
// can measure 39.999999 in a bounding box. That rounding must not fail the 40px
// touch contract, so every assertion compares against this floor instead of 40.
export const MIN_TOUCH_SIZE = 40 - 0.05
