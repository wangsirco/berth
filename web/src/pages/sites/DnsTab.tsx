import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Switch } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Integration } from './types'

interface Rec { id: string; type: string; name: string; content: string; proxied?: boolean; ttl: number }
interface DDNS { id: number; host: string; integration: string; zone_id: string; name: string; type: string; proxied: boolean; enabled: boolean; last_ip?: string; last_check?: number; last_error?: string; source?: string }
interface WharfDDNS { id: string; domain: string; owner: string; ip?: string; status?: string; updated_at?: number }

const fieldLabels: Record<string, string> = { api_token: 'API Token', access_key_id: 'AccessKey ID', access_key_secret: 'AccessKey Secret', secret_id: 'SecretId', secret_key: 'SecretKey', secret_access_key: 'Secret Access Key', region: 'Region', token: 'Token' }
const plainFields = ['region', 'server', 'username', 'url']
// dyndns2 servers people use most
const DYN_SERVERS = [['No-IP', 'dynupdate.no-ip.com'], ['Dynu', 'api.dynu.com'], ['Hurricane Electric', 'dyn.dns.he.net'], ['FreeDNS (afraid.org)', 'freedns.afraid.org']]

export function DnsTab({ section }: { section?: 'accounts' | 'ddns' } = {}) {
  const show = (x: string) => !section || section === x
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const integ = useApi<{ items: Integration[]; kinds: Record<string, string[]>; caps: Record<string, string[]> }>('/api/v1/integrations')
  const ddns = useApi<{ items: DDNS[]; wharf?: { name: string; items: WharfDDNS[] } }>('/api/v1/ddns')
  const capOf = (kind: string, c: string) => integ.data?.caps?.[kind]?.includes(c) ?? false
  const ddnsAccounts = (integ.data?.items ?? []).filter((i) => capOf(i.kind, 'ddns'))
  const [add, setAdd] = useState(false)
  const [sel, setSel] = useState<{ acc: string; zone: string; name: string } | null>(null)
  const [newDdns, setNewDdns] = useState(false)
  const admin = can(user, 'settings.manage')
  const manage = can(user, 'sites.manage')
  const reload = () => { void integ.reload(); void ddns.reload() }
  return (
    <div className="space-y-4">
      {show('accounts') && <Box title={t('dns.accounts')} count={integ.data?.items.length} right={admin && <Button size="sm" variant="primary" onClick={() => setAdd(true)}>{t('dns.add_account')}</Button>}>
        {integ.data?.items.length === 0 && <Empty title={t('dns.no_accounts')}>{t('dns.no_accounts_hint')}</Empty>}
        {integ.data?.items.map((i) => (
          <div key={i.id}>
            <Row>
              <div className="min-w-0 flex-1">
                <div className="text-[13.5px] font-semibold">{i.name} <span className="font-normal text-mute">· {t('dns.kind_' + i.kind)} · {i.id}</span></div>
                <div className="flex flex-wrap gap-1 py-0.5">{(integ.data?.caps?.[i.kind] ?? []).map((c) => <Pill key={c}>{t('dns.cap_' + c)}</Pill>)}</div>
                <div className="text-[12px] text-mute">{i.hint}{i.check ? ' · ' + t('dns.check', { zones: i.check.zone_count }) : ''}{i.used_by?.length ? ' · ' + t('certs.used_by', { sites: i.used_by.join(', ') }) : ''}</div>
              </div>
              {i.check && <><Pill tone={i.check.zones_read ? 'ok' : 'bad'}>Zone:Read</Pill><Pill tone={i.check.dns_read ? 'ok' : 'bad'}>DNS:Read</Pill></>}
              {admin && <Button size="sm" variant="ghost" onClick={() => run('integration.delete', { id: i.id }, { host, onDone: reload })}>{t('common.delete')}</Button>}
            </Row>
            {capOf(i.kind, 'records') && <Zones acc={i.id} onPick={(zone, name) => setSel({ acc: i.id, zone, name })} />}
          </div>
        ))}
      </Box>}
      {sel && <Records acc={sel.acc} zone={sel.zone} zoneName={sel.name} manage={manage} onClose={() => setSel(null)} />}
      {show('ddns') && <Box title="DDNS" count={(ddns.data?.items.length ?? 0) + (ddns.data?.wharf?.items.length ?? 0)}
        right={manage && (ddnsAccounts.length > 0 || ddns.data?.wharf) && <Button size="sm" onClick={() => setNewDdns(true)}>{t('dns.add_ddns')}</Button>}>
        {ddns.data?.items.length === 0 && !ddns.data.wharf?.items.length && <Empty title={t('dns.no_ddns')}>{ddnsAccounts.length ? t('dns.no_ddns_hint') : t('dns.no_ddns_account')}</Empty>}
        {ddns.data?.items.map((d) => (
          <Row key={d.id}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{d.name} <span className="font-normal text-mute">{d.type} · {d.host} · {t('dns.src_' + (d.source?.split(':')[0] || 'auto'), { v: d.source?.split(':').slice(1).join(':') ?? '' })}</span></div>
              <div className="text-[12px] text-mute">{d.last_ip || '—'}{d.last_check ? ' · ' + ago(d.last_check, t) : ''}{d.last_error ? ' · ' : ''}{d.last_error && <span className="text-bad">{d.last_error}</span>}</div>
              {!d.last_error && d.last_ip && <div className="text-[12px] text-mute">{t('dns.cname_hint', { name: d.name })}</div>}
            </div>
            {!d.enabled && <Pill>{t('sites.disabled')}</Pill>}
            {manage && <Button size="sm" variant="ghost" onClick={() => run('ddns.delete', { id: d.id }, { host: d.host, onDone: reload })}>{t('common.delete')}</Button>}
          </Row>
        ))}
        {ddns.data?.wharf?.items.map((d) => (
          <Row key={'w' + d.id}>
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{d.domain} <span className="font-normal text-mute">· {t('dns.kept_by_wharf', { name: ddns.data!.wharf!.name })}</span></div>
              <div className="text-[12px] text-mute">{d.ip || '—'}{d.updated_at ? ' · ' + ago(d.updated_at, t) : ''}{d.status ? ' · ' + d.status : ''}</div>
            </div>
            <Pill tone="acc">Wharf</Pill>
          </Row>
        ))}
      </Box>}
      {add && <AccountDialog kinds={integ.data?.kinds ?? {}} onClose={() => setAdd(false)} onDone={reload} />}
      {newDdns && <DdnsDialog accounts={ddnsAccounts} caps={integ.data?.caps ?? {}} wharf={ddns.data?.wharf?.name} onClose={() => setNewDdns(false)} onDone={reload} />}
    </div>
  )
}

