import { render } from '@testing-library/react'
import { expect, it } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import { Heat } from './StatsView'
import { daysLeft, targetText, type Site } from './types'

const site = (target: Site['target']): Site => ({ id: 1, domains: ['a.example.com'], enabled: true, target, tls: { mode: 'auto' }, options: {} })

it('describes every target type', () => {
  expect(targetText(site({ type: 'container', container: 'n8n', port: 5678 }))).toBe('n8n:5678')
  expect(targetText(site({ type: 'address', upstreams: ['10.0.0.2:80', '10.0.0.3:80'] }))).toBe('10.0.0.2:80, 10.0.0.3:80')
  expect(targetText(site({ type: 'redirect', to: 'https://b.example.com' }))).toBe('https://b.example.com')
})

it('counts days left', () => {
  expect(daysLeft(new Date(Date.now() + 10.5 * 86400000).toISOString())).toBe(10)
  expect(daysLeft(new Date(Date.now() - 86400000).toISOString())).toBeLessThan(0)
})

it('draws 7 rows of 24 squares, darker for busier hours', () => {
  const series = Array.from({ length: 168 }, (_, i) => ({ ts: 1_800_000_000 + i * 3600, req: i === 5 ? 100 : i % 24 === 0 ? 0 : 10, bytes: 0, e4: 0, e5: 0, p95: 0 }))
  const { container } = render(<I18nProvider initial="zh-CN"><Heat series={series} /></I18nProvider>)
  const rows = container.querySelectorAll('[role=img] > div')
  expect(rows).toHaveLength(7)
  expect(rows[0].querySelectorAll('i')).toHaveLength(24)
  const cells = container.querySelectorAll('i')
  expect(cells[5].className.split(' ')).toContain('bg-acc')
  expect(cells[0].className).toContain('bg-line')
})
