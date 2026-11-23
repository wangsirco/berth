import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ArrowRight, BellRing, Check, Copy, Globe, House, Link2, Radar, Server, ShieldAlert, Unlink, Wifi } from 'lucide-react'
import { Box, Dot, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Textarea } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { del, errText, post } from '@/lib/api'
import { datetime } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { ReauthProvider, useReauth } from '../account/reauth'
import { WharfMark } from '@/components/brand/WharfMark'
import { WharfAsk } from './WharfAsk'

// 面板互联: servers that both run Berth. One panel (main) manages what the other hands over;
// the other keeps working by itself and can take them back. This page explains the two ways
// to bind a server, makes and takes link codes (a link opened on the other panel lands here
// with the code filled in), and shows each link: LAN or public, latency, the peer's address
// and rough location.

export interface Live {
  online: boolean; addr?: string; ip?: string; route?: 'lan' | 'tailscale' | 'public'; via?: 'direct' | 'cdn' | 'proxy'; cdn?: string; issuer?: string
  rtt_ms?: number; jitter_ms?: number; country?: string; error?: string; checked: number; hosts?: { name: string; online: boolean; version?: string }[]; peer_version?: string
}
interface LinkItem {
  id: number; role: 'main' | 'managed'; status: 'code' | 'confirm' | 'wait' | 'active'; name: string; slug: string; addrs: string[]; fp: string; sas?: string
  hosts: string[]; by: string; created_at: number; expires_at?: number; confirmed_at?: number; last_addr?: string; live: Live
}
interface LinkRequest { id: string; from: string; ip: string; at: number; code: string }
interface Data { items: LinkItem[]; name: string; fp: string; addrs: string[]; discoverable_until: number; requests: LinkRequest[] }

export default function LinksPage() {
  return <ReauthProvider><Links /></ReauthProvider>
}

function Links() {
  const t = useT()
  const d = useApi<Data>('/api/v1/links', [])
  useEvents(['link.pending', 'link.request'], () => void d.reload())
  useInterval(() => void d.reload(), 15000)
  const [making, setMaking] = useState(false)
  const [scanning, setScanning] = useState(false)
  const [taking, setTaking] = useState<string | null>(null)
  // a link opened on this panel: /local/links#accept=<code>
  useEffect(() => {
    const m = /accept=([^&]+)/.exec(location.hash)
    if (m) { setTaking(decodeURIComponent(m[1])); history.replaceState(null, '', location.pathname) }
  }, [])
  const items = d.data?.items ?? []
  const main = items.filter((l) => l.role === 'main' && l.status !== 'code')
  const managed = items.filter((l) => l.role === 'managed')
  return (
    <div className="space-y-4">
      {(d.data?.requests ?? []).map((rq) => (
        <div key={rq.id} className="flex flex-wrap items-center gap-3 rounded-xl border border-acc bg-tint px-4 py-3" data-link-request={rq.id}>
          <BellRing className="size-5 text-acc" />
          <div className="min-w-0 flex-1 text-[13px]"><b>{t('links.request_title', { name: rq.from })}</b><div className="num text-[12px] text-mute">{rq.ip} · {datetime(rq.at)}</div></div>
          <Button size="sm" variant="primary" onClick={() => setTaking(rq.code)}>{t('links.request_review')}</Button>
          <Button size="sm" variant="ghost" onClick={() => void del(`/api/v1/links/requests/${rq.id}`).then(() => d.reload())}>{t('links.request_refuse')}</Button>
        </div>
      ))}
      <Box title={t('links.title')}>
        <div className="grid gap-3 p-4 md:grid-cols-2">
          <div className="rounded-xl border border-line p-3">
            <div className="flex items-center gap-2 text-[13.5px] font-semibold"><Server className="size-4 text-acc" />{t('links.way_agent')}</div>
            <p className="mt-1 text-[12.5px] text-mute">{t('links.way_agent_d')}</p>
            <Link to="/local/hosts?add=1" className="mt-2 inline-flex items-center gap-1 text-[12.5px] text-acc hover:underline">{t('hosts.add')}<ArrowRight className="size-3.5" /></Link>
          </div>
          <div className="rounded-xl border border-acc/50 bg-tint p-3">
            <div className="flex items-center gap-2 text-[13.5px] font-semibold"><Link2 className="size-4 text-acc" />{t('links.way_link')}</div>
            <p className="mt-1 text-[12.5px] text-mute">{t('links.way_link_d')}</p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button size="sm" variant="primary" onClick={() => setScanning(true)}><Radar className="size-3.5" />{t('links.scan')}</Button>
              {d.data && <Discoverable until={d.data.discoverable_until} onChange={() => void d.reload()} />}
            </div>
            <div className="mt-2 text-[12px] text-mute">
              {t('links.cross_net')} <button type="button" className="text-acc hover:underline" onClick={() => setMaking(true)}>{t('links.make_short')}</button>
              {' · '}<button type="button" className="text-acc hover:underline" onClick={() => setTaking('')}>{t('links.take_short')}</button>
            </div>
          </div>
        </div>
        <div className="border-t border-line px-4 py-2 text-[12px] text-mute">{t('links.security_note')}</div>
      </Box>

      <Box title={t('links.i_manage')} count={main.length}>
        {main.length === 0 && <Empty title={t('links.none_main')}>{t('links.none_main_d')}</Empty>}
        {main.map((l) => <LinkCard key={l.id} l={l} onChange={() => void d.reload()} />)}
      </Box>
      <Box title={t('links.managed_by')} count={managed.length}>
        {managed.length === 0 && <Empty title={t('links.none_managed')}>{t('links.none_managed_d')}</Empty>}
        {managed.map((l) => <LinkCard key={l.id} l={l} onChange={() => void d.reload()} />)}
      </Box>

      {scanning && <ScanDialog onClose={() => { setScanning(false); void d.reload() }} />}
      {making && d.data && <MakeDialog data={d.data} onClose={() => { setMaking(false); void d.reload() }} />}
      {taking !== null && <TakeDialog initial={taking} onClose={() => { setTaking(null); void d.reload() }} />}
    </div>
  )
}

