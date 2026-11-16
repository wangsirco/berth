import { expect, it } from 'vitest'
import { signalFlag } from './signalFlag'

it('is deterministic', () => {
  expect(signalFlag('mini', false)).toEqual(signalFlag('mini', false))
})
it('differs between names', () => {
  const names = ['mini', 'prod', 'nas', 'pve', 'vps-hk', 'lab', 'router', 'pi']
  const set = new Set(names.map(n => JSON.stringify(signalFlag(n, false))))
  expect(set.size).toBeGreaterThanOrEqual(7)
})
it('never puts the same color side by side', () => {
  for (const n of ['a', 'b', 'c', 'mini', 'prod', 'x1', 'x2', 'x3']) {
    const { colors } = signalFlag(n, false)
    for (let i = 1; i < colors.length; i++) expect(colors[i]).not.toBe(colors[i - 1])
  }
})
it('uses dark variants', () => {
  const light = signalFlag('mini', false).colors, dark = signalFlag('mini', true).colors
  expect(light.length).toBe(dark.length)
  expect(dark).not.toContain('#ffffff')
})
