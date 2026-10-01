import { expect, test } from 'bun:test'
import { CATEGORY_COLORS } from '../src/features/context/palette'
import { CATEGORY_ORDER } from '../src/features/context/model'

const rgb = (hex: string) => [1, 3, 5].map(offset => parseInt(hex.slice(offset, offset + 2), 16))
const distance = (a: number[], b: number[]) => Math.hypot(...a.map((channel, index) => channel - b[index]))

test('human inputs are warm red and tool results are cool teal, not two neighboring greens', () => {
  const human = rgb(CATEGORY_COLORS.human)
  const tool = rgb(CATEGORY_COLORS.tool)
  expect(human[0] - Math.max(human[1], human[2])).toBeGreaterThan(100)
  expect(Math.min(tool[1], tool[2]) - tool[0]).toBeGreaterThan(100)
  expect(distance(human, tool)).toBeGreaterThan(190)
})

test('every category has its own swatch without near-identical RGB aliases', () => {
  expect(Object.keys(CATEGORY_COLORS)).toEqual([...CATEGORY_ORDER])
  const colors = Object.values(CATEGORY_COLORS).map(rgb)
  for (let i = 0; i < colors.length; i++) {
    for (let j = i + 1; j < colors.length; j++) {
      // This guards accidental aliases, not a claim of color-vision accessibility.
      expect(distance(colors[i], colors[j])).toBeGreaterThan(50)
    }
  }
})
