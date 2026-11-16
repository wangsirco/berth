import { useState } from 'react'
import { Segmented } from '@/components/ui/Field'
import { Empty } from '@/components/ui/Box'
import { cx } from '@/components/ui/cx'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

interface Point { ts: number; req: number; bytes: number; e4: number; e5: number; p95: number }
interface KV { k: string; n: number }
export interface Stats {
  range: string; step: number; series: Point[]
  total: { req: number; bytes: number; uniq: number; status: Record<string, number>; bots: number; scans: number; p50: number; p95: number }
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
            {[[t('stats.requests'), fmtN(s.total.req)], [t('stats.visitors'), fmtN(s.total.uniq)], [t('stats.traffic'), bytes(s.total.bytes)], ['p95', s.total.p95 + ' ms']].map(([k, v]) => (
              <div key={k}><div className="text-[12px] text-mute">{k}</div><div className="num text-[18px] font-semibold">{v}</div></div>
            ))}
          </div>
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
