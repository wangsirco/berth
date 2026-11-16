import { useEffect, useState } from 'react'
import { AreaChart, Spark } from '@/components/charts/Spark'
import { Segmented } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { bytes, duration, pct, rate } from '@/lib/format'
import { useT } from '@/lib/i18n'
import type { Host, Point } from '@/lib/types'

type Range = '1h' | '24h' | '7d' | '30d'
const SERIES = ['cpu', 'mem_used', 'net_rx', 'net_tx']

export function ResourcePanel({ host, hostName, live }: { host?: Host; hostName: string; live: Host[] }) {
  const t = useT()
  const [range, setRange] = useState<Range>('1h')
  const [series, setSeries] = useState<Record<string, Point[]>>({})
  useEffect(() => {
    let alive = true
    const load = () => get<{ series: Record<string, Point[]> }>(`/api/v1/hosts/${hostName}/metrics?series=${SERIES.join(',')}&range=${range}&points=120`)
      .then((r) => alive && setSeries(r.series)).catch(() => {})
    void load()
    const id = setInterval(load, range === '1h' ? 60000 : 300000)
    return () => { alive = false; clearInterval(id) }
  }, [hostName, range])
  // in the 1-hour view, live 1 s samples extend the line
  const withLive = (k: string, pick: (h: Host) => number) => {
    const base = (series[k] ?? []).map((p) => p.v)
    return range === '1h' ? [...base, ...live.map(pick)].slice(-180) : base
  }
  if (!host) return null
  const root = host.disks.find((d) => d.mount === '/') ?? host.disks[0]
  const rx = host.net.reduce((a, n) => a + n.rx_bps, 0)
  const tx = host.net.reduce((a, n) => a + n.tx_bps, 0)
  const row = (label: string, chart: React.ReactNode, value: React.ReactNode) => (
    <div className="grid grid-cols-[54px_1fr] items-center gap-x-2 gap-y-0.5 text-[13px]">
      <span className="text-mute">{label}</span>{chart ?? value}{chart && <><span />{value}</>}
    </div>
  )
  return (
    <section aria-label={t('overview.resources')}>
      <div className="mb-2 flex items-center justify-between">
        <h2 className="text-[13.5px] font-semibold">{t('overview.resources')}</h2>
        <Segmented<Range> value={range} onChange={setRange} options={(['1h', '24h', '7d', '30d'] as Range[]).map((r) => ({ value: r, label: t('range.' + r) }))} />
      </div>
      <div className="space-y-3">
        {row('CPU', range === '1h' ? <Spark values={withLive('cpu', (h) => h.cpu)} max={100} height={24} label="CPU" /> : <AreaChart points={series.cpu ?? []} max={100} height={40} format={(v) => pct(v)} />,
          <b className="num font-medium">{pct(host.cpu)} · {t('overview.cores', { n: host.cores })} · {t('overview.load')} {host.load[0].toFixed(2)}</b>)}
        {row(t('overview.memory'), range === '1h' ? <Spark values={withLive('mem_used', (h) => h.mem_used)} max={host.mem_total} height={24} label={t('overview.memory')} /> : <AreaChart points={series.mem_used ?? []} max={host.mem_total} height={40} format={bytes} />,
          <b className="num font-medium">{bytes(host.mem_used)} / {bytes(host.mem_total)}</b>)}
        {root && row(t('overview.disk'), <div className="h-1.5 overflow-hidden rounded-full bg-soft"><div className="h-full bg-acc" style={{ width: (root.used / root.total) * 100 + '%' }} /></div>,
          <b className="font-medium">{bytes(root.used)} / {bytes(root.total)}<span className="font-normal text-mute"> · {root.fs}</span></b>)}
        {row(t('overview.network'), range === '1h' ? <Spark values={withLive('net_rx', (h) => h.net.reduce((a, n) => a + n.rx_bps, 0))} height={24} label={t('overview.network')} /> : <AreaChart points={series.net_rx ?? []} height={40} format={rate} />,
          <b className="num font-medium">↓ {rate(rx)}　↑ {rate(tx)}</b>)}
        {row(t('overview.uptime'), null, <b className="font-medium">{duration(host.uptime, t)}</b>)}
        {host.temps.length > 0 && row(t('overview.temp'), null, <b className="num font-medium">{Math.max(...host.temps.map((x) => x.c)).toFixed(0)} °C</b>)}
      </div>
    </section>
  )
}
