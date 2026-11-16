import { BadgeAlert, Boxes, CircleX, Clock, Monitor, Plus, Rocket, ShieldAlert, Sparkles } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { askAI, useAIReady } from '@/components/ai/AiDrawer'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Menu } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { can, useAuth } from '@/lib/auth'
import { bytes, pct, rate } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Berth, Host } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { daysLeft, siteName, type CertRow, type Site } from '../../sites/types'

const card = 'rounded-2xl border border-line bg-card'

function Section({ title, sub, right, children }: { title: ReactNode; sub?: ReactNode; right?: ReactNode; children: ReactNode }) {
  return (
    <section className="mb-7">
      <div className="mb-3 flex items-baseline gap-2.5">
        <h2 className="text-[15.5px] font-semibold">{title}</h2>
        {sub && <span className="text-[12.5px] text-mute">{sub}</span>}
        {right && <div className="ml-auto text-[13px]">{right}</div>}
      </div>
      {children}
    </section>
  )
}

// ---- 问候 ----
export function Greeting() {
  const t = useT()
  const nav = useNavigate()
  const { host } = useHost()
  const { user } = useAuth()
  const ai = useAIReady()
  const h = new Date().getHours()
  const hello = t(h < 11 ? 'home.morning' : h < 18 ? 'home.afternoon' : 'home.evening', { name: user?.display_name || user?.username || '' })
  const items = [
    can(user, 'sites.manage') && { label: t('home.new_site'), onClick: () => nav(hostPath(host, 'sites?new=1')) },
    can(user, 'store.install') && { label: t('home.new_app'), onClick: () => nav(hostPath(host, 'store')) },
    can(user, 'vms.manage') && { label: t('home.new_vm'), onClick: () => nav(hostPath(host, 'vms')) },
    can(user, 'db.manage') && { label: t('home.new_db'), onClick: () => nav(hostPath(host, 'db')) },
  ].filter(Boolean) as { label: string; onClick: () => void }[]
  return (
    <div className="mb-2 flex flex-wrap items-center gap-3">
      <h1 className="text-[23px] font-semibold tracking-tight">{hello}</h1>
      <div className="ml-auto flex gap-2">
        {ai && <Button onClick={() => askAI('', '')}><Sparkles className="size-4" />{t('home.ask_ai')}</Button>}
        {items.length > 0 && <Menu trigger={<Button variant="primary"><Plus className="size-4" />{t('home.new')}</Button>} items={items} />}
      </div>
    </div>
  )
}

// ---- 需要处理的事 ----
interface AlertEv { id: number; severity: string; host: string; state: string; title: string; message: string; rule: string }
interface Thing { key: string; tone: 'warn' | 'bad'; icon: ReactNode; title: string; detail?: string; to: string; action: string }

