import type { TFn } from './i18n'

export function bytes(n: number | undefined | null): string {
  if (n === undefined || n === null || !isFinite(n)) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (Math.abs(v) >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return i === 0 ? `${v} B` : `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))} ${u[i]}`
}

export const rate = (bps: number | undefined) => (bps === undefined ? '—' : bytes(bps) + '/s')
export const pct = (v: number | undefined, digits = 0) => (v === undefined ? '—' : `${v.toFixed(digits)}%`)

export function duration(sec: number, t: TFn): string {
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  if (d > 0) return t('time.days_hours', { d, h })
  if (h > 0) return t('time.hours_minutes', { h, m })
  if (m > 0) return t('time.minutes', { m })
  return t('time.seconds', { s: Math.max(0, Math.floor(sec)) })
}

export function ago(ts: number, t: TFn, now = Date.now() / 1000): string {
  const s = Math.max(0, now - ts)
  if (s < 45) return t('common.just_now')
  if (s < 3600) return t('common.minutes_ago', { n: Math.round(s / 60) })
  if (s < 86400) return t('common.hours_ago', { n: Math.round(s / 3600) })
  if (s < 86400 * 30) return t('common.days_ago', { n: Math.round(s / 86400) })
  return new Date(ts * 1000).toLocaleDateString()
}

export const clock = (ts: number) => new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
export const date = (ts: number) => new Date(ts * 1000).toLocaleDateString([], { year: 'numeric', month: '2-digit', day: '2-digit' })
export const datetime = (ts: number) => new Date(ts * 1000).toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