// ConnBadge: a small tag on the LAN; on the internet a clear tag plus how it travels
export function ConnBadge({ live }: { live?: Live }) {
  const t = useT()
  if (!live?.online) return <Pill tone="warn">{t('links.offline')}</Pill>
  if (live.route === 'lan') return <span className="rounded bg-line px-1.5 py-px text-[11px] text-mute" data-route="lan">{t('links.route_lan')}</span>
  if (live.route === 'tailscale') return <span className="rounded bg-ok/15 px-1.5 py-px text-[11px] text-ok" data-route="tailscale">{t('links.route_tailscale')}</span>
  return (
    <span className="inline-flex items-center gap-1" data-route="public">
      <Pill tone="acc"><Globe className="mr-0.5 inline size-3" />{t('links.route_public')}</Pill>
      {live.via === 'direct' && <span className="text-[11px] text-ok">{t('links.via_direct')}</span>}
      {live.via === 'cdn' && <span className="text-[11px] text-warn">{t('links.via_cdn', { cdn: live.cdn ?? '' })}</span>}
      {live.via === 'proxy' && <span className="text-[11px] text-warn">{t('links.via_proxy', { issuer: live.issuer ?? '' })}</span>}
    </span>
  )
}

export function countryName(code: string | undefined, locale: string) {
  if (!code) return ''
  try { return new Intl.DisplayNames([locale], { type: 'region' }).of(code.toUpperCase()) ?? code } catch { return code }
}

// LiveLine: latency, the peer's address and where it is
export function LiveLine({ live }: { live?: Live }) {
  const t = useT()
  const [locale] = useLocale()
  if (!live?.online) return live?.error ? <span className="text-[12px] text-mute">{live.error}</span> : null
  return (
    <span className="num inline-flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[12px] text-mute">
      {live.rtt_ms !== undefined && <span title={t('links.rtt_hint')}>{t('links.rtt', { ms: live.rtt_ms.toFixed(live.rtt_ms < 10 ? 1 : 0) })}{live.jitter_ms ? ` ±${live.jitter_ms}` : ''}</span>}
      {live.ip && <span>{live.ip}</span>}
      {live.route === 'public' && live.country && <span>{countryName(live.country, locale)}</span>}
      {live.route === 'lan' && <span>{t('links.same_lan')}</span>}
    </span>
  )
}

