import { describe, expect, it } from 'vitest'
import { navGroups } from './nav'

describe('navGroups', () => {
  it('shows every group to someone who may see everything', () => {
    expect(navGroups(() => true).map((g) => g.key)).toEqual(['home', 'mine', 'net', 'build', 'protect', 'server'])
  })
  it('drops items and then empty groups the user may not see', () => {
    const can = (p?: string) => !p || p === 'overview.view' || p === 'sites.view'
    const g = navGroups(can)
    const mine = g.find((x) => x.key === 'mine')
    expect(mine?.items.map((i) => i.key)).toEqual(['sites', 'projects'])
    expect(g.find((x) => x.key === 'net')?.items.map((i) => i.key)).toEqual(['domains'])
    expect(g.find((x) => x.key === 'build')).toBeUndefined()
    // monitor and tokens need no permission, so their groups stay
    expect(g.find((x) => x.key === 'protect')?.items.map((i) => i.key)).toEqual(['monitor'])
    expect(g.find((x) => x.key === 'server')?.items.map((i) => i.key)).toEqual(['tokens'])
  })
})
