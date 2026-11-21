import { useState } from 'react'
import { Segmented } from '@/components/ui/Field'
import { Empty } from '@/components/ui/Box'
import { cx } from '@/components/ui/cx'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

interface Point { ts: number; req: number; bytes: number; e4: number; e5: number; p95: number; uniq?: number; humans?: number; bots?: number; new?: number }
interface KV { k: string; n: number }
export interface Stats {
  range: string; step: number; series: Point[]
  total: { req: number; bytes: number; uniq: number; status: Record<string, number>; bots: number; scans: number; p50: number; p95: number; humans?: number; human_req?: number; new?: number; returning?: number; bot_kinds?: Record<string, number> }
  countries: KV[]; paths: KV[]; refs: KV[]; scan_paths: KV[]; slow: { ts: number; method: string; path: string; status: number; ms: number }[]
  geo_credit: string
}

const fmtN = (n: number) => (n >= 10000 ? (n / 1000).toFixed(1) + 'k' : String(n))

// Heat: GitHub-style squares, 7 days × 24 hours (from an hourly 7d series).
export function Heat({ series }: { series: Point[] }) {
  const t = useT()
  const max = Math.max(1, ...series.map((p) => p.req))
  const level = (v: number) => (v === 0 ? 0 : Math.min(4, Math.ceil((v / max) * 4)))
  const days: Point[][] = []
  for (let i = 0; i < series.length; i += 24) days.push(series.slice(i, i + 24))
  return (
    <div>
      <div className="flex flex-col gap-[3px]" role="img" aria-label={t('stats.heat_label')}>
        {days.map((d, i) => (
          <div key={i} className="flex gap-[3px]">
            {d.map((p) => <i key={p.ts} title={`${new Date(p.ts * 1000).toLocaleString([], { weekday: 'short', hour: '2-digit' })} · ${p.req}`}
              className={cx('size-[11px] rounded-[2px]', ['bg-line', 'bg-acc/25', 'bg-acc/50', 'bg-acc/75', 'bg-acc'][level(p.req)])} />)}
          </div>
        ))}
      </div>
      <div className="mt-1 text-[11.5px] text-mute">{t('stats.heat_legend')}</div>
    </div>
  )
}

function Bars({ series }: { series: Point[] }) {
  const max = Math.max(1, ...series.map((p) => p.req))
  return (
    <div className="flex h-20 items-end gap-px" aria-hidden>
      {series.map((p) => (
        <div key={p.ts} className="relative flex-1" style={{ height: `${(p.req / max) * 100}%` }} title={`${new Date(p.ts * 1000).toLocaleString()} · ${p.req}`}>
          <div className="absolute inset-0 rounded-t-[1px] bg-acc/70" />
          {p.e5 > 0 && <div className="absolute inset-x-0 bottom-0 bg-bad" style={{ height: `${(p.e5 / p.req) * 100}%` }} />}
        </div>
      ))}
    </div>
  )
}

function Top({ title, items, mono }: { title: string; items: KV[]; mono?: boolean }) {
  const max = Math.max(1, ...items.map((x) => x.n))
  return (
    <div>
      <h4 className="mb-1 text-[12.5px] font-semibold">{title}</h4>
      {items.length === 0 ? <p className="text-[12px] text-mute">—</p> : items.slice(0, 8).map((x) => (
        <div key={x.k} className="relative mb-0.5 flex justify-between gap-2 px-1 text-[12.5px]">
          <i className="absolute inset-y-0 left-0 rounded-[2px] bg-tint" style={{ width: `${(x.n / max) * 100}%` }} />
          <span className={cx('relative truncate', mono && 'num')}>{x.k}</span><span className="num relative text-mute">{fmtN(x.n)}</span>
        </div>
      ))}
    </div>
  )
}

