import { expect, it, vi } from 'vitest'
vi.mock('@/lib/useEvents', () => ({ useEvents: () => {} }))
import { groupBySession } from './AuditPage'
import type { Change } from '@/lib/types'

const c = (id: string, session?: string) => ({ id, actor: { ai_session: session } }) as unknown as Change

it('groups consecutive changes of one AI session', () => {
  const g = groupBySession([c('1', 's'), c('2', 's'), c('3'), c('4', 't')])
  expect(g).toHaveLength(3)
  expect('session' in g[0] && g[0].items.map((x) => x.id)).toEqual(['1', '2'])
  expect((g[1] as Change).id).toBe('3')
  expect((g[2] as Change).id).toBe('4') // a single change stays a plain row
})
