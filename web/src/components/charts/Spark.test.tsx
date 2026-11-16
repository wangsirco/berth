import { expect, it } from 'vitest'
import { sparkPoints } from './Spark'

it('spans the full width', () => {
  const p = sparkPoints([1, 2, 3, 4, 5], 100, 20).split(' ')
  expect(p).toHaveLength(5)
  expect(p[0].startsWith('0,')).toBe(true)
  expect(p[4].startsWith('100,')).toBe(true)
})

it('handles all zeros at the bottom', () => {
  const p = sparkPoints([0, 0, 0], 10, 20).split(' ')
  expect(p.every((x) => x.endsWith(',19'))).toBe(true)
})