export function StatsView({ host, siteId }: { host: string; siteId: number }) {
  const t = useT()
  const [range, setRange] = useState<'1h' | '24h' | '7d' | '30d'>('24h')
  const st = useApi<Stats>(`/api/v1/hosts/${host}/sites/${siteId}/stats?range=${range}`, [range, siteId])
  const s = st.data
  return (
    <div className="space-y-4 p-4">
      <Segmented value={range} onChange={setRange} options={(['1h', '24h', '7d', '30d'] as const).map((r) => ({ value: r, label: t('stats.range_' + r) }))} />
      {s && s.total.req === 0 ? <Empty title={t('stats.empty')}>{t('stats.empty_hint')}</Empty> : s && (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {[[t('stats.people'), fmtN(s.total.humans ?? s.total.uniq), t('stats.people_d')], [t('stats.new'), fmtN(s.total.new ?? 0), t('stats.returning', { n: fmtN(s.total.returning ?? 0) })],
              [t('stats.requests'), fmtN(s.total.req), t('stats.from_people', { n: fmtN(s.total.human_req ?? 0) })], [t('stats.traffic'), bytes(s.total.bytes), 'p95 ' + s.total.p95 + ' ms']].map(([k, v, sub]) => (
              <div key={k}><div className="text-[12px] text-mute">{k}</div><div className="num text-[18px] font-semibold">{v}</div><div className="text-[11.5px] text-mute">{sub}</div></div>
            ))}
          </div>
          <Who s={s} />
          {range === '7d' ? <Heat series={s.series} /> : <Bars series={s.series} />}
          <div className="flex flex-wrap gap-2 text-[12.5px]">
            {['2xx', '3xx', '4xx', '5xx'].map((c) => <span key={c} className={cx('num', c === '5xx' && (s.total.status[c] ?? 0) > 0 && 'text-bad')}>{c} {fmtN(s.total.status[c] ?? 0)}</span>)}
            <span className="text-mute">· {t('stats.bots', { n: fmtN(s.total.bots) })} · {t('stats.scans', { n: fmtN(s.total.scans) })}</span>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Top title={t('stats.paths')} items={s.paths} mono />
            <Top title={t('stats.countries')} items={s.countries.map((c) => ({ k: c.k === 'LAN' ? t('stats.lan') : c.k, n: c.n }))} />
            <Top title={t('stats.refs')} items={s.refs} />
            <Top title={t('stats.scan_paths')} items={s.scan_paths} mono />
          </div>
          {s.slow?.length > 0 && <div>
            <h4 className="mb-1 text-[12.5px] font-semibold">{t('stats.slow')}</h4>
            {s.slow.map((x, i) => <div key={i} className="num flex gap-2 text-[12px]"><span className="text-mute">{new Date(x.ts * 1000).toLocaleTimeString()}</span><span>{x.method} {x.path}</span><span className="ml-auto">{x.status} · {x.ms} ms</span></div>)}
          </div>}
          <p className="text-[11.5px] text-mute">{s.geo_credit}</p>
        </>
      )}
    </div>
  )
}

// Who: people against programs, and which programs.
function Who({ s }: { s: Stats }) {
  const t = useT()
  const people = s.total.human_req ?? 0
  const kinds = Object.entries(s.total.bot_kinds ?? {}).sort((a, b) => b[1] - a[1])
  const all = people + kinds.reduce((n, [, v]) => n + v, 0)
  if (!all) return null
  const color: Record<string, string> = { people: 'bg-acc', search: 'bg-[#4f7fd0]', ai: 'bg-[#9a6fd0]', social: 'bg-[#d08a4f]', monitor: 'bg-[#6fa86f]', tool: 'bg-[#8a8a8a]', scanner: 'bg-bad', other: 'bg-[#b8b2a6]' }
  const parts: [string, number][] = [['people', people], ...kinds]
  return (
    <div>
      <h4 className="mb-1 text-[12.5px] font-semibold">{t('stats.who')}</h4>
      <div className="flex h-2.5 overflow-hidden rounded-full bg-soft" role="img" aria-label={t('stats.who')}>
        {parts.map(([k, v]) => v > 0 && <span key={k} className={color[k] ?? 'bg-mute'} style={{ width: `${(v / all) * 100}%` }} />)}
      </div>
      <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-[12px]">
        {parts.map(([k, v]) => v > 0 && <span key={k} className="flex items-center gap-1" title={t('stats.k_' + k + '_d')}><i className={cx('size-2 rounded-full', color[k] ?? 'bg-mute')} />{t('stats.k_' + k)} <span className="num text-mute">{fmtN(v)} · {Math.round((v / all) * 100)}%</span></span>)}
      </div>
    </div>
  )
}