export function Todo() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const alerts = useApi<{ items: AlertEv[] }>('/api/v1/alerts')
  const sb = useApi<{ pending: number }>(`/api/v1/statusbar?host=${encodeURIComponent(host)}`, [host])
  const certs = useApi<{ items: CertRow[] }>(can(user, 'sites.view') ? '/api/v1/certs' : null)
  useEvents(['alert', 'change.pending', 'change.done'], () => { void alerts.reload(); void sb.reload() })
  const things: Thing[] = []
  for (const a of (alerts.data?.items ?? []).filter((x) => x.state === 'firing').slice(0, 5)) {
    things.push({ key: 'a' + a.id, tone: a.severity === 'critical' ? 'bad' : 'warn', icon: a.severity === 'critical' ? <CircleX className="size-[18px]" /> : <BadgeAlert className="size-[18px]" />,
      title: a.title + (a.host && a.host !== 'local' ? ' · ' + a.host : ''), detail: a.message, to: hostPath(host, 'monitor'), action: t('home.look') })
  }
  if ((sb.data?.pending ?? 0) > 0) {
    things.push({ key: 'pending', tone: 'warn', icon: <Clock className="size-[18px]" />, title: t('home.pending', { n: sb.data!.pending }), to: hostPath(host, 'audit?status=pending_approval'), action: t('home.review') })
  }
  for (const c of (certs.data?.items ?? []).filter((x) => x.info.not_after && daysLeft(x.info.not_after) <= 30 && !x.info.self_signed).slice(0, 3)) {
    things.push({ key: 'c' + c.id, tone: daysLeft(c.info.not_after) <= 7 ? 'bad' : 'warn', icon: <ShieldAlert className="size-[18px]" />,
      title: t('home.cert_expiring', { name: c.info.domains[0] ?? c.name, n: Math.max(0, daysLeft(c.info.not_after)) }), to: hostPath(host, 'sites/certs'), action: t('home.look') })
  }
  if (!alerts.data) return <div className="mb-7 h-6" />
  if (things.length === 0) return <p className="mb-7 text-mute">{t('home.all_good')}</p>
  return (
    <div className="mb-7">
      <p className="mb-3 text-mute">{t('home.n_things', { n: things.length })}</p>
      <div className="space-y-2">
        {things.map((x) => (
          <div key={x.key} className={cx('flex items-center gap-3 rounded-xl px-3.5 py-2.5', x.tone === 'bad' ? 'bg-[color-mix(in_srgb,var(--bad)_11%,var(--card))]' : 'bg-[color-mix(in_srgb,var(--warn)_11%,var(--card))]')}>
            <span className={x.tone === 'bad' ? 'text-bad' : 'text-warn'}>{x.icon}</span>
            <div className="min-w-0 flex-1 sm:flex sm:items-baseline sm:gap-3">
              <span className="font-medium">{x.title}</span>
              {x.detail && <span className="block truncate text-[12.5px] text-mute sm:inline">{x.detail}</span>}
            </div>
            <Link to={x.to} className="shrink-0 rounded-[9px] border border-line bg-card px-3 py-1 text-[13px] hover:border-mute">{x.action}</Link>
          </div>
        ))}
      </div>
    </div>
  )
}

// ---- 网站卡片 ----
const KIND: Record<string, { label: string; color: string; name: (s: Site) => string }> = {
  php: { label: 'P', color: '#6a6fc2', name: (s) => 'PHP ' + (s.target.php ? s.target.php.replace(/^(\d)(\d)$/, '$1.$2') : '') },
  static: { label: 'S', color: '#4b7f9c', name: () => 'static' },
  container: { label: 'C', color: '#1f6f5c', name: (s) => s.target.container ?? '' },
  address: { label: '↗', color: '#7a6a4f', name: (s) => (s.target.upstreams ?? []).join(', ') },
  redirect: { label: '→', color: '#8a8478', name: (s) => '→ ' + (s.target.to ?? '') },
  project: { label: 'N', color: '#111111', name: (s) => (s.target.project ?? '') },
}

function SiteCard({ s, host }: { s: Site; host: string }) {
  const t = useT()
  const k = KIND[s.target.type] ?? KIND.address
  const stats = useApi<{ total: { req: number } }>(`/api/v1/hosts/${host}/sites/${s.id}/stats?range=24h`, [s.id])
  const tags: string[] = []
  if (s.target.type === 'static' || s.target.type === 'php' || s.target.type === 'project') tags.push(t('home.tag_native'))
  if (s.target.type === 'container') tags.push(t('home.tag_container'))
  if (s.options.waf) tags.push('WAF')
  if (s.options.basic_auth?.length) tags.push(t('sites.basic_auth'))
  return (
    <Link to={hostPath(host, 'sites/s/' + s.id)} className={cx(card, 'block p-4 transition-colors hover:border-mute')}>
      <div className="flex items-center gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-[10px] text-[13px] font-bold text-white" style={{ background: k.color }}>{k.label}</span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[15px] font-semibold leading-snug" title={s.domains.join('\n')}>{siteName(s)}</div>
          <div className="num truncate text-[12.5px] text-mute">{k.name(s)}</div>
        </div>
        <span className={cx('inline-flex shrink-0 items-center gap-1.5 text-[12px]', s.enabled ? 'text-ok' : 'text-mute')}>
          <span className={cx('size-[7px] rounded-full', s.enabled ? 'bg-ok' : 'bg-mute')} />{s.enabled ? t('home.running') : t('home.stopped')}
        </span>
      </div>
      <div className="my-3 flex min-h-[22px] flex-wrap gap-1.5">
        {tags.map((x) => <span key={x} className="rounded-full bg-soft px-2.5 text-[12px] leading-[22px] text-mute">{x}</span>)}
      </div>
      <div className="grid grid-cols-2 gap-2 border-t border-line pt-2.5">
        <div><div className="text-[11.5px] text-mute">{t('home.visits_today')}</div><div className="num text-[13.5px] font-semibold">{stats.data ? stats.data.total.req.toLocaleString() : '—'}</div></div>
        <div><div className="text-[11.5px] text-mute">{t('home.tls')}</div><div className="text-[13.5px] font-semibold">{t('sites.tls_' + s.tls.mode)}</div></div>
      </div>
    </Link>
  )
}

