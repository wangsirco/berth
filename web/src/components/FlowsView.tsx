import { useState } from 'react'
import { Link } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Empty, Notice } from '@/components/ui/Box'
import { Segmented } from '@/components/ui/Field'
import { bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'

interface Peer { peer: string; ptr?: string; country?: string; conns: number; bytes: number; ports: string[] }
interface Flows { in: Peer[]; out: Peer[]; totals: Record<'in' | 'out', { bytes: number; conns: number }> }
type Range = '1h' | '24h' | '7d'

const PRIVATE = /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|fd|fe80:|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.)/

export function countryName(code: string, locale: string) {
  try { return new Intl.DisplayNames([locale], { type: 'region' }).of(code) ?? code } catch { return code }
}

// what a peer is, in words: another of our objects, this server, the LAN, or the internet
export function PeerLabel({ p }: { p: Peer }) {
  const t = useT()
  const [locale] = useLocale()
  const { host } = useHost()
  if (p.peer === 'host') return <span>{t('flows.this_server')}</span>
  const [kind, name] = p.peer.split(':', 2)
  if ((kind === 'container' || kind === 'vm') && name) {
    return <Link className="text-acc hover:underline" to={hostPath(host, kind === 'vm' ? 'vms' : 'containers/c/' + encodeURIComponent(name))}>{t('flows.kind_' + kind)} {name}</Link>
  }
  return (
    <span className="min-w-0">
      <span className="num">{p.peer}</span>
      {p.ptr && <span className="ml-1.5 text-mute">{p.ptr}</span>}
      {!p.ptr && PRIVATE.test(p.peer) && <span className="ml-1.5 text-mute">{t('flows.lan')}</span>}
      {p.country && <span className="ml-1.5 text-mute">· {countryName(p.country, locale)}</span>}
    </span>
  )
}

// FlowsView: ranked bars (one hue, figures beside them) for where an object's traffic goes
// and where it comes from.
export function FlowsView({ obj }: { obj: string }) {
  const t = useT()
  const { host } = useHost()
  const [range, setRange] = useState<Range>('24h')
  const d = useApi<Flows>(`/api/v1/hosts/${host}/flows?obj=${encodeURIComponent(obj)}&range=${range}`, [host, obj, range])
  return (
    <div className="space-y-4 p-4 text-[13px]">
      <div className="flex items-center gap-2">
        <span className="text-mute">{t('flows.hint')}</span>
        <span className="ml-auto"><Segmented<Range> value={range} onChange={setRange} options={(['1h', '24h', '7d'] as Range[]).map((r) => ({ value: r, label: t('range.' + r) }))} /></span>
      </div>
      {d.error && <Notice tone="bad">{d.error.message}</Notice>}
      {d.data && (['out', 'in'] as const).map((dir) => { const data = d.data!; return (
        <section key={dir}>
          <div className="mb-1.5 flex items-baseline gap-2">
            <h3 className="font-semibold">{t('flows.' + dir)}</h3>
            <span className="num text-[12px] text-mute">{t('flows.total', { bytes: bytes(data.totals[dir].bytes), conns: data.totals[dir].conns })}</span>
          </div>
          {data[dir].length === 0 ? <Empty title={t('flows.none_' + dir)} /> : <Bars list={data[dir]} />}
        </section>
      ) })}
    </div>
  )
}

function Bars({ list }: { list: Peer[] }) {
  const t = useT()
  const top = Math.max(...list.map((p) => p.bytes), 1)
  return (
    <div className="space-y-1.5">
      {list.slice(0, 15).map((p) => (
        <div key={p.peer} className="grid grid-cols-[minmax(0,1fr)_5.5rem] items-center gap-x-3 gap-y-0.5" title={`${p.peer} · ${bytes(p.bytes)} · ${t('flows.conns', { n: p.conns })} · ${p.ports.join(' ')}`}>
          <div className="truncate"><PeerLabel p={p} /></div>
          <div className="num text-right text-[12px]">{bytes(p.bytes)}</div>
          <div className="h-1.5 overflow-hidden rounded-full bg-soft"><div className="h-full rounded-full bg-acc" style={{ width: Math.max(1, (p.bytes / top) * 100) + '%' }} /></div>
          <div className="num truncate text-right text-[11px] text-mute">{p.ports.slice(0, 3).join(' ')}</div>
        </div>
      ))}
      {list.length > 15 && <div className="text-[12px] text-mute">{t('flows.more', { n: list.length - 15 })}</div>}
    </div>
  )
}
