import { expect, test } from 'bun:test'
import { fieldOutsideBand, keyboardInset } from '../src/lib/keyboard-inset.ts'

const sample = (innerHeight: number, height: number, scale = 1) => ({ innerHeight, height, scale })

test('keyboardInset is the occluded band between layout and visual viewport', () => {
  // iOS Safari: keyboard open, layout viewport unchanged.
  expect(keyboardInset(sample(800, 460), true)).toBe(340)
})

test('keyboardInset ignores visual-viewport panning', () => {
  // A browser that pans the visual viewport to reveal the focused field must
  // not neutralize the inset: the layout bottom is still under the keyboard.
  // (Pan shows up as a smaller height with an unchanged innerHeight; the
  // formula has no offsetTop to credit.)
  expect(keyboardInset(sample(800, 460), true)).toBe(340)
})

test('keyboardInset stays 0 where the layout viewport already resizes', () => {
  // Chrome with interactive-widget=resizes-content shrinks both together.
  expect(keyboardInset(sample(460, 460), true)).toBe(0)
})

test('keyboardInset requires an editable focus and an unzoomed page', () => {
  expect(keyboardInset(sample(800, 460), false)).toBe(0)
  // Pinch-zoom also shrinks the visual viewport; the browser owns panning then.
  expect(keyboardInset(sample(800, 400, 2), true)).toBe(0)
})

test('keyboardInset never goes negative', () => {
  expect(keyboardInset(sample(400, 460), true)).toBe(0)
})

test('fieldOutsideBand only flags fields (partially) outside the visible band', () => {
  // Field fully inside the band above a 410px keyboard.
  expect(fieldOutsideBand(300, 340, 0, 434)).toBe(false)
  // Field slid under the keyboard.
  expect(fieldOutsideBand(550, 590, 0, 434)).toBe(true)
  // Field scrolled above the band.
  expect(fieldOutsideBand(-60, -20, 0, 434)).toBe(true)
  // Inside the band clear of the margin still counts as visible.
  expect(fieldOutsideBand(12, 420, 0, 434)).toBe(false)
  // Edges hugging the band boundary within the margin get nudged in.
  expect(fieldOutsideBand(4, 430, 0, 434)).toBe(true)
  // A field taller than the band is governed by its top edge.
  expect(fieldOutsideBand(20, 800, 0, 434)).toBe(false)
  expect(fieldOutsideBand(-40, 700, 0, 434)).toBe(true)
  // Zero-size rects (hidden field) never trigger a scroll.
  expect(fieldOutsideBand(100, 100, 0, 434)).toBe(false)
})