export function SiteCards() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const sites = useApi<{ items: Site[] }>(can(user, 'sites.view') ? `/api/v1/hosts/${host}/sites` : null, [host])
  if (!can(user, 'sites.view')) return null
  const list = sites.data?.items ?? []
  const on = list.filter((s) => s.enabled).length
  return (
    <Section title={t('nav.sites')} sub={sites.data && (list.length ? t('home.sites_sub', { n: list.length, on }) : '')}
      right={<Link to={hostPath(host, 'sites')} className="text-mute hover:text-fg">{t('home.all')} →</Link>}>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3.5">
        {list.slice(0, 8).map((s) => <SiteCard key={s.id} s={s} host={host} />)}
        {can(user, 'sites.manage') && (
          <Link to={hostPath(host, 'sites?new=1')} className="flex min-h-[150px] flex-col items-center justify-center gap-1.5 rounded-2xl border-[1.5px] border-dashed border-line text-mute transition-colors hover:border-acc hover:text-acc">
            <Plus className="size-5" />{t('home.new_site_card')}
            <span className="text-[12px] opacity-80">{t('home.new_site_hint')}</span>
          </Link>
        )}
      </div>
    </Section>
  )
}

// ---- 应用与虚拟机 ----
interface Proj { name: string; running: number; total: number }
interface Inst { name: string; type: string; status: string; cpus?: string; memory?: string }

