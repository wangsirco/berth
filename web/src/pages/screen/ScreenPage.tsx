import { useEffect, useState } from 'react'
import { HostCtx } from '@/components/shell/host'
import { Dot } from '@/components/ui/Box'
import { bytes, rate } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Berth, Host, HostInfo } from '@/lib/types'
import { BerthBar } from '../overview/BerthBar'

interface Overview { host: Host; berths: Berth[]; tasks: { id: string; title?: string; kind: string; status: string }[] }
interface Alert { id: number; host: string; state: string; severity: string; title?: string; started: number }
interface Check { id: number; name: string; up?: boolean; last_ms?: number; uptime_30d: number }

// The big screen (方案 §10.4 /screen): kept awake, cycles through the hosts every 30 s and
// shows each one's berth bar, traffic, task queue, firing alerts and uptime checks.
export default function ScreenPage() {
  const t = useT()
  const hosts = useApi<{ items: HostInfo[] }>('/api/v1/hosts')
  const names = ['local', ...(hosts.data?.items.map((h) => h.name).filter((n) => n !== 'local') ?? [])]
  const [i, setI] = useState(0)
  const [now, setNow] = useState(new Date())
  useInterval(() => { setI((x) => x + 1); setNow(new Date()) }, 30000)
  useInterval(() => setNow(new Date()), 1000)
  // stay awake
  useEffect(() => {
    let lock: { release: () => Promise<void> } | null = null
    const take = () => (navigator as Navigator & { wakeLock?: { request: (t: string) => Promise<{ release: () => Promise<void> }> } }).wakeLock?.request('screen').then((l) => { lock = l }).catch(() => {})
    void take()
    const vis = () => { if (document.visibilityState === 'visible') void take() }
    document.addEventListener('visibilitychange', vis)
    return () => { document.removeEventListener('visibilitychange', vis); void lock?.release() }
  }, [])
  const host = names[i % names.length]
  return (
    <HostCtx.Provider value={{ host, hosts: hosts.data?.items ?? [] }}>
      <div className="flex min-h-screen flex-col gap-4 bg-canvas p-6 text-[15px]" data-screen>
        <header className="flex items-baseline gap-4">
          <h1 className="text-[26px] font-semibold">{host}</h1>
          {names.length > 1 && <span className="text-mute">{(i % names.length) + 1} / {names.length}</span>}
          <span className="num ml-auto text-[26px]">{now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
        </header>
        <HostScreen host={host} />
        <div className="grid flex-1 gap-4 lg:grid-cols-2">
          <Alerts />
          <Uptime />
        </div>
        <footer className="text-[12px] text-mute">{t('screen.hint')}</footer>
      </div>
    </HostCtx.Provider>
  )
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return <section className="rounded-[var(--radius-box)] border border-line p-4"><h2 className="mb-3 text-[13px] font-semibold uppercase tracking-wide text-mute">{title}</h2>{children}</section>
}

function HostScreen({ host }: { host: string }) {
  const t = useT()
  const ov = useApi<Overview>(`/api/v1/hosts/${encodeURIComponent(host)}/overview`, [host])
  useInterval(() => void ov.reload(), 5000)
  const h = ov.data?.host
  if (!h) return <div className="text-mute">{t('common.loading')}</div>
  const rx = h.net.reduce((s, n) => s + n.rx_bps, 0)
  const tx = h.net.reduce((s, n) => s + n.tx_bps, 0)
  const disk = h.disks.find((d) => d.mount === '/') ?? h.disks[0]
  const tile = (k: string, v: string, sub?: string) => (
    <div className="rounded-[var(--radius-box)] border border-line p-4"><div className="text-[13px] text-mute">{k}</div><div className="num text-[30px] font-semibold leading-tight">{v}</div>{sub && <div className="num text-[13px] text-mute">{sub}</div>}</div>
  )
  return (
    <>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
        {tile('CPU', `${h.cpu.toFixed(0)}%`, `${h.cores} · ${h.load[0].toFixed(2)}`)}
        {tile(t('overview.memory'), `${((h.mem_used / h.mem_total) * 100).toFixed(0)}%`, `${bytes(h.mem_used)} / ${bytes(h.mem_total)}`)}
        {disk && tile(t('overview.disk'), `${((disk.used / disk.total) * 100).toFixed(0)}%`, `${bytes(disk.used)} / ${bytes(disk.total)}`)}
        {tile('↓', rate(rx))}
        {tile('↑', rate(tx))}
      </div>
      <Panel title={t('screen.berths')}><BerthBar berths={ov.data!.berths} host={h} /></Panel>
      <Panel title={t('screen.tasks')}>
        {ov.data!.tasks.filter((x) => x.status === 'running' || x.status === 'waiting').length === 0 ? <div className="text-mute">{t('screen.no_tasks')}</div>
          : ov.data!.tasks.filter((x) => x.status === 'running' || x.status === 'waiting').map((x) => <div key={x.id} className="flex items-center gap-2"><Dot state={x.status === 'running' ? 'busy' : 'off'} />{x.title || t('kind.' + x.kind)}</div>)}
      </Panel>
    </>
  )
}

function Alerts() {
  const t = useT()
  const d = useApi<{ items: Alert[] }>('/api/v1/alerts')
  useInterval(() => void d.reload(), 15000)
  const firing = (d.data?.items ?? []).filter((a) => a.state === 'firing')
  return (
    <Panel title={t('screen.alerts', { n: firing.length })}>
      {firing.length === 0 ? <div className="flex items-center gap-2 text-ok"><Dot state="ok" />{t('screen.all_clear')}</div>
        : firing.slice(0, 8).map((a) => <div key={a.id} className="flex items-center gap-2 py-0.5"><Dot state={a.severity === 'critical' ? 'bad' : 'warn'} /><span className="truncate">{a.title}</span><span className="ml-auto text-[13px] text-mute">{a.host}</span></div>)}
    </Panel>
  )
}

function Uptime() {
  const t = useT()
  const d = useApi<{ items: Check[] }>('/api/v1/uptime')
  useInterval(() => void d.reload(), 30000)
  const items = d.data?.items ?? []
  return (
    <Panel title={t('screen.uptime')}>
      {items.length === 0 ? <div className="text-mute">{t('screen.no_checks')}</div>
        : items.slice(0, 10).map((c) => <div key={c.id} className="flex items-center gap-2 py-0.5"><Dot state={c.up === undefined ? 'off' : c.up ? 'ok' : 'bad'} /><span className="truncate">{c.name}</span>
          <span className="num ml-auto text-[13px] text-mute">{c.last_ms !== undefined ? c.last_ms + ' ms' : ''} · {c.uptime_30d >= 0 ? c.uptime_30d.toFixed(2) + '%' : '—'}</span></div>)}
    </Panel>
  )
}
