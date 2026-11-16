import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Switch, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { errText, get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes, rate } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { useHostLive } from '@/lib/useHostLive'

interface Addr { ip: string; prefix: number; family: '4' | '6'; scope: string; dynamic: boolean; public: boolean }
interface Conn { name: string; uuid: string; method4: string; addrs4: string[]; gateway4?: string; dns4: string[]; method6: string; autoconnect: boolean }
export interface NIC {
  name: string; kind: string; physical: boolean; owner: string; state: string; carrier: boolean; mac?: string; mtu: number; speed_mbps?: number; driver?: string
  master?: string; addrs: Addr[]; default: boolean; gateway?: string; rx_bytes: number; tx_bytes: number; rx_errors: number; tx_errors: number; rx_dropped: number; tx_dropped: number
  conn?: Conn; ssid?: string
}
interface Route { dst: string; gateway?: string; dev: string; proto?: string; metric?: number; src?: string; family: string }
interface Data { items: NIC[]; routes: Route[]; dns: { servers: string[]; search: string[]; via: string; per_link: Record<string, string[]> }; pending: boolean; rollback_at: number; nm: boolean }

const MAIN_KINDS = ['ethernet', 'wifi', 'vpn', 'bridge']

export function Nics() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<Data>(`/api/v1/hosts/${host}/system/nics`, [host])
  useEvents(['change.done'], () => void d.reload())
  const { last } = useHostLive(host)
  const [all, setAll] = useState(false)
  const [edit, setEdit] = useState<NIC | null>(null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const live = new Map(last?.net.map((n) => [n.name, n]))
  const shown = d.data.items.filter((n) => all || MAIN_KINDS.includes(n.kind) || n.default)
  const hidden = d.data.items.length - shown.length
  return (
    <div className="space-y-4">
      {d.data.pending && <Pending at={d.data.rollback_at} onDone={() => void d.reload()} />}
      <div className="grid gap-3 xl:grid-cols-2">
        {shown.map((n) => <NicCard key={n.name} n={n} live={live.get(n.name)} manage={manage && d.data!.nm} onEdit={() => setEdit(n)}
          onToggle={(up) => run('system.nic.set', { name: n.name, up }, { host })} />)}
      </div>
      {hidden > 0 && <button className="text-[12.5px] text-acc hover:underline" onClick={() => setAll(true)}>{t('system.show_virtual', { n: hidden })}</button>}
      {all && <button className="text-[12.5px] text-acc hover:underline" onClick={() => setAll(false)}>{t('system.hide_virtual')}</button>}
      <div className="grid gap-4 lg:grid-cols-2">
        <Box title={t('system.dns')} right={<span>{d.data.dns.via}</span>}>
          <div className="space-y-1 px-4 py-3 text-[13px]">
            {d.data.dns.servers.length > 0 && <div><span className="text-mute">{t('system.dns_global')}</span> <span className="num">{d.data.dns.servers.join(' · ')}</span></div>}
            {Object.entries(d.data.dns.per_link).map(([k, v]) => <div key={k}><span className="text-mute">{k}</span> <span className="num">{v.join(' · ')}</span></div>)}
            {d.data.dns.search.length > 0 && <div className="text-[12px] text-mute">{t('system.dns_search')} <span className="num">{d.data.dns.search.join(' ')}</span></div>}
            <div className="pt-1 text-[12px] text-mute">{t('system.dns_hint')}</div>
            <Resolver />
          </div>
        </Box>
        <Box title={t('system.routes')} count={d.data.routes.length}>
          <div className="max-h-72 overflow-auto">
            {d.data.routes.filter((r) => all || !/^(br-|docker|veth)/.test(r.dev)).map((r, i) => (
              <div key={i} className="num flex gap-3 border-b border-line px-4 py-1.5 text-[12px] last:border-b-0">
                <span className={cx('w-40 truncate', r.dst === 'default' && 'font-semibold')}>{r.dst === 'default' ? t('system.route_default') + ' (IPv' + r.family + ')' : r.dst}</span>
                <span className="w-32 truncate text-mute">{r.gateway ? 'via ' + r.gateway : ''}</span>
                <span className="flex-1 truncate">{r.dev}</span>
                <span className="text-mute">{r.proto}</span>
              </div>
            ))}
          </div>
        </Box>
      </div>
      {edit && <NicDialog n={edit} onClose={() => setEdit(null)} />}
    </div>
  )
}

