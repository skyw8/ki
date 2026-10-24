import { expect, test } from 'bun:test'
import { fieldOutsideBand, keyboardInset } from '../src/lib/keyboard-inset.ts'

const sample = (innerHeight: number, height: number, scale = 1) => ({ innerHeight, height, scale })

test('keyboardInset is the occluded band between layout and visual viewport', () => {
  expect(keyboardInset(sample(800, 460), true)).toBe(340)
})

test('keyboardInset ignores visual-viewport panning', () => {
  expect(keyboardInset(sample(800, 460), true)).toBe(340)
})

test('keyboardInset stays 0 where the layout viewport already resizes', () => {
  expect(keyboardInset(sample(460, 460), true)).toBe(0)
})

test('keyboardInset requires an editable focus and an unzoomed page', () => {
  expect(keyboardInset(sample(800, 460), false)).toBe(0)
  expect(keyboardInset(sample(800, 400, 2), true)).toBe(0)
})

test('keyboardInset never goes negative', () => {
  expect(keyboardInset(sample(400, 460), true)).toBe(0)
})

test('fieldOutsideBand only flags fields (partially) outside the visible band', () => {
  expect(fieldOutsideBand(300, 340, 0, 434)).toBe(false)
  expect(fieldOutsideBand(550, 590, 0, 434)).toBe(true)
  expect(fieldOutsideBand(-60, -20, 0, 434)).toBe(true)
  expect(fieldOutsideBand(12, 420, 0, 434)).toBe(false)
  expect(fieldOutsideBand(4, 430, 0, 434)).toBe(true)
  expect(fieldOutsideBand(20, 800, 0, 434)).toBe(false)
  expect(fieldOutsideBand(-40, 700, 0, 434)).toBe(true)
  expect(fieldOutsideBand(100, 100, 0, 434)).toBe(false)
})
