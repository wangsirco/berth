import { expect, it } from 'vitest'
import { layoutBerths } from './BerthBar'
import type { Berth } from '@/lib/types'

const GB = 2 ** 30
it('merges compose projects, small items and system', () => {
  const segs = layoutBerths(([
    { type: 'container', id: 'a', name: 'n8n', project: 'n8n', mem_bytes: 0.2 * GB },
    { type: 'container', id: 'b', name: 'n8n-worker', project: 'n8n', mem_bytes: 0.1 * GB },
    { type: 'container', id: 'c', name: 'tiny1', project: '', mem_bytes: 0.05 * GB },
    { type: 'container', id: 'd', name: 'tiny2', project: '', mem_bytes: 0.05 * GB },
    { type: 'service', id: 'sshd.service', name: 'sshd', project: '', mem_bytes: 0.01 * GB },
    { type: 'kernel', id: 'k', name: 'kernel', project: '', mem_bytes: 0.5 * GB },
    { type: 'free', id: 'f', name: 'free', project: '', mem_bytes: 7.09 * GB },
  ] as Berth[]), 8 * GB)
  const labels = segs.map(s => s.label)
  expect(labels[0]).toBe('n8n')
  expect(segs[0].bytes).toBeCloseTo(0.3 * GB)
  expect(labels).toContain('…2')
  expect(segs.find(s => s.kind === 'sys')!.bytes).toBeCloseTo(0.51 * GB)
  expect(segs.at(-1)!.kind).toBe('free')
  expect(Math.round(segs.reduce((a, s) => a + s.pct, 0))).toBe(100)
})
