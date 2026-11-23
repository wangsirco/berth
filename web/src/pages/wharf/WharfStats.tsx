import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ArrowLeftRight, Ban, ChevronRight, Globe, ShieldX, Smartphone } from 'lucide-react'
import { OpenPeer } from '@/components/access/OpenPeer'
import { useChange } from '@/components/change/ChangeDialog'
import { BarList, FlowChart, type FlowPoint } from '@/components/charts/Flow'
import { Box, Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Segmented } from '@/components/ui/Field'
import { TabBar } from '@/components/ui/Tabs'
import { post } from '@/lib/api'
import { bytes, clock, date, datetime } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import type { WharfStatus } from '../network/NetworkPage'

// Wharf's statistics in Berth: read from Wharf, drawn here. Every number and row opens the
// matching tab here; every panel can also be opened in Wharf on the right name.

interface Dev { mac: string; name: string; ip: string; type: string; down: number; up: number; last_seen?: number }
interface Summary {
  devices: { online: number; total: number }; today: { down: number; up: number }
  visitors_24h: { count: number; countries: number }; blocked_24h: { attacks: number; dns: number }
  series_24h: FlowPoint[] | null; top_devices: Dev[] | null; top_countries: { cc: string; count: number }[] | null
  pages: Record<string, string>
}
interface Agg { name: string; category?: string; bytes: number; down: number; up: number }
interface Traffic { up: number; down: number; step: number; series: FlowPoint[] | null; devices: Dev[] | null; services: Agg[] | null }
interface Geo { countries: { code: string; name: string; bytes: number; conns: number }[] | null; origin?: { name?: string } }
interface Visitor { id: number; ts: number; ip: string; country: string; city: string; username: string; path: string; blocked: boolean; reason: string }
interface WDevice { mac: string; ip: string; name: string; type: string; online: boolean; presence?: 'home' | 'away' }
interface Presence { state?: 'home' | 'away'; online?: boolean; away_minutes: number }
interface DevTraffic { series: FlowPoint[] | null; up: number; down: number; peak_hour: number }
interface Dns { configured: boolean; status?: { id?: string; name?: string; queries: number }[]; blocked?: { domain?: string; name?: string; queries: number }[]; devices?: { name?: string; queries: number }[] }

type Tab = 'overview' | 'traffic' | 'world' | 'devices' | 'visitors' | 'dns' | 'settings'

export function WharfStats({ st, settings }: { st: WharfStatus; settings: React.ReactNode }) {
  const t = useT()
  const [qs, setQs] = useSearchParams()
  const tab = (qs.get('tab') as Tab) || 'overview'
  const go = (to: Tab, extra: Record<string, string> = {}) => setQs({ tab: to, ...extra })
  const l = st.link!
  const lan = l.peer_lan || (l.mode !== 'public' ? l.url : '')
  const pub = l.peer_public || (l.mode === 'public' ? l.url : '')
  // open a page of Wharf on the right name, signed in when the link allows it
  const inWharf = (path: string) => <OpenPeer id="wharf" lan={lan} pub={pub} variant="ghost" label={t('wstats.in_wharf')}
    open={async (via) => l.scopes.includes('sso') ? (await post<{ url: string }>('/api/v1/wharf/sso', { return_to: path, via })).url : undefined} />
  const tabs: { id: Tab; label: string }[] = [
    { id: 'overview', label: t('wstats.overview') }, { id: 'traffic', label: t('wstats.traffic') }, { id: 'world', label: t('wstats.world') },
    { id: 'devices', label: t('wstats.devices') }, { id: 'visitors', label: t('wstats.visitors') }, { id: 'dns', label: 'DNS' }, { id: 'settings', label: t('wstats.settings') },
  ]
  return (
    <div className="space-y-4" data-wharf-stats>
      <TabBar items={tabs.map((x) => ({ value: x.id, label: x.label }))} value={tab} onChange={(k) => go(k)} />
      {tab === 'overview' && <Overview go={go} inWharf={inWharf} />}
      {tab === 'traffic' && <TrafficTab go={go} inWharf={inWharf} />}
      {tab === 'world' && <WorldTab inWharf={inWharf} />}
      {tab === 'devices' && <DevicesTab mac={qs.get('mac') ?? ''} go={go} inWharf={inWharf} />}
      {tab === 'visitors' && <VisitorsTab inWharf={inWharf} />}
      {tab === 'dns' && <DnsTab inWharf={inWharf} />}
      {tab === 'settings' && settings}
    </div>
  )
}