function Zones({ acc, onPick }: { acc: string; onPick: (zone: string, name: string) => void }) {
  const z = useApi<{ items: { id: string; name: string; status: string }[] }>(`/api/v1/integrations/${acc}/zones`, [acc])
  if (z.error) return <p className="px-4 pb-2 text-[12px] text-bad">{z.error.message}</p>
  return <div className="flex flex-wrap gap-1 px-4 pb-2">{z.data?.items.map((x) => <button key={x.id} className="rounded border border-line px-1.5 text-[12px] hover:border-acc" onClick={() => onPick(x.id, x.name)}>{x.name}</button>)}</div>
}

export function Records({ acc, zone, zoneName, manage, onClose }: { acc: string; zone: string; zoneName: string; manage: boolean; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const recs = useApi<{ items: Rec[] }>(`/api/v1/integrations/${acc}/zones/${zone}/records`, [zone])
  const [nr, setNr] = useState<Rec>({ id: '', type: 'A', name: '', content: '', proxied: false, ttl: 1 })
  const reload = () => void recs.reload()
  return (
    <Box title={t('dns.records', { zone: zoneName })} count={recs.data?.items.length} right={<Button size="sm" variant="ghost" onClick={onClose}>{t('common.close')}</Button>}>
      {recs.error && <Notice tone="bad">{recs.error.message}</Notice>}
      {recs.data?.items.map((r) => (
        <Row key={r.id}>
          <span className="num w-14 text-[12px] text-mute">{r.type}</span>
          <span className="num min-w-0 flex-1 truncate text-[13px]">{r.name}</span>
          <span className="num min-w-0 max-w-[40%] truncate text-[12.5px]">{r.content}</span>
          {r.proxied && <Pill tone="acc">{t('sites.cf_proxied')}</Pill>}
          {manage && <Button size="sm" variant="ghost" onClick={() => run('dns.record.delete', { integration: acc, zone_id: zone, id: r.id }, { host, onDone: reload })}>{t('common.delete')}</Button>}
        </Row>
      ))}
      {manage && <div className="flex flex-wrap items-center gap-2 px-4 py-2">
        <Select value={nr.type} onChange={(e) => setNr({ ...nr, type: e.target.value })} aria-label="type">{['A', 'AAAA', 'CNAME', 'TXT', 'MX'].map((x) => <option key={x}>{x}</option>)}</Select>
        <Input className="w-56" value={nr.name} onChange={(e) => setNr({ ...nr, name: e.target.value })} placeholder={'www.' + zoneName} aria-label={t('dns.name')} />
        <Input className="w-56" value={nr.content} onChange={(e) => setNr({ ...nr, content: e.target.value })} placeholder="203.0.113.5" aria-label={t('dns.content')} />
        <Switch checked={!!nr.proxied} onChange={(v) => setNr({ ...nr, proxied: v })} label={t('sites.cf_proxied')} />
        <Button size="sm" variant="primary" onClick={() => run('dns.record.create', { integration: acc, zone_id: zone, record: { ...nr, id: undefined } }, { host, onDone: reload })}>{t('action.dns.record.create')}</Button>
      </div>}
    </Box>
  )
}

function AccountDialog({ kinds, onClose, onDone }: { kinds: Record<string, string[]>; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [kind, setKind] = useState('cloudflare')
  const [id, setId] = useState('cf')
  const [name, setName] = useState('')
  const [cred, setCred] = useState<Record<string, string>>({})
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('dns.add_account')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={() => run('integration.set', { id, kind, name, credentials: cred }, { host, onDone: () => { onDone(); onClose() } })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        {Object.keys(kinds).length === 0 ? <div className="text-mute">{t('common.loading')}</div> : <Field label={t('dns.provider')}><Select value={kind} onChange={(e) => { if (!e.target.value) return; setKind(e.target.value); setCred({}); setId(e.target.value === 'cloudflare' ? 'cf' : e.target.value) }}>
          {Object.keys(kinds).map((k) => <option key={k} value={k}>{t('dns.kind_' + k)}</option>)}
        </Select></Field>}
        <p className="text-[12.5px] text-mute">{t('dns.kind_hint_' + kind)}</p>
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('dns.account_id')} hint={t('dns.account_id_hint')}><Input value={id} onChange={(e) => setId(e.target.value)} /></Field>
          <Field label={t('dns.account_name')}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        </div>
        {(kinds[kind] ?? []).map((f) => (
          <Field key={f} label={fieldLabels[f] ?? t('dns.field_' + f)} hint={f === 'url' ? t('dns.url_hint') : undefined}>
            {f === 'server'
              ? <div className="flex flex-wrap gap-1.5">
                {DYN_SERVERS.map(([n, h]) => <button key={h} type="button" onClick={() => setCred({ ...cred, server: h })} className={'rounded border px-2 py-0.5 text-[12px] ' + (cred.server === h ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>{n}</button>)}
                <Input className="num" value={cred.server ?? ''} placeholder="dynupdate.no-ip.com" onChange={(e) => setCred({ ...cred, server: e.target.value })} />
              </div>
              : <Input type={plainFields.includes(f) || f.endsWith('_id') ? 'text' : 'password'} autoComplete="off" value={cred[f] ?? ''} onChange={(e) => setCred({ ...cred, [f]: e.target.value })}
                placeholder={f === 'url' ? 'https://example.com/update?host={domain}&ip={ip}' : undefined} />}
          </Field>
        ))}
        <p className="text-[12px] text-mute">{t('dns.sealed_note')}</p>
      </div>
    </Dialog>
  )
}

interface NicOpt { name: string; kind: string; state: string; addrs: { ip: string; family: string; scope: string; public: boolean }[]; default: boolean }

// A new DDNS name: who keeps it (this server or the router), the service, the name, and
// which address it should carry — chosen from what this server can actually see.
function DdnsDialog({ accounts, caps, wharf, onClose, onDone }: { accounts: Integration[]; caps: Record<string, string[]>; wharf?: string; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [keeper, setKeeper] = useState<'berth' | 'wharf'>(accounts.length ? 'berth' : 'wharf')
  const [acc, setAcc] = useState(accounts[0]?.id ?? '')
  const account = accounts.find((a) => a.id === acc)
  const records = !!account && caps[account.kind]?.includes('records')
  const [zone, setZone] = useState('')
  const [sub, setSub] = useState('home')
  const [full, setFull] = useState('')
  const [type, setType] = useState<'A' | 'AAAA'>('A')
  const [proxied, setProxied] = useState(false)
  const [source, setSource] = useState('auto')
  const [fixed, setFixed] = useState('')
  const zones = useApi<{ items: { id: string; name: string }[] }>(records ? `/api/v1/integrations/${acc}/zones` : null, [acc, records])
  const zoneName = zones.data?.items.find((z) => z.id === zone)?.name ?? ''
  const name = keeper === 'wharf' || !records ? full.trim().toLowerCase() : sub && zoneName ? (sub === '@' ? zoneName : `${sub}.${zoneName}`) : ''
  const pub = useApi<{ ipv4?: string; ipv6?: string }>(`/api/v1/hosts/${host}/public-ip`, [host])
  const nics = useApi<{ items: NicOpt[] }>(`/api/v1/hosts/${host}/system/nics`, [host])
  const cards = (nics.data?.items ?? []).filter((n) => ['ethernet', 'wifi', 'vpn'].includes(n.kind) && n.addrs.some((a) => a.scope === 'global'))
  const [egress, setEgress] = useState<Record<string, string>>({})
  useEffect(() => { // what each card goes out as (one probe each)
    for (const n of cards) {
      if (egress[n.name + type] !== undefined) continue
      setEgress((e) => ({ ...e, [n.name + type]: '…' }))
      get<{ ip: string }>(`/api/v1/hosts/${host}/system/nics/${encodeURIComponent(n.name)}/public-ip?family=${type === 'A' ? 4 : 6}`)
        .then((r) => setEgress((e) => ({ ...e, [n.name + type]: r.ip }))).catch(() => setEgress((e) => ({ ...e, [n.name + type]: '' })))
    }
  }, [cards.map((n) => n.name).join(','), type]) // eslint-disable-line react-hooks/exhaustive-deps
  const auto = type === 'A' ? pub.data?.ipv4 : pub.data?.ipv6
  const src = source === 'fixed' ? 'fixed:' + fixed.trim() : source
  const submit = () => keeper === 'wharf'
    ? run('wharf.ddns.claim', { domain: name, owner: 'wharf' }, { host, onDone: () => { onDone(); onClose() } })
    : run('ddns.set', { integration: acc, zone_id: records ? zone : '', name, type, proxied: proxied && account?.kind === 'cloudflare', source: src }, { host, onDone: () => { onDone(); onClose() } })
  const opt = (value: string, title: React.ReactNode, detail: React.ReactNode) => (
    <label key={value} className={'flex cursor-pointer items-start gap-2 rounded-[var(--radius-box)] border px-2.5 py-1.5 ' + (source === value ? 'border-acc bg-tint' : 'border-line')}>
      <input type="radio" className="mt-1" checked={source === value} onChange={() => setSource(value)} />
      <div className="min-w-0 flex-1"><div className="font-semibold">{title}</div><div className="num text-[12px] text-mute">{detail}</div></div>
    </label>
  )
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('dns.add_ddns')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!name.includes('.') || (keeper === 'berth' && source === 'fixed' && !fixed)} onClick={submit}>{t('common.continue')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        {wharf && <Field label={t('dns.keeper')}>
          <Segmented value={keeper} onChange={setKeeper} options={[...(accounts.length ? [{ value: 'berth' as const, label: t('dns.keeper_berth') }] : []), { value: 'wharf' as const, label: t('dns.keeper_wharf', { name: wharf }) }]} />
        </Field>}
        {keeper === 'wharf' ? <>
          <Notice>{t('dns.wharf_keeps')}</Notice>
          <Field label={t('dns.name')}><Input className="num" value={full} onChange={(e) => setFull(e.target.value)} placeholder="home.example.com" /></Field>
        </> : <>
          <Field label={t('dns.service')}>
            <Select value={acc} onChange={(e) => setAcc(e.target.value)}>{accounts.map((a) => <option key={a.id} value={a.id}>{a.name} · {t('dns.kind_' + a.kind)}</option>)}</Select>
          </Field>
          {records ? (
            <div className="grid gap-2 sm:grid-cols-[1fr_1.3fr]">
              <Field label={t('dns.sub')} hint={t('dns.sub_hint')}><Input className="num" value={sub} onChange={(e) => setSub(e.target.value.trim().toLowerCase())} /></Field>
              <Field label={t('sites.zone')}><Select value={zone} onChange={(e) => setZone(e.target.value)}><option value="" />{zones.data?.items.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}</Select></Field>
            </div>
          ) : (
            <Field label={t('dns.name')} hint={account?.kind === 'duckdns' ? t('dns.duck_hint') : t('dns.full_hint')}>
              <Input className="num" value={full} onChange={(e) => setFull(e.target.value)} placeholder={account?.kind === 'duckdns' ? 'myhome.duckdns.org' : 'myhome.ddns.net'} />
            </Field>
          )}
          {name && <div className="num text-[12.5px]">{t('dns.will_point', { name })}</div>}
          <Field label={t('dns.record_type')}>
            <div className="flex items-center gap-3">
              <Segmented value={type} onChange={setType} options={[{ value: 'A', label: 'IPv4 (A)' }, { value: 'AAAA', label: 'IPv6 (AAAA)' }]} />
              {account?.kind === 'cloudflare' && <Switch checked={proxied} onChange={setProxied} label={t('sites.cf_proxied')} />}
            </div>
          </Field>
          <div>
            <div className="mb-1.5 font-semibold">{t('dns.which_ip')}</div>
            <div className="space-y-1.5">
              {opt('auto', t('dns.src_auto_title'), auto ? t('dns.now_is', { ip: auto }) : t('dns.detecting'))}
              {cards.map((n) => opt('iface:' + n.name, <>{t('dns.src_iface_title', { name: n.name })}{n.default && <span className="ml-1.5 text-[11.5px] font-normal text-mute">{t('system.default_route')}</span>}</>,
                <>{n.addrs.filter((a) => a.scope === 'global' && a.family === (type === 'A' ? '4' : '6')).map((a) => a.ip).join(' · ') || '—'}{' → '}
                  {egress[n.name + type] === '…' ? t('dns.detecting') : egress[n.name + type] ? t('dns.goes_out_as', { ip: egress[n.name + type] }) : t('system.no_internet')}</>))}
              {wharf && opt('wharf', t('dns.src_wharf_title', { name: wharf }), t('dns.src_wharf_hint'))}
              {opt('fixed', t('dns.src_fixed_title'), source === 'fixed'
                ? <Input className="num mt-1" value={fixed} onChange={(e) => setFixed(e.target.value)} placeholder={type === 'A' ? '203.0.113.5' : '2001:db8::5'} />
                : t('dns.src_fixed_hint'))}
            </div>
            {cards.length > 1 && <p className="mt-1.5 text-[12px] text-mute">{t('dns.multi_wan_hint')}</p>}
          </div>
        </>}
        <p className="text-[12px] text-mute">{t('dns.after_ddns')}</p>
      </div>
    </Dialog>
  )
}
