import { Link } from 'react-router-dom'
import { Spark } from '@/components/charts/Spark'
import { RingMeter } from '@/components/charts/Ring'
import { hostPath, useHost } from '@/components/shell/host'
import { cx } from '@/components/ui/cx'
import { bytes, duration, rate } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Host } from '@/lib/types'
import { netRx, netTx, rootDisk, type HostSample } from '@/lib/useHostLive'

interface Summary {
  sites?: { total: number; enabled: number }
  containers?: { total: number; running: number; open_ports: number }
  vms?: { total: number; running: number }
  traffic_today: { rx: number; tx: number; top: { name: string; rx: number; tx: number }[] }
  security: { bans: number; auth_failures: number; waf_blocked: number }
  certs_expiring: number
  backup_last?: { status: string; finished: number }
  uptime: { up: number; down: number }
}

// Dashboard: the server at a glance (方案 §5.1) — round meters for the things that have a
// limit, live figures for the rest, then one tile per area with what needs attention.
export function Dashboard({ host: h, live }: { host?: Host; live: Host[] }) {
  const t = useT()
  const { host } = useHost()
  const s = useApi<Summary>(`/api/v1/hosts/${encodeURIComponent(host)}/summary`, [host])
  useInterval(() => void s.reload(), 30000)
  const cur = h as unknown as HostSample | undefined
  const disk = cur && rootDisk(cur)
  const swapTotal = h?.swap_total ?? 0 // zram swap devices are part of it
  const swapUsed = h?.swap_used ?? 0
  const samples = live as unknown as HostSample[]
  const temp = h && (h.temps ?? []).length > 0 ? Math.max(...h.temps.map((x) => x.c)) : null
  return (
    <div className="space-y-3">
      <section aria-label={t('dash.now')} className="rounded-[var(--radius-box)] border border-line p-4">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-[repeat(4,minmax(0,1fr))_minmax(0,1.6fr)]">
          <RingMeter value={h ? h.cpu / 100 : null} label="CPU" sub={h ? t('dash.cores_load', { n: h.cores, load: h.load[0].toFixed(2) }) : undefined} />
          <RingMeter value={h ? h.mem_used / h.mem_total : null} label={t('overview.memory')} sub={h ? `${bytes(h.mem_used)} / ${bytes(h.mem_total)}` : undefined} />
          <RingMeter value={disk ? disk.used / disk.total : null} label={t('overview.disk')} sub={disk ? `${bytes(disk.used)} / ${bytes(disk.total)}` : undefined} badAt={0.95} />
          <RingMeter value={swapTotal ? swapUsed / swapTotal : null} label={t('dash.swap')} sub={swapTotal ? `${bytes(swapUsed)} / ${bytes(swapTotal)}` : t('dash.none')} />
          <div className="col-span-2 space-y-2 text-[12.5px] sm:col-span-4 lg:col-span-1">
            <Live label={t('dash.down')} value={cur ? rate(netRx(cur)) : '—'} values={samples.map(netRx)} />
            <Live label={t('dash.up')} value={cur ? rate(netTx(cur)) : '—'} values={samples.map(netTx)} />
            <div className="grid grid-cols-[1.5fr_1fr_0.8fr] gap-2 pt-1">
              <Stat label={t('dash.load')} value={h ? h.load.map((x) => x.toFixed(2)).join(' / ') : '—'} />
              <Stat label={t('overview.uptime')} value={h ? duration(h.uptime, t) : '—'} />
              <Stat label={t('overview.temp')} value={temp != null ? `${temp.toFixed(0)} °C` : '—'} tone={temp != null && temp >= 80 ? 'warn' : undefined} />
            </div>
          </div>
        </div>
      </section>
      {s.data && <Tiles s={s.data} host={host} />}
    </div>
  )
}

function Live({ label, value, values }: { label: string; value: string; values: number[] }) {
  return (
    <div className="grid grid-cols-[2.5rem_1fr_5.5rem] items-center gap-2">
      <span className="text-mute">{label}</span>
      <Spark values={values} height={22} label={label} />
      <b className="num text-right font-medium">{value}</b>
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'warn' }) {
  return (
    <div>
      <div className="text-[11.5px] text-mute">{label}</div>
      <div className={cx('num truncate text-[13px] font-semibold', tone === 'warn' && 'text-warn')}>{value}</div>
    </div>
  )
}

// one tile per area: a headline number, a line of detail, and a link to the page
function Tiles({ s, host }: { s: Summary; host: string }) {
  const t = useT()
  const tiles: { key: string; to: string; title: string; big: string; detail: React.ReactNode; tone?: 'warn' | 'bad' }[] = []
  if (s.sites) tiles.push({ key: 'sites', to: 'sites', title: t('nav.sites'), big: `${s.sites.enabled}`, detail: t('dash.sites', { total: s.sites.total }) })
  if (s.containers) tiles.push({ key: 'ct', to: 'containers', title: t('nav.containers'), big: `${s.containers.running}/${s.containers.total}`,
    detail: s.containers.open_ports ? <span className="text-warn">{t('dash.open_ports', { n: s.containers.open_ports })}</span> : t('dash.running'),
    tone: s.containers.running < s.containers.total ? 'warn' : undefined })
  if (s.vms && s.vms.total > 0) tiles.push({ key: 'vm', to: 'vms', title: t('nav.vms'), big: `${s.vms.running}/${s.vms.total}`, detail: t('dash.running') })
  tiles.push({ key: 'traffic', to: 'network?tab=traffic', title: t('dash.traffic_today'), big: `↓${bytes(s.traffic_today.rx)} ↑${bytes(s.traffic_today.tx)}`,
    detail: (s.traffic_today.top ?? []).length ? t('dash.top', { list: s.traffic_today.top.map((x) => `${x.name} ${bytes(x.rx + x.tx)}`).join('、') }) : t('dash.no_traffic') })
  const sec = s.security
  tiles.push({ key: 'sec', to: 'network?tab=bans', title: t('dash.security'), big: `${sec.bans}`,
    detail: t('dash.security_detail', { fails: sec.auth_failures, waf: sec.waf_blocked }), tone: sec.auth_failures > 50 ? 'warn' : undefined })
  const attention: string[] = []
  if (s.certs_expiring) attention.push(t('dash.certs', { n: s.certs_expiring }))
  if (s.uptime.down) attention.push(t('dash.down_checks', { n: s.uptime.down }))
  if (s.backup_last && s.backup_last.status !== 'ok' && s.backup_last.status !== 'done') attention.push(t('dash.backup_failed'))
  tiles.push({ key: 'health', to: 'monitor', title: t('dash.health'), big: attention.length ? `${attention.length}` : '✓',
    detail: attention.length ? attention.join(' · ') : t('dash.all_good', { up: s.uptime.up }), tone: attention.length ? 'bad' : undefined })
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      {tiles.map((x) => (
        <Link key={x.key} to={hostPath(host, x.to)} className="group rounded-[var(--radius-box)] border border-line px-3.5 py-3 hover:border-acc">
          <div className="text-[12px] text-mute">{x.title}</div>
          <div className={cx('num mt-0.5 truncate text-[20px] font-semibold', x.tone === 'warn' && 'text-warn', x.tone === 'bad' && 'text-bad')}>{x.big}</div>
          <div className="mt-0.5 line-clamp-2 text-[11.5px] text-mute">{x.detail}</div>
        </Link>
      ))}
    </div>
  )
}