type Go = (to: Tab, extra?: Record<string, string>) => void
type InWharf = (path: string) => React.ReactNode

// the statistics need Wharf 0.25+ and the read-only scope: say what to do, plainly
function Unavailable({ err }: { err: { message: string } | null | undefined }) {
  if (!err) return null
  return <Notice tone="acc">{err.message}</Notice>
}

function countryName(cc: string, loc: string) {
  try { return new Intl.DisplayNames([loc], { type: 'region' }).of(cc.toUpperCase()) ?? cc } catch { return cc }
}

function Tile({ label, value, sub, tone, onClick, id }: { label: string; value: string; sub: string; tone?: 'bad'; onClick: () => void; id: string }) {
  return (
    <button type="button" onClick={onClick} data-tile={id} className="rounded-[var(--radius-box)] border border-line bg-card px-3.5 py-3 text-left hover:border-acc">
      <div className="flex items-center text-[12px] text-mute">{label}<ChevronRight className="ml-auto size-3.5" /></div>
      <div className={cx('num mt-0.5 text-[22px] font-semibold', tone === 'bad' && 'text-bad')}>{value}</div>
      <div className="num text-[12px] text-mute">{sub}</div>
    </button>
  )
}

function Overview({ go, inWharf }: { go: Go; inWharf: InWharf }) {
  const t = useT()
  const [loc] = useLocale()
  const s = useApi<Summary>('/api/v1/wharf/stats/summary')
  const v = useApi<{ published?: { site_id: number; mode: string; wan_port?: string }[]; sni?: { domains: string[] } }>('/api/v1/hosts/local/wharf')
  const bans = useApi<{ active: { layers: string[] }[] }>('/api/v1/hosts/local/bans')
  useInterval(() => void s.reload(), 30000)
  const d = s.data
  const synced = (bans.data?.active ?? []).filter((b) => b.layers?.includes('wharf')).length
  return (
    <>
      <Unavailable err={s.error} />
      {d && <>
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <Tile id="devices" label={t('wstats.online')} value={String(d.devices.online)} sub={t('wstats.of_total', { n: d.devices.total })} onClick={() => go('devices')} />
          <Tile id="today" label={t('wstats.today')} value={bytes(d.today.down + d.today.up)} sub={`↓ ${bytes(d.today.down)} · ↑ ${bytes(d.today.up)}`} onClick={() => go('traffic')} />
          <Tile id="visitors" label={t('wstats.visitors_24h')} value={d.visitors_24h.count.toLocaleString()} sub={t('wstats.countries', { n: d.visitors_24h.countries })} onClick={() => go('visitors')} />
          <Tile id="blocked" label={t('wstats.blocked_24h')} value={String(d.blocked_24h.attacks + d.blocked_24h.dns)} sub={t('wstats.blocked_split', { a: d.blocked_24h.attacks, d: d.blocked_24h.dns })} tone="bad" onClick={() => go('visitors')} />
        </div>
        <Box title={t('wstats.wan_24h')} right={inWharf(d.pages?.traffic ?? '/traffic')}>
          <div className="px-4 py-3"><FlowChart points={d.series_24h ?? []} label={{ down: t('wstats.down'), up: t('wstats.up') }} time={clock} /></div>
        </Box>
        <div className="grid gap-4 md:grid-cols-2">
          <Box title={t('wstats.top_devices')} right={inWharf(d.pages?.devices ?? '/devices')}>
            <div className="px-4 py-1"><BarList rows={(d.top_devices ?? []).map((x) => ({ key: x.mac, label: x.name || x.ip, value: x.down + x.up, text: bytes(x.down + x.up), onClick: () => go('devices', { mac: x.mac }) }))} /></div>
          </Box>
          <Box title={t('wstats.top_countries')} right={inWharf(d.pages?.visitors ?? '/visitors')}>
            <div className="px-4 py-1"><BarList rows={(d.top_countries ?? []).map((x) => ({ key: x.cc, label: countryName(x.cc, loc), value: x.count, text: x.count.toLocaleString(), onClick: () => go('visitors', { cc: x.cc }) }))} /></div>
          </Box>
        </div>
      </>}
      <Box title={t('wstats.this_server')}>
        <div className="px-4 py-1 text-[13px]">
          {(v.data?.sni?.domains ?? []).map((dm) => <Row key={dm} icon={<Globe className="size-4" />} label={dm} sub={t('wstats.sni')} onClick={() => go('settings')} />)}
          {(v.data?.published ?? []).filter((p) => p.mode === 'forward').map((p) => <Row key={p.site_id} icon={<ArrowLeftRight className="size-4" />} label={t('wstats.site_n', { id: p.site_id })} sub={t('wstats.wan_port', { port: p.wan_port ?? '' })} onClick={() => go('settings')} />)}
          <Row icon={<ShieldX className="size-4" />} label={t('wstats.synced_bans')} sub={String(synced)} onClick={() => go('settings')} />
        </div>
      </Box>
    </>
  )
}