export function AppsVms({ berths }: { berths?: Berth[] }) {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const projs = useApi<{ items: Proj[] }>(can(user, 'containers.view') ? `/api/v1/hosts/${host}/compose` : null, [host])
  const vms = useApi<{ items: Inst[] }>(can(user, 'vms.view') ? `/api/v1/hosts/${host}/vms` : null, [host])
  const natives = useApi<{ items: { name: string; kind?: string; version?: string; active: boolean; sites: number[] }[] }>(can(user, 'sites.view') ? `/api/v1/hosts/${host}/native` : null, [host])
  const mem = (p: string) => (berths ?? []).filter((b) => b.type === 'container' && b.project === p).reduce((a, b) => a + b.mem_bytes, 0)
  const rows: { key: string; icon: ReactNode; name: string; detail: string; ok: boolean; to: string }[] = []
  for (const n of (natives.data?.items ?? []).slice(0, 6)) {
    rows.push({ key: 'n' + n.name, icon: <Rocket className="size-4" />, name: n.name, ok: n.active,
      detail: [t('home.project'), n.version && (n.kind ?? 'node') + ' ' + n.version, n.sites.length === 0 && t('home.no_site')].filter(Boolean).join(' · '), to: hostPath(host, 'projects') })
  }
  for (const p of (projs.data?.items ?? []).filter((x) => !x.name.startsWith('berth-')).slice(0, 6)) {
    const m = mem(p.name)
    rows.push({ key: 'p' + p.name, icon: <Boxes className="size-4" />, name: p.name, ok: p.running > 0,
      detail: t('home.containers_n', { n: p.total }) + (m ? ' · ' + bytes(m) : ''), to: hostPath(host, 'containers/compose/' + encodeURIComponent(p.name)) })
  }
  for (const v of (vms.data?.items ?? []).slice(0, 4)) {
    rows.push({ key: 'v' + v.name, icon: <Monitor className="size-4" />, name: v.name, ok: v.status === 'Running',
      detail: (v.type === 'virtual-machine' ? t('vms.type_vm') : t('vms.type_ct')) + (v.status === 'Running' ? '' : ' · ' + t('home.stopped')), to: hostPath(host, 'vms') })
  }
  if (!projs.data && !vms.data && !natives.data) return null
  return (
    <div className={cx(card, 'p-4 sm:px-5')}>
      <div className="mb-1 flex items-baseline"><h2 className="text-[15.5px] font-semibold">{t('home.apps_vms')}</h2>
        <Link to={hostPath(host, 'containers')} className="ml-auto text-[13px] text-mute hover:text-fg">{t('home.all')} →</Link></div>
      {rows.length === 0 && <p className="py-3 text-[13px] text-mute">{t('home.no_apps')}</p>}
      {rows.map((r) => (
        <Link key={r.key} to={r.to} className="flex items-center gap-2.5 border-t border-line py-2 text-[13.5px] first-of-type:border-t-0">
          <span className="grid size-7 place-items-center rounded-lg bg-soft text-mute">{r.icon}</span>
          <span className="min-w-0 flex-1 truncate">{r.name}</span>
          <span className="text-[12.5px] text-mute">{r.detail}</span>
          <span className={cx('size-[7px] rounded-full', r.ok ? 'bg-ok' : 'bg-mute')} />
        </Link>
      ))}
    </div>
  )
}

// ---- 这台服务器 ----
export function ServerBox({ host: h }: { host?: Host }) {
  const t = useT()
  const { host } = useHost()
  if (!h) return null
  const root = h.disks.find((d) => d.mount === '/') ?? h.disks[0]
  const rx = h.net.reduce((a, n) => a + n.rx_bps, 0)
  const tx = h.net.reduce((a, n) => a + n.tx_bps, 0)
  const cell = (label: string, side: string, value: string, frac?: number) => (
    <div className="rounded-xl bg-canvas p-3">
      <div className="flex justify-between text-[12px] text-mute"><span>{label}</span><span className="num">{side}</span></div>
      <div className="num text-[19px] font-semibold">{value}</div>
      {frac !== undefined && <div className="mt-2 h-[5px] overflow-hidden rounded-full bg-line"><div className="h-full rounded-full bg-acc" style={{ width: Math.min(100, frac * 100) + '%' }} /></div>}
    </div>
  )
  return (
    <div className={cx(card, 'p-4 sm:px-5')}>
      <div className="mb-3 flex items-baseline gap-2.5"><h2 className="text-[15.5px] font-semibold">{t('home.this_server')}</h2>
        <span className="text-[12.5px] text-mute">{t('home.load', { l: h.load[0].toFixed(2) })}</span>
        <Link to={hostPath(host, 'system')} className="ml-auto text-[13px] text-mute hover:text-fg">{t('home.details')} →</Link></div>
      <div className="grid gap-2.5 sm:grid-cols-2">
        {cell('CPU', t('home.cores', { n: h.cores }), pct(h.cpu), h.cpu / 100)}
        {cell(t('home.memory'), `${bytes(h.mem_used)} / ${bytes(h.mem_total)}`, pct((h.mem_used / h.mem_total) * 100), h.mem_used / h.mem_total)}
        {root && cell(t('home.disk'), `${bytes(root.used)} / ${bytes(root.total)}`, pct((root.used / root.total) * 100), root.used / root.total)}
        {cell(t('home.network'), '↑ ' + rate(tx), '↓ ' + rate(rx))}
      </div>
    </div>
  )
}