function NicCard({ n, live, manage, onEdit, onToggle }: { n: NIC; live?: { rx_bps: number; tx_bps: number }; manage: boolean; onEdit: () => void; onToggle: (up: boolean) => void }) {
  const t = useT()
  const { host } = useHost()
  const [pub, setPub] = useState<string | null>(null)
  const up = n.state === 'UP' || (n.state === 'UNKNOWN' && n.addrs.length > 0)
  const errors = n.rx_errors + n.tx_errors
  const drops = n.rx_dropped + n.tx_dropped
  const probe = async () => {
    setPub('…')
    try { setPub((await get<{ ip: string }>(`/api/v1/hosts/${host}/system/nics/${encodeURIComponent(n.name)}/public-ip`)).ip) } catch (e) { setPub(t('system.no_internet') + ' · ' + errText(e)) }
  }
  return (
    <section className="rounded-[var(--radius-box)] border border-line">
      <header className="flex flex-wrap items-center gap-2 border-b border-line bg-soft px-3.5 py-2">
        <Dot state={up ? (n.carrier || n.kind === 'vpn' ? 'ok' : 'warn') : 'off'} />
        <b className="num text-[13.5px]">{n.name}</b>
        <Pill tone={n.kind === 'ethernet' || n.kind === 'wifi' ? 'acc' : undefined}>{t('system.kind_' + n.kind)}</Pill>
        {n.default && <Pill tone="ok">{t('system.default_route')}</Pill>}
        {n.ssid && <span className="text-[12px] text-mute">Wi-Fi「{n.ssid}」</span>}
        <span className="ml-auto flex gap-1">
          {manage && n.conn && n.owner === 'system' && <Button size="sm" variant="ghost" onClick={onEdit}>{t('system.configure')}</Button>}
          {manage && n.conn && n.owner === 'system' && !n.default && <Button size="sm" variant="ghost" onClick={() => onToggle(!up)}>{up ? t('system.disconnect') : t('system.connect')}</Button>}
          {manage && !n.conn && n.kind === 'ethernet' && !up && <Button size="sm" variant="ghost" onClick={() => onToggle(true)}>{t('system.connect')}</Button>}
        </span>
      </header>
      <div className="space-y-1.5 px-3.5 py-2.5 text-[12.5px]">
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-mute">
          {n.speed_mbps && <span>{n.speed_mbps >= 1000 ? n.speed_mbps / 1000 + ' Gbps' : n.speed_mbps + ' Mbps'}</span>}
          {n.mac && <span className="num">{n.mac}</span>}
          <span>MTU {n.mtu}</span>
          {n.driver && <span>{n.driver}</span>}
          {n.master && <span>{t('system.in_bridge', { name: n.master })}</span>}
          {n.owner !== 'system' && <span>{t('system.owned_by', { who: n.owner })}</span>}
        </div>
        {n.addrs.filter((a) => a.scope !== 'link').map((a) => (
          <div key={a.ip} className="num flex flex-wrap items-center gap-2">
            <span>{a.ip}/{a.prefix}</span>
            {a.public && <Pill tone="warn">{t('system.public_addr')}</Pill>}
            <span className="text-[11.5px] text-mute">{a.dynamic ? 'DHCP' : n.conn?.method4 === 'manual' && a.family === '4' ? t('system.static') : ''}</span>
          </div>
        ))}
        {n.addrs.filter((a) => a.scope !== 'link').length === 0 && <div className="text-mute">{t('system.no_addr')}</div>}
        {n.gateway && <div className="text-mute">{t('system.gateway')} <span className="num">{n.gateway}</span></div>}
        <div className="num flex flex-wrap gap-x-4 text-mute">
          <span>↓ {live ? rate(live.rx_bps) : '—'} · {t('system.total')} {bytes(n.rx_bytes)}</span>
          <span>↑ {live ? rate(live.tx_bps) : '—'} · {t('system.total')} {bytes(n.tx_bytes)}</span>
          {errors > 0 && <span className="text-warn">{t('system.errors', { n: errors })}</span>}
          {drops > 0 && <span title={t('system.drops_hint')}>{t('system.drops', { n: drops })}</span>}
        </div>
        {up && (n.kind === 'ethernet' || n.kind === 'wifi' || n.kind === 'vpn' || n.default) && (
          <div className="flex items-center gap-2">
            <button className="text-acc hover:underline" onClick={() => void probe()}>{t('system.egress_ip')}</button>
            {pub && <span className="num">{pub}</span>}
          </div>
        )}
      </div>
    </section>
  )
}