function Row({ icon, label, sub, onClick }: { icon: React.ReactNode; label: string; sub: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="flex w-full items-center gap-2.5 border-t border-line py-2 text-left first:border-t-0 hover:bg-soft">
      <span className="text-mute">{icon}</span><span className="flex-1 truncate">{label}</span><span className="text-[12px] text-mute">{sub}</span><ChevronRight className="size-3.5 text-mute" />
    </button>
  )
}

const RANGES = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 } as const

function TrafficTab({ go, inWharf }: { go: Go; inWharf: InWharf }) {
  const t = useT()
  const [r, setR] = useState<keyof typeof RANGES>('24h')
  const to = useMemo(() => Math.floor(Date.now() / 1000), [r])
  const d = useApi<Traffic>(`/api/v1/wharf/stats/traffic?from=${to - RANGES[r]}&to=${to}`, [r])
  return (
    <>
      <Unavailable err={d.error} />
      <Box title={t('wstats.traffic')} right={<div className="flex items-center gap-2"><Segmented value={r} onChange={setR} options={(Object.keys(RANGES) as (keyof typeof RANGES)[]).map((k) => ({ value: k, label: t('wstats.r_' + k) }))} />{inWharf('/traffic?range=' + (r === '24h' ? 'today' : r))}</div>}>
        <div className="px-4 py-3">
          {d.data && <div className="mb-2 flex gap-5 text-[13px]"><span>{t('wstats.down')} <b className="num">{bytes(d.data.down)}</b></span><span>{t('wstats.up')} <b className="num">{bytes(d.data.up)}</b></span></div>}
          <FlowChart points={d.data?.series ?? []} height={180} label={{ down: t('wstats.down'), up: t('wstats.up') }} time={r === '24h' ? clock : date} />
        </div>
      </Box>
      <div className="grid gap-4 md:grid-cols-2">
        <Box title={t('wstats.by_device')}>
          <div className="px-4 py-1"><BarList rows={(d.data?.devices ?? []).slice(0, 12).map((x) => ({ key: x.mac, label: x.name || x.ip, value: x.down + x.up, text: bytes(x.down + x.up), onClick: () => go('devices', { mac: x.mac }) }))} /></div>
        </Box>
        <Box title={t('wstats.by_service')}>
          <div className="px-4 py-1"><BarList rows={(d.data?.services ?? []).slice(0, 12).map((x) => ({ key: x.name, label: x.name, value: x.bytes, text: bytes(x.bytes) }))} /></div>
        </Box>
      </div>
    </>
  )
}

