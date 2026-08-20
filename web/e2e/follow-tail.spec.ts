import { expect, test } from '@playwright/test'
import { applyFollowTail, followFromGap } from '../src/follow-tail.ts'

test('applyFollowTail writes scrollTop to scrollHeight only when follow is on', () => {
  const el = { scrollHeight: 800, scrollTop: 12 }
  expect(applyFollowTail(el, true)).toBe(true)
  expect(el.scrollTop).toBe(800)
  const paused = { scrollHeight: 800, scrollTop: 40 }
  expect(applyFollowTail(paused, false)).toBe(false)
  expect(paused.scrollTop).toBe(40)
  expect(applyFollowTail(null, true)).toBe(false)
})

test('followFromGap uses hysteresis so a small drag does not re-stick', () => {
  expect(followFromGap(0, false)).toBe(true)
  expect(followFromGap(16, false)).toBe(true)
  expect(followFromGap(17, true)).toBe(true)
  expect(followFromGap(17, false)).toBe(false)
  expect(followFromGap(80, true)).toBe(true)
  expect(followFromGap(81, true)).toBe(false)
})