function NicDialog({ n, onClose }: { n: NIC; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const c = n.conn!
  const [method, setMethod] = useState<'auto' | 'manual'>(c.method4 === 'manual' ? 'manual' : 'auto')
  const v4 = n.addrs.find((a) => a.family === '4' && a.scope === 'global')
  const [addrs, setAddrs] = useState((c.addrs4.length ? c.addrs4 : v4 ? [`${v4.ip}/${v4.prefix}`] : []).join('\n'))
  const [gw, setGw] = useState(c.gateway4 || n.gateway || '')
  const [dns, setDns] = useState(c.dns4.join(' '))
  const [ownDns, setOwnDns] = useState(false)
  const [mtu, setMtu] = useState(String(n.mtu))
  const [secs, setSecs] = useState('60')
  const lines = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)
  const params = {
    name: n.name, method4: method, ...(method === 'manual' ? { addrs4: lines(addrs), gateway4: gw } : {}),
    dns4: lines(dns), own_dns: ownDns, ...(Number(mtu) !== n.mtu ? { mtu: Number(mtu) } : {}), seconds: Number(secs),
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('system.configure_title', { name: n.name })} description={t('system.configure_hint')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={() => run('system.nic.set', params, { host, onDone: onClose })}>{t('action.system.nic.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {n.default && <Notice tone="warn">{t('system.default_warning')}</Notice>}
        <Field label={t('system.ipv4')}>
          <Segmented value={method} onChange={setMethod} options={[{ value: 'auto', label: t('system.dhcp') }, { value: 'manual', label: t('system.static') }]} />
        </Field>
        {method === 'manual' && <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('system.addresses')} hint={t('system.addresses_hint')}><Textarea className="num h-20" value={addrs} onChange={(e) => setAddrs(e.target.value)} /></Field>
          <Field label={t('system.gateway')}><Input className="num" value={gw} onChange={(e) => setGw(e.target.value)} placeholder="192.168.1.1" /></Field>
        </div>}
        <Field label={t('system.dns_servers')} hint={t('system.dns_servers_hint')}>
          <Input className="num" value={dns} onChange={(e) => setDns(e.target.value)} placeholder="223.5.5.5 1.1.1.1" />
        </Field>
        {method === 'auto' && <Switch checked={ownDns} onChange={setOwnDns} label={t('system.own_dns')} />}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="MTU"><Input className="num" type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} /></Field>
          <Field label={t('system.rollback_after')} hint={t('system.rollback_hint')}>
            <Segmented value={secs} onChange={setSecs} options={['60', '120', '300'].map((s) => ({ value: s, label: t('system.seconds', { n: s }) }))} />
          </Field>
        </div>
      </div>
    </Dialog>
  )
}

// After a network change: if this page still talks to the panel, the network works.
function Pending({ at, onDone }: { at: number; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [now, setNow] = useState(Date.now() / 1000)
  useEffect(() => { const i = setInterval(() => setNow(Date.now() / 1000), 1000); return () => clearInterval(i) }, [])
  const left = Math.max(0, Math.round(at - now))
  useEffect(() => { if (left === 0) onDone() }, [left, onDone])
  return (
    <Notice tone="warn">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex-1">{t('system.pending', { s: left })}</span>
        <Button size="sm" variant="primary" onClick={() => run('system.net.confirm', {}, { host, onDone })}>{t('system.keep')}</Button>
        <Button size="sm" onClick={() => run('system.net.rollback', {}, { host, onDone })}>{t('system.restore')}</Button>
      </div>
    </Notice>
  )
}

// The system-wide DNS servers (systemd-resolved), set once instead of per card.
function Resolver() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const d = useApi<{ resolver: { resolved: boolean; servers: string[] } }>(`/api/v1/hosts/${host}/system/tuning`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [v, setV] = useState<string | null>(null)
  const r = d.data?.resolver
  if (!r?.resolved || !can(user, 'system.manage')) return null
  const cur = r.servers.join(' ')
  const val = v ?? cur
  const list = val.split(/[\s,]+/).filter(Boolean)
  return (
    <div className="flex flex-wrap items-center gap-2 border-t border-line pt-2">
      <span className="text-[12.5px]">{t('system.resolver')}</span>
      <Input className="num w-56" value={val} placeholder={t('system.resolver_ph')} onChange={(e) => setV(e.target.value)} />
      {val !== cur && <Button size="sm" variant="primary" onClick={() => run('dns.resolver', { servers: list }, { host, onDone: () => setV(null) })}>{t('common.apply')}</Button>}
      {r.servers.length > 0 && val === cur && <button className="text-[12px] text-mute hover:underline" onClick={() => run('dns.resolver', { servers: [] }, { host })}>{t('system.resolver_clear')}</button>}
    </div>
  )
}