function WorldTab({ inWharf }: { inWharf: InWharf }) {
  const t = useT()
  const [loc] = useLocale()
  const [r, setR] = useState<'1h' | '24h' | '7d'>('24h')
  const d = useApi<Geo>(`/api/v1/wharf/stats/geo?range=${r}`, [r])
  const rows = [...(d.data?.countries ?? [])].sort((a, b) => b.bytes - a.bytes)
  return (
    <>
      <Unavailable err={d.error} />
      <Box title={t('wstats.world_t')} right={<div className="flex items-center gap-2"><Segmented value={r} onChange={setR} options={(['1h', '24h', '7d'] as const).map((k) => ({ value: k, label: t('wstats.r_' + k) }))} />{inWharf('/world?range=' + r)}</div>}>
        <div className="px-4 py-2">
          <p className="pb-1 text-[12px] text-mute">{t('wstats.world_d')}</p>
          <BarList rows={rows.map((x) => ({ key: x.code, label: countryName(x.code, loc) || x.name, value: x.bytes, text: bytes(x.bytes) }))} />
        </div>
      </Box>
    </>
  )
}

function DevicesTab({ mac, go, inWharf }: { mac: string; go: Go; inWharf: InWharf }) {
  const t = useT()
  const d = useApi<WDevice[] | { items: WDevice[] }>('/api/v1/wharf/stats/devices')
  const list = Array.isArray(d.data) ? d.data : d.data?.items ?? []
  const sel = list.find((x) => x.mac === mac)
  return (
    <>
      <Unavailable err={d.error} />
      {mac && <DeviceDetail mac={mac} name={sel?.name || mac} inWharf={inWharf} onClose={() => go('devices')} />}
      <Box title={t('wstats.devices_t', { on: list.filter((x) => x.online).length, all: list.length })} right={inWharf('/devices')}>
        <div className="px-4 py-1">
          {list.map((x) => (
            <button key={x.mac} type="button" onClick={() => go('devices', { mac: x.mac })} data-device={x.mac}
              className={cx('flex w-full items-center gap-2.5 border-t border-line py-2 text-left text-[13px] first:border-t-0 hover:bg-soft', x.mac === mac && 'bg-acc/10')}>
              <Dot state={x.online ? 'ok' : 'off'} />
              <span className="w-40 truncate font-medium">{x.name || x.mac}</span>
              <span className="num w-32 text-[12px] text-mute">{x.ip}</span>
              <span className="flex-1 text-[12px] text-mute">{x.type}</span>
              {x.presence && <Pill tone={x.presence === 'home' ? 'acc' : undefined}>{t('wstats.presence_' + x.presence)}</Pill>}
              <ChevronRight className="size-3.5 text-mute" />
            </button>
          ))}
        </div>
      </Box>
    </>
  )
}

function DeviceDetail({ mac, name, inWharf, onClose }: { mac: string; name: string; inWharf: InWharf; onClose: () => void }) {
  const t = useT()
  const to = useMemo(() => Math.floor(Date.now() / 1000), [mac])
  const tr = useApi<DevTraffic>(`/api/v1/wharf/stats/devices/${mac}/traffic?from=${to - 86400}&to=${to}`, [mac])
  const pr = useApi<Presence>(`/api/v1/wharf/stats/devices/${mac}/presence`, [mac])
  return (
    <Box title={<span className="inline-flex items-center gap-2"><Smartphone className="size-4" />{name}{pr.data?.state && <Pill tone={pr.data.state === 'home' ? 'acc' : undefined}>{t('wstats.presence_' + pr.data.state)}</Pill>}</span>}
      right={<div className="flex gap-2">{inWharf('/devices/' + encodeURIComponent(mac))}<Button size="sm" variant="ghost" onClick={onClose}>{t('common.close')}</Button></div>}>
      <div className="px-4 py-3" data-device-detail>
        <Unavailable err={tr.error} />
        {tr.data && <div className="mb-2 flex gap-5 text-[13px]"><span>{t('wstats.down')} <b className="num">{bytes(tr.data.down)}</b></span><span>{t('wstats.up')} <b className="num">{bytes(tr.data.up)}</b></span>{tr.data.peak_hour >= 0 && <span className="text-mute">{t('wstats.peak', { h: tr.data.peak_hour })}</span>}</div>}
        <FlowChart points={tr.data?.series ?? []} label={{ down: t('wstats.down'), up: t('wstats.up') }} time={clock} />
      </div>
    </Box>
  )
}