function LinkCard({ l, onChange }: { l: LinkItem; onChange: () => void }) {
  const t = useT()
  const reauth = useReauth()
  const nav = useNavigate()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const confirm = async () => {
    setErr(''); setBusy(true)
    try { await reauth(() => post(`/api/v1/links/${l.id}/confirm`)); onChange() } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  const unlink = async () => {
    if (!window.confirm(t(l.role === 'main' ? 'links.unlink_main_q' : 'links.unlink_managed_q', { name: l.name }))) return
    try { await del(`/api/v1/links/${l.id}`); onChange() } catch (e) { setErr(errText(e)) }
  }
  const live = l.live
  const insecure = l.status === 'active' && live?.online && live.route === 'public' && live.via !== 'direct'
  return (
    <div className="border-b border-line px-4 py-3 last:border-b-0" data-link={l.id}>
      <div className="flex flex-wrap items-center gap-2">
        <Dot state={l.status !== 'active' ? 'warn' : live?.online ? 'ok' : 'off'} />
        <span className="text-[14px] font-semibold">{l.name || '—'}</span>
        <Pill>{t(l.role === 'main' ? 'links.role_main' : 'links.role_managed')}</Pill>
        {l.status !== 'active' && <Pill tone="warn">{t('links.st_' + l.status)}</Pill>}
        {l.status === 'active' && <ConnBadge live={live} />}
        <span className="ml-auto flex gap-1">
          {l.status === 'confirm' && <Button size="sm" variant="primary" disabled={busy} onClick={() => void confirm()}><Check className="size-3.5" />{t('links.confirm')}</Button>}
          <Button size="sm" variant="ghost" onClick={() => void unlink()}><Unlink className="size-3.5" />{t('links.unlink')}</Button>
        </span>
      </div>
      {l.status === 'active' && <div className="mt-1"><LiveLine live={live} /></div>}
      {err && <div className="mt-2"><Notice tone="bad" >{err}</Notice></div>}
      {l.status === 'confirm' && <div className="mt-2"><Notice>{t('links.confirm_hint', { sas: l.sas ?? '', hosts: l.hosts.join(', ') })}</Notice></div>}
      {l.status === 'wait' && <div className="mt-2"><Notice>{t('links.wait_hint', { sas: l.sas ?? '' })}</Notice></div>}
      {insecure && <div className="mt-2"><Notice tone="warn" ><ShieldAlert className="mr-1 inline size-3.5" />{t('links.proxy_warn')}</Notice></div>}
      <div className="mt-2 grid gap-1 text-[12.5px] sm:grid-cols-[110px_1fr]">
        <span className="text-mute">{t(l.role === 'main' ? 'links.their_servers' : 'links.handed_over')}</span>
        <span className="flex flex-wrap gap-1.5">
          {l.hosts.map((h) => {
            const on = live?.hosts?.find((x) => x.name === h)
            return l.role === 'main' && l.status === 'active'
              ? <button key={h} type="button" onClick={() => nav(`/${encodeURIComponent(h + '@' + l.slug)}/overview`)} className="inline-flex items-center gap-1 rounded border border-line px-1.5 py-0.5 hover:border-acc" data-peer-host={h + '@' + l.slug}>
                <Dot state={on?.online ? 'ok' : 'off'} />{h}<span className="text-mute">@{l.slug}</span></button>
              : <span key={h} className="rounded border border-line px-1.5 py-0.5">{h}</span>
          })}
        </span>
        <span className="text-mute">{t('links.addrs')}</span>
        <span className="num break-all text-mute">{l.addrs.map((x) => <span key={x} className={cx('mr-2', x === (live?.addr ?? l.last_addr) && 'text-fg')}>{x}</span>)}</span>
        <span className="text-mute">{t('links.fp')}</span>
        <span className="num break-all text-[11.5px] text-mute">{l.fp}</span>
        {l.confirmed_at ? <><span className="text-mute">{t('links.since')}</span><span className="text-mute">{datetime(l.confirmed_at)} · {l.by}</span></> : null}
      </div>
    </div>
  )
}

// MakeDialog (main side): a code to paste on the other panel, or a link to open there
function MakeDialog({ data, onClose }: { data: Data; onClose: () => void }) {
  const t = useT()
  const [code, setCode] = useState<{ id: number; code: string; expires_at: number } | null>(null)
  const [err, setErr] = useState('')
  const [peer, setPeer] = useState('')
  const [copied, setCopied] = useState('')
  const [pending, setPending] = useState<LinkItem | null>(null)
  const [ours, setOurs] = useState(data.addrs.join('\n'))
  const make = () => { setErr(''); post<{ id: number; code: string; expires_at: number }>('/api/v1/links/code', { addrs: ours.split(/\s+/).filter(Boolean) }).then(setCode).catch((e) => setErr(errText(e))) }
  // the other panel accepted: this one shows the confirmation right here
  const d = useApi<Data>('/api/v1/links', [])
  useEvents(['link.pending'], () => void d.reload())
  useInterval(() => void d.reload(), 4000)
  useEffect(() => { const p = d.data?.items.find((l) => l.id === code?.id && l.status === 'confirm'); if (p) setPending(p) }, [d.data, code])
  const peerLink = useMemo(() => {
    const base = peer.trim().replace(/\/+$/, '')
    if (!code || !base) return ''
    return (/^https?:\/\//.test(base) ? base : 'https://' + base) + '/local/links#accept=' + encodeURIComponent(code.code)
  }, [peer, code])
  const copy = (s: string, k: string) => { void navigator.clipboard?.writeText(s); setCopied(k); setTimeout(() => setCopied(''), 1500) }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('links.make')}
      footer={<Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {pending
          ? <div className="space-y-2">
            <Notice tone="acc">{t('links.make_accepted', { name: pending.name, hosts: pending.hosts.join(', ') })}</Notice>
            <div className="text-center"><div className="text-[12px] text-mute">{t('links.sas')}</div><div className="num text-[26px] font-semibold tracking-widest" data-sas>{pending.sas}</div></div>
            <LinkCard l={pending} onChange={() => { void d.reload(); onClose() }} />
          </div>
          : <>
            <p className="text-mute">{t('links.make_d')}</p>
            {!code && <>
              <Field label={t('links.our_addrs_for_peer')} hint={t('links.our_addrs_for_peer_hint')}>
                <Textarea rows={3} className="num text-[12px]" value={ours} onChange={(e) => setOurs(e.target.value)} data-our-addrs />
              </Field>
              <Button variant="primary" onClick={make}>{t('links.make_code')}</Button>
            </>}
            {code && <>
              <Field label={t('links.code')} hint={t('links.code_hint', { time: datetime(code.expires_at) })}>
                <div className="flex items-start gap-2">
                  <Textarea readOnly rows={3} className="num text-[11.5px]" value={code.code} data-link-code onFocus={(e) => e.target.select()} />
                  <Button size="sm" onClick={() => copy(code.code, 'code')}>{copied === 'code' ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}{t('common.copy')}</Button>
                </div>
              </Field>
              <Field label={t('links.peer_addr')} hint={t('links.peer_addr_hint')}>
                <Input className="num" placeholder="https://192.168.1.20:7090" value={peer} onChange={(e) => setPeer(e.target.value)} />
              </Field>
              {peerLink && <div className="flex items-center gap-2">
                <a href={peerLink} target="_blank" rel="noreferrer" className="num min-w-0 flex-1 truncate text-[12px] text-acc hover:underline">{peerLink}</a>
                <Button size="sm" onClick={() => copy(peerLink, 'link')}>{copied === 'link' ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}{t('common.copy')}</Button>
              </div>}
              <div className="text-[12px] text-mute">{t('links.our_addrs')} <span className="num">{ours.split(/\s+/).filter(Boolean).join(' · ')}</span></div>
              <div className="flex items-center gap-2 text-[12px] text-mute"><span className="size-2 animate-pulse rounded-full bg-acc" />{t('links.waiting_peer')}</div>
            </>}
          </>}
      </div>
    </Dialog>
  )
}

// TakeDialog (managed side, also the landing page): check the code, see who wants what, hand servers over
function TakeDialog({ initial, onClose }: { initial: string; onClose: () => void }) {
  const t = useT()
  const reauth = useReauth()
  const [code, setCode] = useState(initial)
  const [info, setInfo] = useState<{ name: string; version: string; addr: string; addrs: string[]; fp: string; sas: string; hosts: string[]; self_addrs: string[]; host: string } | null>(null)
  const [pick, setPick] = useState<string[]>([])
  const [addrs, setAddrs] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<string | null>(null)
  const inspect = async (c = code) => {
    setErr(''); setBusy(true)
    try {
      const r = await post<NonNullable<typeof info>>('/api/v1/links/inspect', { code: c.trim() })
      setInfo(r); setPick(r.hosts); setAddrs(r.self_addrs.join('\n'))
    } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  useEffect(() => { if (initial) void inspect(initial) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const accept = async () => {
    setErr(''); setBusy(true)
    try {
      const r = await reauth(() => post<{ id: number; sas: string }>('/api/v1/links/accept', { code: code.trim(), hosts: pick, addrs: addrs.split(/\s+/).filter(Boolean) }))
      setDone(r.sas)
    } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('links.take')}
      footer={<>{!done && info && <Button variant="primary" className="ml-auto" disabled={busy || pick.length === 0} onClick={() => void accept()}>{t('links.hand_over', { n: pick.length })}</Button>}
        <Button className={done || !info ? 'ml-auto' : ''} onClick={onClose}>{t(done ? 'common.close' : 'common.cancel')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {done
          ? <>
            <Notice tone="acc">{t('links.handed', { name: info?.name ?? '' })}</Notice>
            <div className="text-center"><div className="text-[12px] text-mute">{t('links.sas')}</div><div className="num text-[26px] font-semibold tracking-widest" data-sas>{done}</div></div>
          </>
          : !info
            ? <>
              <p className="text-mute">{t('links.take_d')}</p>
              <Field label={t('links.code')}><Textarea rows={3} className="num text-[11.5px]" value={code} placeholder="berth-link:…" onChange={(e) => setCode(e.target.value)} data-take-code /></Field>
              <Button variant="primary" disabled={!code.trim() || busy} onClick={() => void inspect()}>{t('links.check')}</Button>
            </>
            : <>
              <div className="rounded-xl border border-line p-3">
                <div className="flex items-center gap-2"><House className="size-4 text-acc" /><b className="text-[14px]">{info.name}</b><span className="text-mute">{t('links.wants')}</span></div>
                <div className="num mt-1 text-[12px] text-mute">{info.addr} · {info.version}</div>
                <div className="num mt-0.5 break-all text-[11px] text-mute">{t('links.fp')} {info.fp}</div>
              </div>
              <div>
                <div className="mb-1 font-semibold">{t('links.pick_hosts')}</div>
                <div className="flex flex-wrap gap-2">
                  {info.hosts.map((h) => (
                    <label key={h} className="flex items-center gap-1.5 rounded border border-line px-2 py-1">
                      <input type="checkbox" checked={pick.includes(h)} onChange={(e) => setPick(e.target.checked ? [...pick, h] : pick.filter((x) => x !== h))} />{h}
                    </label>
                  ))}
                </div>
              </div>
              <Notice>{t('links.what_it_can')}</Notice>
              <Field label={t('links.our_addrs_for_main')} hint={t('links.our_addrs_for_main_hint')}>
                <Textarea rows={2} className="num text-[12px]" value={addrs} onChange={(e) => setAddrs(e.target.value)} />
              </Field>
              <div className="text-center"><div className="text-[12px] text-mute">{t('links.sas')}</div><div className="num text-[22px] font-semibold tracking-widest">{info.sas}</div><div className="text-[11.5px] text-mute">{t('links.sas_hint')}</div></div>
            </>}
      </div>
    </Dialog>
  )
}

// Discoverable: like Bluetooth, this panel answers LAN discovery for ten minutes
function Discoverable({ until, onChange }: { until: number; onChange: () => void }) {
  const t = useT()
  const [now, setNow] = useState(Date.now() / 1000)
  useInterval(() => setNow(Date.now() / 1000), until > now ? 1000 : null)
  const left = Math.max(0, Math.floor(until - now))
  const set = (open: boolean) => void post('/api/v1/links/discoverable', { open }).then(onChange)
  return left > 0
    ? <span className="inline-flex items-center gap-2 rounded-full bg-ok/15 px-2.5 py-1 text-[12px] text-ok" data-discoverable="on">
      <Wifi className="size-3.5 animate-pulse" />{t('links.discoverable_left', { time: `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}` })}
      <button type="button" className="text-mute hover:text-fg" onClick={() => set(false)}>{t('links.discoverable_stop')}</button>
    </span>
    : <Button size="sm" onClick={() => set(true)} title={t('links.discoverable_hint')} data-discoverable="off"><Wifi className="size-3.5" />{t('links.discoverable_open')}</Button>
}

interface Found { kind: 'berth' | 'wharf'; addr: string; ip: string; name?: string; version?: string; fp?: string; managed?: string; gateway?: boolean }

// ScanDialog: panels open for discovery on this LAN, and the router's Wharf
function ScanDialog({ onClose }: { onClose: () => void }) {
  const t = useT()
  const nav = useNavigate()
  const reauth = useReauth()
  const [res, setRes] = useState<{ items: Found[]; nets: string[] } | null>(null)
  const [err, setErr] = useState('')
  const [asked, setAsked] = useState<Record<string, 'sent' | 'done' | string>>({})
  const scan = () => { setRes(null); setErr(''); post<{ items: Found[]; nets: string[] }>('/api/v1/links/scan').then(setRes).catch((e) => setErr(errText(e))) }
  useEffect(scan, [])
  // after a request: wait for the other side to allow (this panel then confirms by itself)
  const links = useApi<Data>('/api/v1/links', [])
  useEvents(['link.pending'], () => void links.reload())
  useInterval(() => void links.reload(), Object.values(asked).includes('sent') ? 3000 : null)
  useEffect(() => {
    for (const [fp, st] of Object.entries(asked)) {
      if (st === 'sent' && links.data?.items.some((l) => l.role === 'main' && l.status === 'active' && l.fp === fp)) setAsked((a) => ({ ...a, [fp]: 'done' }))
    }
  }, [links.data, asked])
  const ask = async (f: Found) => {
    try { await reauth(() => post('/api/v1/links/request', { addr: f.addr, fp: f.fp })); setAsked((a) => ({ ...a, [f.fp!]: 'sent' })) } catch (e) { setAsked((a) => ({ ...a, [f.fp!]: errText(e) })) }
  }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('links.scan')}
      footer={<><Button variant="ghost" onClick={scan} disabled={!res}>{t('links.rescan')}</Button><Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        {err && <Notice tone="bad">{err}</Notice>}
        {!res && !err && <div className="flex items-center gap-2 py-6 text-mute"><Radar className="size-4 animate-spin" />{t('links.scanning')}</div>}
        {res && <div className="text-[12px] text-mute">{t('links.scanned', { nets: res.nets.join(', ') || '—' })}</div>}
        {res && res.items.length === 0 && <Notice>{t('links.none_found')}</Notice>}
        {res?.items.map((f) => (
          <div key={f.addr} className="flex flex-wrap items-center gap-3 rounded-xl border border-line p-3" data-found={f.kind}>
            {f.kind === 'wharf' ? <WharfMark className="size-6" /> : <Server className="size-5 text-acc" />}
            <div className="min-w-0 flex-1">
              <div className="font-semibold">{f.kind === 'wharf' ? t('links.found_wharf') : f.name}{f.gateway && <span className="ml-2 text-[11.5px] font-normal text-mute">{t('links.is_router')}</span>}</div>
              <div className="num text-[12px] text-mute">{f.addr}{f.version ? ` · ${f.version}` : ''}{f.managed ? ` · ${t('links.already_managed', { name: f.managed })}` : ''}</div>
            </div>
            {f.kind === 'wharf'
              ? <WharfAsk addr={f.addr} onManual={() => { onClose(); nav('/local/wharf?url=' + encodeURIComponent(f.addr)) }} />
              : asked[f.fp!] === 'done' ? <Pill tone="acc">{t('links.linked_now')}</Pill>
                : asked[f.fp!] === 'sent' ? <span className="flex items-center gap-2 text-[12px] text-mute"><span className="size-2 animate-pulse rounded-full bg-acc" />{t('links.wait_allow', { name: f.name ?? '' })}</span>
                  : <Button size="sm" variant="primary" disabled={!!f.managed} onClick={() => void ask(f)}>{t('links.ask_manage')}</Button>}
            {asked[f.fp ?? ''] && !['sent', 'done'].includes(asked[f.fp ?? '']) && <div className="w-full"><Notice tone="bad">{asked[f.fp!]}</Notice></div>}
          </div>
        ))}
      </div>
    </Dialog>
  )
}