function VisitorsTab({ inWharf }: { inWharf: InWharf }) {
  const t = useT()
  const [loc] = useLocale()
  const run = useChange()
  const [qs] = useSearchParams()
  const cc = qs.get('cc') ?? ''
  const d = useApi<{ items: Visitor[] }>('/api/v1/wharf/stats/visitors?limit=200&range=24h' + (cc ? '&cc=' + encodeURIComponent(cc) : ''), [cc])
  const rows = (d.data?.items ?? []).filter((x) => !cc || x.country.toUpperCase() === cc.toUpperCase())
  return (
    <>
      <Unavailable err={d.error} />
      <Box title={t('wstats.visitors_t')} right={inWharf('/visitors?range=24h' + (cc ? '&cc=' + encodeURIComponent(cc) : ''))}>
        <div className="px-4 py-1">
          <p className="py-1 text-[12px] text-mute">{t('wstats.visitors_d')}{cc && <> · {t('wstats.only', { c: countryName(cc, loc) })}</>}</p>
          {rows.length === 0 && <div className="py-3 text-[12.5px] text-mute">—</div>}
          {rows.map((x) => (
            <div key={x.id} className="flex items-center gap-2.5 border-t border-line py-2 text-[13px]" data-visitor={x.ip}>
              <span className="num w-28 text-[12px] text-mute">{datetime(x.ts)}</span>
              <span className="num w-32">{x.ip}</span>
              <span className="w-28 truncate text-[12px]">{[countryName(x.country, loc), x.city].filter(Boolean).join(' · ')}</span>
              <span className="num flex-1 truncate text-[12px] text-mute">{x.username || x.path}</span>
              {x.blocked ? <Pill tone="bad">{t('wstats.blocked')}</Pill> : null}
              <Button size="sm" variant="ghost" title={t('wstats.ban_here_d')} onClick={() => run('ban.add', { addr: x.ip, reason: 'Wharf · ' + (x.reason || x.path) })}><Ban className="size-3.5" />{t('wstats.ban_here')}</Button>
            </div>
          ))}
        </div>
      </Box>
    </>
  )
}

function DnsTab({ inWharf }: { inWharf: InWharf }) {
  const t = useT()
  const d = useApi<Dns>('/api/v1/wharf/stats/dns?range=24h')
  const total = (d.data?.status ?? []).reduce((s, x) => s + x.queries, 0)
  const blocked = (d.data?.status ?? []).filter((x) => (x.id ?? x.name) === 'blocked').reduce((s, x) => s + x.queries, 0)
  return (
    <>
      <Unavailable err={d.error} />
      {d.data && <>
        <div className="grid grid-cols-2 gap-3">
          <div className="rounded-[var(--radius-box)] border border-line bg-card px-3.5 py-3"><div className="text-[12px] text-mute">{t('wstats.dns_queries')}</div><div className="num text-[22px] font-semibold">{total.toLocaleString()}</div></div>
          <div className="rounded-[var(--radius-box)] border border-line bg-card px-3.5 py-3"><div className="text-[12px] text-mute">{t('wstats.dns_blocked')}</div><div className="num text-[22px] font-semibold text-bad">{blocked.toLocaleString()}</div></div>
        </div>
        <div className="grid gap-4 md:grid-cols-2">
          <Box title={t('wstats.dns_top_blocked')} right={inWharf('/dns?range=1d')}><div className="px-4 py-1"><BarList rows={(d.data.blocked ?? []).slice(0, 10).map((x) => ({ key: x.domain ?? x.name ?? '', label: x.domain ?? x.name, value: x.queries, text: x.queries.toLocaleString() }))} /></div></Box>
          <Box title={t('wstats.dns_devices')}><div className="px-4 py-1"><BarList rows={(d.data.devices ?? []).slice(0, 10).map((x) => ({ key: x.name ?? '', label: x.name, value: x.queries, text: x.queries.toLocaleString() }))} /></div></Box>
        </div>
      </>}
    </>
  )
}
