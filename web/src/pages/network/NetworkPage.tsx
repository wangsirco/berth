import { CloudflareWelcome, TokenDialog } from '../cloudflare/Welcome'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Navigate } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { FlowsView } from '@/components/FlowsView'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Segmented, Select, Switch, Textarea } from '@/components/ui/Field'
import { ago, bytes, datetime } from '@/lib/format'
import { can, useAuth } from '@/lib/auth'
import { del, errText, get, post, put } from '@/lib/api'
import { domainsShort, type Site } from '@/pages/sites/types'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { ConnBadge, type Live as LinkLive } from '../links/LinksPage'
import { useEvents } from '@/lib/useEvents'
import { NeedsExtension, useBuiltin } from '../extensions/Builtin'
import { TabBar } from '@/components/ui/Tabs'

interface Rule { id: number; action: 'allow' | 'deny'; proto: 'tcp' | 'udp' | 'any'; ports: string; source: 'any' | 'ip' | 'cidr' | 'cloudflare' | 'country'; value: string; except: boolean; comment: string; enabled: boolean }
interface Listener { proto: string; addr: string; port: number; public: boolean; process?: string; pid?: number }
interface FwState { firewalld: boolean; zone?: string; open_ports?: string[]; services?: string[]; rich_rules?: string[]; docker: boolean; pending: boolean; rollback_at?: number; nft_available: boolean }
interface Fw { state: FwState; listening: Listener[]; rules: Rule[]; your_ip: string }

type Tab = 'ports' | 'rules' | 'bans' | 'waf' | 'access' | 'traffic' | 'cloudflare' | 'wharf'

export default function NetworkPage() {
  const t = useT()
  const { host } = useHost()
  const [tab, setTab] = useState<Tab>(() => (new URLSearchParams(location.search).get('tab') as Tab) || 'rules')
  const fw = useApi<Fw>(`/api/v1/hosts/${host}/firewall`, [host])
  useEvents(['firewall.pending', 'change.done'], () => void fw.reload())
  if (tab === 'cloudflare') return <Navigate to={hostPath(host, 'cloudflare')} replace /> // moved to its own page
  if (tab === 'wharf') return <Navigate to={hostPath(host, 'wharf')} replace />
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.network')} value={tab} items={[{ value: 'rules', label: t('network.rules') }, { value: 'bans', label: t('network.bans') }, { value: 'waf', label: 'WAF' }, { value: 'access', label: t('network.access') }, { value: 'traffic', label: t('network.traffic') }, { value: 'ports', label: t('network.ports') }]} onChange={(x) => { setTab(x); history.replaceState(null, '', '?tab=' + x) }} />
      {fw.error && <Notice tone="bad">{fw.error.message}</Notice>}
      {!fw.data && !fw.error && (tab === 'rules' || tab === 'ports') && <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>}
      {fw.data && tab === 'rules' && <Rules fw={fw.data} />}
      {fw.data && tab === 'ports' && <Ports fw={fw.data} />}
      {tab === 'bans' && <Bans />}
      {tab === 'waf' && <Waf />}
      {tab === 'access' && <Access />}
      {tab === 'traffic' && <Traffic />}
    </div>
  )
}

function srcText(r: Rule, t: ReturnType<typeof useT>) {
  const s = r.source === 'any' ? t('network.src_any') : r.source === 'cloudflare' ? 'Cloudflare' : r.source === 'country' ? t('network.src_country_v', { c: r.value }) : r.value
  return r.except ? t('network.except', { s }) : s
}

function Rules({ fw }: { fw: Fw }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'network.manage')
  const [edit, setEdit] = useState<Rule | 'new' | null>(null)
  return (
    <>
      <Notice>
        {t('network.how', { ip: fw.your_ip })}
        {fw.state.firewalld && ' ' + t('network.with_firewalld', { zone: fw.state.zone ?? '' })}
        {fw.state.docker && ' ' + t('network.with_docker')}
      </Notice>
      <Box title={t('network.rules')} count={fw.rules.length} right={manage && <Button size="sm" variant="primary" onClick={() => setEdit('new')}>{t('network.new_rule')}</Button>}>
        {fw.rules.length === 0 && <Empty title={t('network.no_rules')}>{t('network.no_rules_hint')}</Empty>}
        {fw.rules.map((r, i) => (
          <Row key={r.id} onClick={manage ? () => setEdit(r) : undefined}>
            <span className="num w-6 text-right text-[12px] text-mute">{i + 1}</span>
            <Dot state={!r.enabled ? 'off' : r.action === 'allow' ? 'ok' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">
                {t('network.act_' + r.action)} <span className="num">{r.proto === 'any' ? t('network.all_traffic') : r.proto.toUpperCase() + ' ' + (r.ports || t('network.all_ports'))}</span>
                <span className="font-normal text-mute"> · {srcText(r, t)}</span>
              </div>
              {r.comment && <div className="truncate text-[12px] text-mute">{r.comment}</div>}
            </div>
            {!r.enabled && <Pill>{t('network.disabled')}</Pill>}
            {manage && <span onClick={(e) => e.stopPropagation()}><Button size="sm" variant="ghost" onClick={() => run('firewall.rule.delete', { id: r.id }, { host })}>{t('common.delete')}</Button></span>}
          </Row>
        ))}
      </Box>
      {fw.state.firewalld && (fw.state.open_ports?.length || fw.state.services?.length || fw.state.rich_rules?.length) ? (
        <Box title={t('network.firewalld_open', { zone: fw.state.zone ?? '' })}>
          <div className="flex flex-wrap gap-1 px-4 py-3">
            {(fw.state.services ?? []).map((s) => <Pill key={s}>{s}</Pill>)}
            {(fw.state.open_ports ?? []).map((p) => <Pill key={p} tone="acc">{p}</Pill>)}
          </div>
          {(fw.state.rich_rules ?? []).length > 0 && <div className="num space-y-0.5 border-t border-line px-4 py-2 text-[11.5px] text-mute">{fw.state.rich_rules!.map((r) => <div key={r} className="break-all">{r}</div>)}</div>}
        </Box>
      ) : null}
      {edit && <RuleDialog rule={edit === 'new' ? undefined : edit} yourIP={fw.your_ip} onClose={() => setEdit(null)} />}
    </>
  )
}

function RuleDialog({ rule, yourIP, onClose }: { rule?: Rule; yourIP: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [v, setV] = useState<Rule>(rule ?? { id: 0, action: 'deny', proto: 'tcp', ports: '', source: 'any', value: '', except: false, comment: '', enabled: true })
  const set = <K extends keyof Rule>(k: K, x: Rule[K]) => setV({ ...v, [k]: x })
  const needsValue = v.source === 'ip' || v.source === 'cidr' || v.source === 'country'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={rule ? t('network.edit_rule') : t('network.new_rule')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={needsValue && !v.value} onClick={() => run('firewall.rule.set', { ...v, id: rule?.id || undefined }, { host, onDone: onClose })}>{t('action.firewall.rule.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Notice tone="warn">{t('network.grace')}</Notice>
        <Field label={t('network.action')}>
          <Segmented value={v.action} options={[{ value: 'deny', label: t('network.act_deny') }, { value: 'allow', label: t('network.act_allow') }]} onChange={(x) => setV({ ...v, action: x, except: x === 'allow' ? false : v.except })} />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('network.proto')}>
            <Select value={v.proto} onChange={(e) => setV({ ...v, proto: e.target.value as Rule['proto'], ports: e.target.value === 'any' ? '' : v.ports })}>
              <option value="tcp">TCP</option><option value="udp">UDP</option><option value="any">{t('network.all_traffic')}</option>
            </Select>
          </Field>
          <Field label={t('network.ports_label')} hint={t('network.ports_hint')}>
            <Input className="num" disabled={v.proto === 'any'} value={v.ports} placeholder={t('network.all_ports')} onChange={(e) => set('ports', e.target.value.replace(/\s/g, ''))} />
          </Field>
        </div>
        <Field label={t('network.source')}>
          <Select value={v.source} onChange={(e) => setV({ ...v, source: e.target.value as Rule['source'], value: '', except: e.target.value === 'any' ? false : v.except })}>
            <option value="any">{t('network.src_any')}</option>
            <option value="ip">{t('network.src_ip')}</option>
            <option value="cidr">{t('network.src_cidr')}</option>
            <option value="cloudflare">{t('network.src_cloudflare')}</option>
            <option value="country">{t('network.src_country')}</option>
          </Select>
        </Field>
        {needsValue && (
          <Field label={v.source === 'ip' ? t('network.src_ip') : v.source === 'country' ? t('network.src_country') : t('network.src_cidr')}
            hint={v.source === 'country' ? t('network.country_hint') : t('network.your_ip', { ip: yourIP })}>
            <Input className="num" value={v.value} placeholder={v.source === 'ip' ? '203.0.113.7' : v.source === 'country' ? 'KP' : '203.0.113.0/24'}
              onChange={(e) => set('value', v.source === 'country' ? e.target.value.trim().toUpperCase().slice(0, 2) : e.target.value.trim())} />
          </Field>
        )}
        {v.action === 'deny' && v.source !== 'any' && <Switch checked={v.except} onChange={(x) => set('except', x)} label={t('network.except_label')} />}
        <Field label={t('network.comment')}><Input value={v.comment} onChange={(e) => set('comment', e.target.value)} /></Field>
        <Switch checked={v.enabled} onChange={(x) => set('enabled', x)} label={t('network.enabled')} />
      </div>
    </Dialog>
  )
}

function Ports({ fw }: { fw: Fw }) {
  const t = useT()
  const open = new Set(fw.state.open_ports ?? [])
  return (
    <Box title={t('network.ports')} count={fw.listening.length}>
      {fw.listening.length === 0 && <Empty title={t('network.no_ports')} />}
      {fw.listening.map((l) => (
        <Row key={l.proto + l.addr + l.port + (l.process ?? '')}>
          <Dot state={l.public ? 'warn' : 'off'} />
          <span className="num w-24 text-[13px] font-semibold">{l.port}/{l.proto}</span>
          <div className="min-w-0 flex-1 text-[12.5px]">
            <span>{l.process ?? '—'}</span>{l.pid ? <span className="text-mute"> · pid {l.pid}</span> : null}
            <span className="num text-mute"> · {l.addr}</span>
          </div>
          {l.public ? <Pill tone="warn">{t('network.public')}</Pill> : <Pill>{t('network.local_only')}</Pill>}
          {fw.state.firewalld && l.public && open.has(`${l.port}/${l.proto}`) && <Pill tone="acc">{t('network.fwd_open')}</Pill>}
        </Row>
      ))}
    </Box>
  )
}

interface Ban { id: number; addr: string; reason: string; source: string; layers?: string[]; created_by: string; created_at: number; expires_at?: number; revoked_at?: number; strike?: number }
interface BanData { active: Ban[]; recent: Ban[]; failures: { ip: string; count: number; kinds: string; last: number }[]; your_ip: string; settings: { enabled: string; attempts: string; window: string; whitelist: string; waf_ban: string; cf_sync?: string; wharf_sync?: string } }

function banWhy(b: Ban, t: ReturnType<typeof useT>) {
  if (b.source === 'bruteforce') {
    const [, kinds, n] = b.reason.split(':')
    return t('network.ban_bf', { kinds: kinds ?? '', n: n ?? '' }) + (b.strike && b.strike > 1 ? ' · ' + t('network.ban_strike', { n: b.strike }) : '')
  }
  return b.reason || t('network.src_' + b.source)
}

function Bans() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'network.manage')
  const d = useApi<BanData>(`/api/v1/hosts/${host}/bans`, [host])
  useEvents(['ban', 'change.done'], () => void d.reload())
  const [add, setAdd] = useState<string | null>(null)
  const [cfg, setCfg] = useState(false)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const s = d.data.settings
  return (
    <>
      <Notice>
        {s.enabled === '0' ? t('network.bf_off') : t('network.bf_on', { n: s.attempts, m: s.window })}
        {manage && <button className="ml-2 text-acc hover:underline" onClick={() => setCfg(true)}>{t('network.bf_settings')}</button>}
      </Notice>
      <Box title={t('network.bans_active')} count={d.data.active.length} right={manage && <Button size="sm" variant="primary" onClick={() => setAdd('')}>{t('network.new_ban')}</Button>}>
        {d.data.active.length === 0 && <Empty title={t('network.no_bans')}>{t('network.no_bans_hint')}</Empty>}
        {d.data.active.map((b) => (
          <Row key={b.id}>
            <Dot state="bad" />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold"><span className="num">{b.addr}</span> <span className="font-normal text-mute">· {banWhy(b, t)}</span></div>
              <div className="text-[12px] text-mute">{datetime(b.created_at)} · {b.expires_at ? t('network.ban_until', { at: datetime(b.expires_at) }) : t('network.ban_forever')}</div>
            </div>
            <Pill tone={b.source === 'bruteforce' ? 'warn' : undefined}>{t('network.bsrc_' + b.source)}</Pill>
            {manage && s.cf_sync === '1' && <span className="whitespace-nowrap"><Switch checked={!!b.layers?.includes('cloudflare')} onChange={(on) => run('ban.sync', { id: b.id, cloudflare: on }, { host })} label="Cloudflare" /></span>}
            {manage && s.wharf_sync === '1' && b.source !== 'wharf' && <span className="whitespace-nowrap"><Switch checked={!!b.layers?.includes('wharf')} onChange={(on) => run('ban.sync', { id: b.id, wharf: on }, { host })} label="Wharf" /></span>}
            {manage && <Button size="sm" variant="ghost" onClick={() => run('ban.remove', { id: b.id }, { host })}>{t('action.ban.remove')}</Button>}
          </Row>
        ))}
      </Box>
      <Box title={t('network.failures_24h')} count={d.data.failures.length}>
        {d.data.failures.length === 0 && <Empty title={t('network.no_failures')} />}
        {d.data.failures.map((f) => (
          <Row key={f.ip}>
            <span className="num w-40 truncate text-[13px] font-semibold">{f.ip}</span>
            <div className="min-w-0 flex-1 text-[12.5px] text-mute">{t('network.fail_n', { n: f.count })} · {f.kinds} · {ago(f.last, t)}</div>
            {f.ip === d.data!.your_ip && <Pill tone="acc">{t('network.you')}</Pill>}
            {manage && !d.data!.active.some((b) => b.addr === f.ip) && <Button size="sm" variant="ghost" onClick={() => setAdd(f.ip)}>{t('action.ban.add')}</Button>}
          </Row>
        ))}
      </Box>
      {d.data.recent.length > 0 && <Box title={t('network.bans_recent')} count={d.data.recent.length}>
        {d.data.recent.map((b) => (
          <Row key={b.id}>
            <Dot state="off" />
            <span className="num w-40 truncate text-[13px]">{b.addr}</span>
            <div className="min-w-0 flex-1 truncate text-[12px] text-mute">{banWhy(b, t)} · {datetime(b.created_at)} · {b.revoked_at ? t('network.ban_revoked') : t('network.ban_expired')}</div>
          </Row>
        ))}
      </Box>}
      {add !== null && <BanDialog addr={add} yourIP={d.data.your_ip} onClose={() => setAdd(null)} />}
      {cfg && <BfSettings s={s} onClose={() => setCfg(false)} />}
    </>
  )
}

function BanDialog({ addr, yourIP, onClose }: { addr: string; yourIP: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [v, setV] = useState({ addr, minutes: 1440, reason: '' })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('network.new_ban')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!v.addr} onClick={() => run('ban.add', v, { host, onDone: onClose })}>{t('action.ban.add')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Field label={t('network.ban_addr')} hint={t('network.your_ip', { ip: yourIP })}><Input className="num" value={v.addr} placeholder="203.0.113.7 / 203.0.113.0/24" onChange={(e) => setV({ ...v, addr: e.target.value.trim() })} /></Field>
        <Field label={t('network.ban_for')}>
          <Select value={String(v.minutes)} onChange={(e) => setV({ ...v, minutes: Number(e.target.value) })}>
            {[[60, '1h'], [1440, '1d'], [10080, '7d'], [43200, '30d'], [0, 'forever']].map(([m, k]) => <option key={m} value={m}>{t('network.dur_' + k)}</option>)}
          </Select>
        </Field>
        <Field label={t('network.comment')}><Input value={v.reason} onChange={(e) => setV({ ...v, reason: e.target.value })} /></Field>
      </div>
    </Dialog>
  )
}

function BfSettings({ s, onClose }: { s: BanData['settings']; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const [v, setV] = useState(s)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('network.bf_settings')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={() => run('settings.update', { values: { 'security.bf_enabled': v.enabled, 'security.bf_attempts': v.attempts, 'security.bf_window_min': v.window, 'security.bf_whitelist': v.whitelist, 'security.waf_ban': v.waf_ban } }, { onDone: onClose })}>{t('common.save')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Switch checked={v.enabled !== '0'} onChange={(x) => setV({ ...v, enabled: x ? '1' : '0' })} label={t('network.bf_enable')} />
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('network.bf_attempts')}><Input type="number" value={v.attempts} onChange={(e) => setV({ ...v, attempts: e.target.value })} /></Field>
          <Field label={t('network.bf_window')}><Input type="number" value={v.window} onChange={(e) => setV({ ...v, window: e.target.value })} /></Field>
        </div>
        <Switch checked={v.waf_ban === '1'} onChange={(x) => setV({ ...v, waf_ban: x ? '1' : '0' })} label={t('network.waf_ban_label')} />
        <Field label={t('network.bf_whitelist')} hint={t('network.bf_whitelist_hint')}><Textarea rows={4} className="num" value={v.whitelist} onChange={(e) => setV({ ...v, whitelist: e.target.value })} /></Field>
        <p className="text-[12px] text-mute">{t('network.bf_ladder')}</p>
      </div>
    </Dialog>
  )
}

interface WafEv { id: number; site: number; domain?: string; ts: number; ip: string; uri: string; blocked: boolean; rules: { id: number; msg: string; severity?: string }[] }
interface WafData { items: WafEv[]; sites: { id: number; domain: string; mode: string }[]; ban: boolean }

function Waf() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const [site, setSite] = useState(0)
  const d = useApi<WafData>(`/api/v1/hosts/${host}/waf${site ? '?site=' + site : ''}`, [host, site])
  useEvents(['waf', 'change.done'], () => void d.reload())
  const bx = useBuiltin()
  const manage = can(user, 'sites.manage')
  // switch one CRS rule off for one site (false positives)
  const exclude = async (siteID: number, rule: number) => {
    const cur = (await get<{ site: Site }>(`/api/v1/hosts/${host}/sites/${siteID}`)).site
    const waf = cur.options.waf
    if (!waf) return
    run('site.update', { ...cur, options: { ...cur.options, waf: { ...waf, exclude_rules: [...new Set([...(waf.exclude_rules ?? []), rule])] } } }, { host })
  }
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const on = d.data.sites.filter((x) => x.mode)
  return (
    <>
      {bx.data && !bx.data.waf.installed && <NeedsExtension id="waf" />}
      <Notice>{on.length === 0 ? t('network.waf_none') : t('network.waf_on', { list: on.map((x) => x.domain + '（' + t('sites.waf_' + x.mode) + '）').join('、') })}{' '}{t(d.data.ban ? 'network.waf_ban_on' : 'network.waf_ban_off')}</Notice>
      <Box title={t('network.waf_events')} count={d.data.items.length} right={
        <Select className="h-7 text-[12.5px]" value={String(site)} onChange={(e) => setSite(Number(e.target.value))}>
          <option value="0">{t('network.all_sites')}</option>
          {d.data.sites.map((x) => <option key={x.id} value={x.id}>{x.domain}</option>)}
        </Select>}>
        {d.data.items.length === 0 && <Empty title={t('network.no_waf')}>{t('network.no_waf_hint')}</Empty>}
        {d.data.items.map((e) => (
          <Row key={e.id}>
            <Dot state={e.blocked ? 'bad' : 'warn'} />
            <div className="min-w-0 flex-1 space-y-0.5">
              <div className="text-[13px]"><b className="num">{e.ip}</b> <span className="text-mute">→ {e.domain || '?'}</span> <span className="num break-all text-mute">{e.uri}</span></div>
              {e.rules.map((r) => (
                <div key={r.id} className="flex flex-wrap items-center gap-2 text-[12px] text-mute">
                  <span className="num">{r.id}</span><span>{r.msg}</span>
                  {manage && e.site > 0 && <button className="text-acc hover:underline" onClick={() => void exclude(e.site, r.id)}>{t('network.waf_exclude')}</button>}
                </div>
              ))}
            </div>
            <Pill tone={e.blocked ? 'bad' : 'warn'}>{e.blocked ? t('network.waf_blocked') : t('network.waf_logged')}</Pill>
            <span className="num w-28 shrink-0 text-right text-[12px] text-mute">{datetime(e.ts)}</span>
          </Row>
        ))}
      </Box>
    </>
  )
}

interface LoginRec { id: number; ts: number; username: string; ok: boolean; method: string; ip: string; country?: string; device: string; reason?: string; flags?: string[] }
interface Sess { id: string; username: string; ip: string; country?: string; device: string; method: string; created_at: number; seen_at: number; current: boolean }

function Access() {
  const t = useT()
  const tr = (key: string, raw: string) => { const v = t(key); return v === key ? raw : v }
  const { user } = useAuth()
  const [who, setWho] = useState('')
  const [failed, setFailed] = useState(false)
  const logins = useApi<{ items: LoginRec[] }>(can(user, 'audit.view') ? `/api/v1/access/logins?user=${encodeURIComponent(who)}&failed=${failed ? 1 : 0}` : null, [who, failed])
  const sess = useApi<{ items: Sess[] }>(can(user, 'users.manage') ? '/api/v1/access/sessions' : null)
  const [err, setErr] = useState('')
  const kick = async (s: Sess) => {
    setErr('')
    try { await del(`/api/v1/access/sessions/${s.id}`); void sess.reload() } catch (e) { setErr((e as Error).message) }
  }
  return (
    <>
      {err && <Notice tone="bad">{err}</Notice>}
      {sess.data && <Box title={t('network.sessions')} count={sess.data.items.length}>
        {sess.data.items.map((s) => (
          <Row key={s.id}>
            <Dot state={Date.now() / 1000 - s.seen_at < 300 ? 'ok' : 'off'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{s.username} <span className="font-normal text-mute">· {s.device}</span></div>
              <div className="num text-[12px] text-mute">{s.ip}{s.country ? ' · ' + s.country : ''} · {t('network.signed_in', { at: datetime(s.created_at) })} · {t('network.last_seen', { ago: ago(s.seen_at, t) })}</div>
            </div>
            {s.current ? <Pill tone="acc">{t('network.this_session')}</Pill> : <Button size="sm" variant="ghost" onClick={() => void kick(s)}>{t('network.kick')}</Button>}
          </Row>
        ))}
      </Box>}
      {logins.data && <Box title={t('network.logins')} count={logins.data.items.length} right={
        <div className="flex items-center gap-2">
          <Input className="h-7 w-32 text-[12.5px]" placeholder={t('network.filter_user')} value={who} onChange={(e) => setWho(e.target.value.trim())} />
          <span className="whitespace-nowrap"><Switch checked={failed} onChange={setFailed} label={t('network.failed_only')} /></span>
        </div>}>
        {logins.data.items.length === 0 && <Empty title={t('network.no_logins')} />}
        {logins.data.items.map((l) => (
          <Row key={l.id}>
            <Dot state={l.ok ? (l.flags?.length ? 'warn' : 'ok') : l.reason === 'second_step' ? 'off' : 'bad'} />
            <div className="min-w-0 flex-1">
              <div className="text-[13px]"><b>{l.username || '—'}</b> <span className="text-mute">· {tr('network.method_' + l.method, l.method)} · {l.device}</span></div>
              <div className="num text-[12px] text-mute">{l.ip}{l.country ? ' · ' + l.country : ''}{!l.ok && l.reason && l.reason !== 'second_step' ? ' · ' + tr('network.reason_' + l.reason, l.reason) : ''}</div>
            </div>
            {(l.flags ?? []).map((f) => <Pill key={f} tone="warn">{t('network.flag_' + f)}</Pill>)}
            {!l.ok && (l.reason === 'second_step' ? <Pill>{t('network.first_step')}</Pill> : <Pill tone="bad">{t('network.login_failed')}</Pill>)}
            <span className="num w-28 shrink-0 text-right text-[12px] text-mute">{datetime(l.ts)}</span>
          </Row>
        ))}
      </Box>}
    </>
  )
}

interface TrafficLimit { container: string; up_kbit?: number; down_kbit?: number; quota_gb?: number; quota_action?: string; quota_kbit?: number; state?: string; used_bytes: number }
interface TrafficData {
  containers: { name: string; rx: number; tx: number }[]
  trend: { t: number; rx: number; tx: number }[]
  bucket: number
  sites: { site: string; bytes: number }[]
  limits: TrafficLimit[]
  limit_errors?: Record<string, string>
  tools?: { tc: boolean; acct: boolean; dnf: boolean }
}
interface Live { acct: boolean; total: number; inbound: { proto: string; port: number; to_ip?: string; conns: number; peers: number; bytes: number }[]; peers: { ip: string; conns: number; bytes: number; ports: number[] }[] }

const kbit = (k?: number) => (!k ? '—' : k >= 1000 ? `${+(k / 1000).toFixed(1)} Mbit/s` : `${k} kbit/s`)

function Bars({ data, bucket }: { data: TrafficData['trend']; bucket: number }) {
  const max = Math.max(1, ...data.map((p) => p.rx + p.tx))
  const w = 600
  const h = 90
  const bw = data.length ? w / data.length : w
  const fmt = (ts: number) => new Date(ts * 1000).toLocaleString([], bucket >= 86400 ? { month: '2-digit', day: '2-digit' } : { month: '2-digit', day: '2-digit', hour: '2-digit' })
  return (
    <div className="relative px-4 py-3">
      <svg width="100%" height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className="block" role="img">
        {data.map((p, i) => {
          const hr = (p.rx / max) * (h - 2)
          const ht = (p.tx / max) * (h - 2)
          return (
            <g key={p.t}>
              <title>{fmt(p.t)} · ↓ {bytes(p.rx)} · ↑ {bytes(p.tx)}</title>
              <rect x={i * bw + 0.5} y={h - hr} width={Math.max(bw - 1, 1)} height={hr} fill="var(--acc)" opacity=".75" />
              <rect x={i * bw + 0.5} y={h - hr - ht} width={Math.max(bw - 1, 1)} height={ht} fill="var(--warn)" opacity=".75" />
            </g>
          )
        })}
      </svg>
      <span className="num absolute right-4 top-2 text-[10.5px] text-mute">{bytes(max)}</span>
    </div>
  )
}

function Traffic() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'network.manage')
  const [range, setRange] = useState('24h')
  const d = useApi<TrafficData>(`/api/v1/hosts/${host}/traffic?range=${range}`, [host, range])
  const live = useApi<Live>(`/api/v1/hosts/${host}/traffic/live`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [edit, setEdit] = useState<TrafficLimit | null>(null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const tools = d.data.tools
  const total = d.data.trend.reduce((a, p) => ({ rx: a.rx + p.rx, tx: a.tx + p.tx }), { rx: 0, tx: 0 })
  const limitOf = (name: string) => d.data!.limits.find((l) => l.container === name)
  return (
    <>
      {tools && (!tools.tc || !tools.acct) && (
        <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span>{t('network.traffic_tools')}</span>
          {manage && tools.dnf && <Button size="sm" variant="primary" onClick={() => run('traffic.tools.install', {}, { host, onDone: () => { void d.reload(); void live.reload() } })}>{t('action.traffic.tools.install')}</Button>}</div></Notice>
      )}
      <ObjectFlows />
      <Box title={t('network.traffic_trend')} right={
        <div className="flex items-center gap-2">
          <Segmented value={range} options={[{ value: '24h', label: '24h' }, { value: '7d', label: '7d' }, { value: '30d', label: '30d' }, { value: 'month', label: t('network.this_month') }]} onChange={setRange} />
          <a className="text-[12.5px] text-acc hover:underline" href={`/api/v1/hosts/${host}/traffic.csv?range=${range}`}>CSV</a>
        </div>}>
        <div className="flex gap-4 px-4 pt-3 text-[12.5px]"><span><i className="mr-1 inline-block size-2 rounded-sm bg-acc" />{t('network.down')} {bytes(total.rx)}</span><span><i className="mr-1 inline-block size-2 rounded-sm bg-warn" />{t('network.up')} {bytes(total.tx)}</span></div>
        {d.data.trend.length === 0 ? <Empty title={t('network.no_traffic')}>{t('network.no_traffic_hint')}</Empty> : <Bars data={d.data.trend} bucket={d.data.bucket} />}
      </Box>
      <Box title={t('network.traffic_containers')} count={d.data.containers.length}>
        {d.data.containers.map((c) => {
          const l = limitOf(c.name)
          return (
            <Row key={c.name}>
              <div className="min-w-0 flex-1">
                <div className="text-[13.5px] font-semibold">{c.name}</div>
                <div className="num text-[12px] text-mute">↓ {bytes(c.rx)} · ↑ {bytes(c.tx)}{l ? ' · ' + t('network.limit_summary', { down: kbit(l.down_kbit), up: kbit(l.up_kbit) }) : ''}
                  {l?.quota_gb ? ' · ' + t('network.quota_used', { used: bytes(l.used_bytes), gb: l.quota_gb }) : ''}</div>
                {d.data!.limit_errors?.[c.name] && <div className="text-[12px] text-bad">{d.data!.limit_errors[c.name]}</div>}
              </div>
              {l?.state && l.state !== 'ok' && <Pill tone={l.state === 'warned' ? 'warn' : 'bad'}>{t('network.qstate_' + l.state)}</Pill>}
              {manage && <Button size="sm" variant="ghost" onClick={() => setEdit(l ?? { container: c.name, used_bytes: 0 })}>{l ? t('network.edit_limit') : t('network.add_limit')}</Button>}
            </Row>
          )
        })}
        {d.data.containers.length === 0 && <Empty title={t('network.no_containers')} />}
      </Box>
      {d.data.sites.length > 0 && <Box title={t('network.traffic_sites')} count={d.data.sites.length}>
        {d.data.sites.map((x) => <Row key={x.site}><span className="min-w-0 flex-1 truncate text-[13px]">{x.site}</span><span className="num text-[12.5px] text-mute">{bytes(x.bytes)}</span></Row>)}
      </Box>}
      {live.data && <Box title={t('network.live_inbound', { n: live.data.total })} right={<Button size="sm" variant="ghost" onClick={() => void live.reload()}>{t('network.refresh')}</Button>}>
        {!live.data.acct && <div className="px-4 pt-3 text-[12px] text-mute">{t('network.no_acct')}</div>}
        <div className="grid gap-0 sm:grid-cols-2">
          <div>
            <h4 className="px-4 pt-3 text-[12.5px] font-semibold">{t('network.by_port')}</h4>
            {live.data.inbound.slice(0, 20).map((p) => (
              <Row key={p.proto + p.port}><span className="num w-24 text-[13px] font-semibold">{p.port}/{p.proto}</span>
                <span className="min-w-0 flex-1 text-[12px] text-mute">{t('network.conns_peers', { c: p.conns, p: p.peers })}{p.to_ip ? ' → ' + p.to_ip : ''}</span>
                {live.data!.acct && <span className="num text-[12px] text-mute">{bytes(p.bytes)}</span>}</Row>
            ))}
          </div>
          <div>
            <h4 className="px-4 pt-3 text-[12.5px] font-semibold">{t('network.by_peer')}</h4>
            {live.data.peers.slice(0, 20).map((p) => (
              <Row key={p.ip}><span className="num w-36 truncate text-[13px] font-semibold">{p.ip}</span>
                <span className="min-w-0 flex-1 truncate text-[12px] text-mute">{t('network.conns_n', { c: p.conns })} · {p.ports.join(', ')}</span>
                {live.data!.acct && <span className="num text-[12px] text-mute">{bytes(p.bytes)}</span>}</Row>
            ))}
          </div>
        </div>
      </Box>}
      {edit && <LimitDialog l={edit} tc={!!tools?.tc} onClose={() => setEdit(null)} />}
    </>
  )
}

function LimitDialog({ l, tc, onClose }: { l: TrafficLimit; tc: boolean; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const exists = !!(l.up_kbit || l.down_kbit || l.quota_gb)
  const [v, setV] = useState({ container: l.container, down: (l.down_kbit ?? 0) / 1000, up: (l.up_kbit ?? 0) / 1000, quota: l.quota_gb ?? 0, action: l.quota_action || 'limit', qmbit: (l.quota_kbit ?? 1000) / 1000 })
  const params = { container: v.container, down_kbit: Math.round(v.down * 1000), up_kbit: Math.round(v.up * 1000), quota_gb: v.quota, quota_action: v.quota ? v.action : '', quota_kbit: v.quota && v.action === 'limit' ? Math.round(v.qmbit * 1000) : 0 }
  const num = (k: keyof typeof v) => (e: React.ChangeEvent<HTMLInputElement>) => setV({ ...v, [k]: Number(e.target.value) || 0 })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('network.limit_title', { name: l.container })}
      footer={<>
        {exists && <Button variant="ghost" onClick={() => run('traffic.limit.delete', { container: l.container }, { host, onDone: onClose })}>{t('action.traffic.limit.delete')}</Button>}
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={() => run('traffic.limit.set', params, { host, onDone: onClose })}>{t('action.traffic.limit.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        {!tc && <Notice tone="warn">{t('network.need_tc')}</Notice>}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('network.down_limit')} hint={t('network.zero_unlimited')}><Input type="number" min={0} step="0.1" value={v.down || ''} placeholder="0" onChange={num('down')} /></Field>
          <Field label={t('network.up_limit')} hint={t('network.zero_unlimited')}><Input type="number" min={0} step="0.1" value={v.up || ''} placeholder="0" onChange={num('up')} /></Field>
        </div>
        <Field label={t('network.quota')} hint={t('network.quota_hint')}><Input type="number" min={0} className="w-32" value={v.quota || ''} placeholder="0" onChange={num('quota')} /></Field>
        {v.quota > 0 && <Field label={t('network.quota_then')}>
          <div className="flex items-center gap-2">
            <Segmented value={v.action} options={[{ value: 'limit', label: t('network.quota_limit') }, { value: 'stop', label: t('network.quota_stop') }]} onChange={(x) => setV({ ...v, action: x })} />
            {v.action === 'limit' && <><Input type="number" min={0.1} step="0.1" className="w-24" value={v.qmbit} onChange={num('qmbit')} /><span className="text-[12.5px] text-mute">Mbit/s</span></>}
          </div>
        </Field>}
      </div>
    </Dialog>
  )
}

interface CFZone { id: string; name: string; account_id?: string }
interface CFAccount { id: string; name: string; zones?: CFZone[]; features?: Record<string, boolean>; levels?: Record<string, string>; error?: string }
interface CFData {
  accounts: CFAccount[]
  tunnel: { integration: string; tunnel_id: string; name: string; created_at: number } | null
  routes: { site_id: number; domains: string[] }[]
  access: { domain: string; integration: string; emails?: string[]; email_domains?: string[]; created_at: number }[]
  ban_integration: string
  sync_auto_bans: boolean
}
const FEATURES = ['dns', 'tunnel', 'access', 'firewall', 'settings', 'analytics'] as const

export function CloudflareTab({ section }: { section?: 'tunnel' | 'zones' | 'access' | 'protect' } = {}) {
  const show = (x: string) => !section || section === x
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'network.manage')
  const d = useApi<CFData>(`/api/v1/hosts/${host}/cloudflare`, [host])
  const sites = useApi<{ items: Site[] }>(`/api/v1/hosts/${host}/sites`, [host])
  useEvents(['change.done'], () => { void d.reload(); void sites.reload() })
  const [acc, setAcc] = useState('')
  const [stats, setStats] = useState<{ integ: string; zone: CFZone } | null>(null)
  const [access, setAccess] = useState(false)
  const [tok, setTok] = useState<{ id: string; name: string } | 'new' | null>(null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const accounts = d.data.accounts
  if (accounts.length === 0) return manage ? <CloudflareWelcome onDone={() => void d.reload()} /> : <Empty title={t('cf.no_accounts')}>{t('cf.no_accounts_hint')}</Empty>
  const cur = accounts.find((a) => a.id === acc) ?? accounts[0]
  const routed = new Set(d.data.routes.map((r) => r.site_id))
  const reload = () => void d.reload()
  return (
    <>
      <Box title={t('cf.accounts')} count={accounts.length} right={<span className="flex items-center gap-1.5">
        {accounts.length > 1 && <Select className="h-7 text-[12.5px]" value={cur.id} onChange={(e) => setAcc(e.target.value)}>{accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</Select>}
        {manage && <Button size="sm" variant="ghost" onClick={() => setTok({ id: cur.id, name: cur.name })}>{t('cfw.replace')}</Button>}
        {manage && <Button size="sm" variant="ghost" onClick={() => setTok('new')}>{t('cfw.add')}</Button>}
      </span>}>
        {cur.error ? <div className="px-4 py-3"><Notice tone="bad">{t(cur.error)}</Notice></div> : (
          <div className="flex flex-wrap gap-2 px-4 py-3">
            {FEATURES.map((f) => <Pill key={f} tone={cur.features?.[f] ? 'ok' : 'bad'}>{(cur.features?.[f] ? '✓ ' : '✗ ') + t('cf.f_' + f)}</Pill>)}
            {FEATURES.some((f) => !cur.features?.[f]) && <p className="w-full text-[12px] text-mute">{t('cf.missing_hint')}</p>}
          </div>
        )}
      </Box>

      {tok && <TokenDialog existing={tok === 'new' ? undefined : tok} newId={'cf' + (accounts.length + 1)} onClose={() => setTok(null)} onDone={reload} />}
      {show('tunnel') && <Box title={t('cf.tunnel')} right={manage && (d.data.tunnel
        ? <Button size="sm" variant="ghost" onClick={() => run('cf.tunnel.delete', {}, { host, onDone: reload })}>{t('action.cf.tunnel.delete')}</Button>
        : cur.features?.tunnel && <Button size="sm" variant="primary" onClick={() => run('cf.tunnel.create', { integration: cur.id }, { host, onDone: reload })}>{t('action.cf.tunnel.create')}</Button>)}>
        {!d.data.tunnel ? <Empty title={t('cf.no_tunnel')}>{t('cf.tunnel_hint')}</Empty> : <>
          <div className="num px-4 pt-3 text-[12px] text-mute">{d.data.tunnel.name} · {d.data.tunnel.tunnel_id}</div>
          <div className="px-4 pb-1 pt-2 text-[12px] text-mute">{t('cf.tunnel_tls_hint')}</div>
          {(sites.data?.items ?? []).map((s) => (
            <Row key={s.id}>
              <span className="min-w-0 flex-1 truncate text-[13px]" title={s.domains.join('\n')}>{domainsShort(s)}{s.options.note && <span className="text-mute"> · {s.options.note}</span>}</span>
              {manage ? <Switch checked={routed.has(s.id)} onChange={(on) => run('cf.tunnel.route', { site_id: s.id, on }, { host, onDone: reload })} label={t('cf.via_tunnel')} />
                : routed.has(s.id) && <Pill tone="acc">{t('cf.via_tunnel')}</Pill>}
            </Row>
          ))}
        </>}
      </Box>}

      {show('zones') && <Box title={t('cf.zones')} count={cur.zones?.length}>
        {(cur.zones ?? []).map((z) => {
          const lvl = cur.levels?.[z.id]
          const attack = lvl === 'under_attack'
          return (
            <Row key={z.id}>
              <div className="min-w-0 flex-1"><div className="text-[13.5px] font-semibold">{z.name}</div>
                {lvl && <div className="text-[12px] text-mute">{t('cf.level', { level: lvl })}</div>}</div>
              {attack && <Pill tone="bad">{t('cf.under_attack')}</Pill>}
              {cur.features?.analytics && <Button size="sm" variant="ghost" onClick={() => setStats({ integ: cur.id, zone: z })}>{t('cf.stats')}</Button>}
              {manage && <Button size="sm" variant="ghost" onClick={() => run('cf.purge', { integration: cur.id, zone: z.id, zone_name: z.name }, { host })}>{t('action.cf.purge')}</Button>}
              {manage && cur.features?.settings && <Button size="sm" variant={attack ? 'primary' : 'ghost'} onClick={() => run('cf.attack_mode', { integration: cur.id, zone: z.id, zone_name: z.name, level: attack ? 'medium' : 'under_attack' }, { host, onDone: reload })}>{attack ? t('cf.attack_off') : t('cf.attack_on')}</Button>}
            </Row>
          )
        })}
      </Box>}

      {show('protect') && <Box title={t('cf.protection')}>
        <div className="space-y-3 px-4 py-3 text-[13px]">
          <div className="flex flex-wrap items-center gap-2">
            <span>{t('cf.ban_account')}</span>
            <Select className="h-7 w-48 text-[12.5px]" disabled={!manage} value={d.data.ban_integration}
              onChange={(e) => run('settings.update', { values: { 'cloudflare.ban_integration': e.target.value } }, { onDone: reload })}>
              <option value="">{t('cf.ban_off')}</option>{accounts.filter((a) => a.features?.firewall).map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </Select>
            {d.data.ban_integration && <Switch checked={d.data.sync_auto_bans} onChange={(on) => run('settings.update', { values: { 'cloudflare.sync_auto_bans': on ? '1' : '0' } }, { onDone: reload })} label={t('cf.sync_auto')} />}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span>{t('cf.origin_only')}</span>
            {manage && <Button size="sm" onClick={() => run('firewall.rule.set', { action: 'deny', proto: 'tcp', ports: '80,443', source: 'cloudflare', except: true, comment: t('cf.origin_only_comment'), enabled: true }, { host })}>{t('cf.origin_only_btn')}</Button>}
          </div>
        </div>
      </Box>}

      {show('access') && <Box title={t('cf.access')} count={d.data.access.length} right={manage && cur.features?.access && <Button size="sm" onClick={() => setAccess(true)}>{t('action.cf.access.set')}</Button>}>
        {d.data.access.length === 0 && <Empty title={t('cf.no_access')}>{t('cf.access_hint')}</Empty>}
        {d.data.access.map((x) => (
          <Row key={x.domain}>
            <div className="min-w-0 flex-1"><div className="text-[13.5px] font-semibold">{x.domain}</div>
              <div className="truncate text-[12px] text-mute">{[...(x.emails ?? []), ...(x.email_domains ?? []).map((e) => '@' + e)].join(', ')}</div></div>
            {manage && <Button size="sm" variant="ghost" onClick={() => run('cf.access.delete', { domain: x.domain }, { host, onDone: reload })}>{t('action.cf.access.delete')}</Button>}
          </Row>
        ))}
      </Box>}
      {access && <AccessDialog integ={cur.id} onClose={() => { setAccess(false); reload() }} />}
      {stats && <ZoneStats integ={stats.integ} zone={stats.zone} onClose={() => setStats(null)} />}
    </>
  )
}

function AccessDialog({ integ, onClose }: { integ: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [domain, setDomain] = useState('')
  const [who, setWho] = useState('')
  const parts = who.split(/[\s,]+/).filter(Boolean)
  const params = { integration: integ, domain, emails: parts.filter((p) => p.includes('@') && !p.startsWith('@')), email_domains: parts.filter((p) => p.startsWith('@')).map((p) => p.slice(1)) }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.cf.access.set')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!domain || parts.length === 0} onClick={() => run('cf.access.set', params, { host, onDone: onClose })}>{t('action.cf.access.set')}</Button></>}>
      <div className="space-y-3 px-4 py-3">
        <Notice>{t('cf.access_how')}</Notice>
        <Field label={t('cf.access_domain')}><Input className="num" value={domain} placeholder="admin.example.com" onChange={(e) => setDomain(e.target.value.trim().toLowerCase())} /></Field>
        <Field label={t('cf.access_who')} hint={t('cf.access_who_hint')}><Textarea rows={3} className="num" value={who} placeholder={'me@example.com\n@example.com'} onChange={(e) => setWho(e.target.value)} /></Field>
      </div>
    </Dialog>
  )
}

function ZoneStats({ integ, zone, onClose }: { integ: string; zone: CFZone; onClose: () => void }) {
  const t = useT()
  const d = useApi<{ days: { date: string; requests: number; cached: number; bytes: number; threats: number; uniques: number }[] }>(`/api/v1/integrations/${integ}/zones/${zone.id}/analytics?days=7`)
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('cf.stats_title', { zone: zone.name })} footer={<Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>}>
      <div className="px-4 py-3">
        {d.error && <Notice tone="bad">{d.error.message}</Notice>}
        {d.data && <table className="num w-full text-[12.5px]">
          <thead className="text-mute"><tr><th className="text-left font-normal">{t('cf.s_date')}</th><th className="text-right font-normal">{t('cf.s_requests')}</th><th className="text-right font-normal">{t('cf.s_cached')}</th><th className="text-right font-normal">{t('cf.s_bytes')}</th><th className="text-right font-normal">{t('cf.s_uniques')}</th><th className="text-right font-normal">{t('cf.s_threats')}</th></tr></thead>
          <tbody>{d.data.days.map((x) => (
            <tr key={x.date} className="border-t border-line"><td className="py-1">{x.date}</td><td className="text-right">{x.requests}</td><td className="text-right">{x.requests ? Math.round((x.cached / x.requests) * 100) : 0}%</td><td className="text-right">{bytes(x.bytes)}</td><td className="text-right">{x.uniques}</td><td className="text-right">{x.threats}</td></tr>
          ))}</tbody>
        </table>}
      </div>
    </Dialog>
  )
}

interface WharfLinkInfo { url: string; name: string; fp: string; scopes: string[]; target_ips: string[]; alerts_primary: string; paired_at: number; last_seen?: number; state: string; mode?: string; peer_lan?: string; peer_public?: string; protocol?: number; key_auth?: boolean; wharf_key_auth?: boolean; dns_name?: string }
export interface WharfStatus { paired: boolean; berth_fp: string; link?: WharfLinkInfo; live?: LinkLive; user_map?: { wharf_user: string; berth_user: string }[] }
interface WharfView {
  paired: boolean
  published?: { site_id: number; mode: string; wan_port?: string; target_ip: string }[]
  devices?: { name: string; ip: string; mac?: string; online: boolean }[]
  ddns?: { id: string; domain: string; owner: string; ip?: string; status?: string }[]
  traffic?: Record<string, unknown>
  sni?: { domains: string[] }
  sync_auto_bans?: boolean
}

export function WharfPair({ berthFP, bare }: { berthFP: string; bare?: boolean }) {
  const t = useT()
  const { user } = useAuth()
  // a Wharf found by LAN discovery comes in as ?url=…: filled in and checked at once
  const [u, setU] = useState(() => new URLSearchParams(location.search).get('url') ?? '')
  // LAN or internet: which pair of names the link uses (found by LAN discovery: the LAN)
  const [mode, setMode] = useState<'lan' | 'public'>(() => new URLSearchParams(location.search).get('mode') === 'public' ? 'public' : 'lan')
  const where = useApi<{ public_name: string; lan_name: string }>('/api/v1/access/where')
  const [chain, setChain] = useState<{ subject: string; issuer: string; ca: boolean; fp: string; public?: boolean }[] | null>(null)
  const [fp, setFp] = useState('')
  const [err, setErr] = useState('')
  const full = /^https?:\/\//.test(u) ? u : 'https://' + u
  useEffect(() => { if (new URLSearchParams(location.search).get('url')) void probe() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const probe = async () => {
    setErr(''); setChain(null)
    try {
      const r = await post<{ chain: { subject: string; issuer: string; ca: boolean; fp: string; public?: boolean }[] }>('/api/v1/wharf/probe', { url: full })
      setChain(r.chain)
      setFp((r.chain[0]?.public ? r.chain[0] : r.chain.find((c) => c.ca) ?? r.chain[r.chain.length - 1]).fp) // a public chain: any of it, the name is what gets checked
    } catch (e) { setErr(errText(e)) }
  }
  const go = async () => {
    try { const r = await post<{ redirect: string }>('/api/v1/wharf/pair/start', { url: full, fp, mode }); location.href = r.redirect } catch (e) { setErr(errText(e)) }
  }
  if (user?.role !== 'super') return <Notice>{t('wharf.super_only')}</Notice>
  const body = (
      <div className={bare ? 'space-y-3 text-[13px]' : 'space-y-3 px-4 py-3 text-[13px]'}>
        {!bare && <p className="text-mute">{t('wharf.pair_how')}</p>}
        {err && <Notice tone="bad">{err}</Notice>}
        <div className="space-y-1.5" data-link-mode={mode}>
          <div className="font-semibold">{t('linkmode.ask')}</div>
          <Segmented value={mode} onChange={(m) => { setMode(m); setChain(null) }} options={[{ value: 'lan', label: t('linkmode.lan') }, { value: 'public', label: t('linkmode.public') }]} />
          <p className="text-[12px] text-mute">{t(mode === 'lan' ? 'linkmode.lan_d' : 'linkmode.public_d', { lan: where.data?.lan_name || 'mini.lan', pub: where.data?.public_name || '—' })}</p>
          {mode === 'public' && where.data && !where.data.public_name && <Notice tone="warn">{t('wharf.no_public_name')}</Notice>}
        </div>
        <div className="flex gap-2"><Input className="num flex-1" value={u} onChange={(e) => setU(e.target.value.trim())} placeholder={mode === 'lan' ? 'https://wharf.lan:7080' : 'https://wharf.example.com'} /><Button disabled={!u} onClick={probe}>{t('wharf.check_cert')}</Button></div>
        {chain && <>
          {chain[0]?.public
            ? <>
              {/* a public name: checked by the name, nothing to compare by hand */}
              <Notice tone="acc">{t('wharf.public_cert', { issuer: chain[0].issuer })}</Notice>
              <p className="text-[12.5px] text-mute">{t('wharf.public_fp_note')}</p>
              <details className="text-[12px]">
                <summary className="cursor-pointer text-mute">{t('wharf.chain_details')}</summary>
                <div className="mt-1 space-y-1">{chain.map((c) => <div key={c.fp} className="rounded border border-line p-2"><div>{c.subject || '—'} {c.ca && <Pill tone="acc">CA</Pill>} <span className="text-mute">← {c.issuer}</span></div><div className="num break-all text-[11px] text-mute">{c.fp}</div></div>)}</div>
              </details>
            </>
            : <>
              <p>{t('wharf.pick_fp')}</p>
              {chain.map((c) => (
                <label key={c.fp} className="flex cursor-pointer items-start gap-2 rounded-[var(--radius-box)] border border-line p-2">
                  <input type="radio" checked={fp === c.fp} onChange={() => setFp(c.fp)} className="mt-1" />
                  <div className="min-w-0"><div>{c.subject || '—'} {c.ca && <Pill tone="acc">CA</Pill>}</div><div className="num break-all text-[11.5px] text-mute">{c.fp}</div></div>
                </label>
              ))}
            </>}
          <p className="text-[12px] text-mute">{t('wharf.our_fp')} <span className="num break-all">{berthFP}</span></p>
          <Button variant="primary" disabled={!fp} onClick={go}>{t('wharf.go_approve')}</Button>
        </>}
      </div>
  )
  return bare ? body : <Box title={t('wharf.pair_title')}>{body}</Box>
}

export function WharfPaired({ st, reload }: { st: WharfStatus; reload: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const l = st.link!
  const v = useApi<WharfView>(`/api/v1/hosts/${host}/wharf`, [host])
  const sites = useApi<{ items: Site[] }>(`/api/v1/hosts/${host}/sites`, [host])
  useEvents(['change.done'], () => { void v.reload(); void sites.reload() })
  const [err, setErr] = useState('')
  const [unpair, setUnpair] = useState(false)
  const [mapW, setMapW] = useState('')
  const [mapB, setMapB] = useState('')
  const manage = can(user, 'network.manage')
  const openWharf = async () => {
    setErr('')
    try { const r = await post<{ url: string }>('/api/v1/wharf/sso', { return_to: '/' }); window.open(r.url, '_blank', 'noopener') } catch (e) { setErr(errText(e)) }
  }
  const pubOf = (id: number) => v.data?.published?.find((p) => p.site_id === id)
  return (
    <>
      {err && <Notice tone="bad">{err}</Notice>}
      {l.state === 'broken' && <Notice tone="bad">{t('wharf.broken')}</Notice>}
      <Box title={<span className="inline-flex items-center gap-2">{l.name}<ConnBadge live={st.live} /></span>} right={<div className="flex gap-2">
        {l.scopes.includes('sso') && <Button size="sm" variant="primary" onClick={openWharf}>{t('wharf.open')}</Button>}
        {user?.role === 'super' && <Button size="sm" variant="ghost" onClick={() => setUnpair(true)}>{t('action.wharf.unpair')}</Button>}
      </div>}>
        <div className="space-y-2 px-4 py-3 text-[12.5px]">
          <div className="num text-mute">{l.url} · {t('wharf.paired_at', { at: datetime(l.paired_at) })}{l.last_seen ? ' · ' + t('network.last_seen', { ago: ago(l.last_seen, t) }) : ''}</div>
          <div className="flex flex-wrap gap-1">{l.scopes.map((s) => <Pill key={s}>{t('wharf.scope_' + s.replace('.', '_'))}</Pill>)}</div>
          <div className="flex flex-wrap items-center gap-3 pt-1">
            <span>{t('wharf.primary')}</span>
            <div className="w-60"><Select className="h-7 text-[12.5px]" disabled={!manage} value={l.alerts_primary} onChange={(e) => run('wharf.alerts_primary', { primary: e.target.value }, { onDone: reload })}>
              <option value="">{t('wharf.primary_both')}</option><option value="wharf">{t('wharf.primary_wharf')}</option><option value="berth">{t('wharf.primary_berth')}</option>
            </Select></div>
            {l.scopes.includes('bans') && <Switch checked={!!v.data?.sync_auto_bans} onChange={(on) => run('settings.update', { values: { 'wharf.sync_auto_bans': on ? '1' : '0' } }, { onDone: () => void v.reload() })} label={t('wharf.sync_auto')} />}
          </div>
        </div>
      </Box>

      {(l.scopes.includes('forwards') || l.scopes.includes('sni')) && <Box title={t('wharf.publish')} count={sites.data?.items.length}>
        <div className="px-4 pt-3 text-[12px] text-mute">{t('wharf.publish_hint', { ip: l.target_ips[0] ?? '?' })}</div>
        {(sites.data?.items ?? []).map((s) => <PublishRow key={s.id} site={s} pub={pubOf(s.id)} scopes={l.scopes} />)}
      </Box>}

      {v.data?.ddns && <Box title={t('wharf.ddns')} count={v.data.ddns.length}>
        {v.data.ddns.map((d) => (
          <Row key={d.id}>
            <div className="min-w-0 flex-1"><div className="text-[13.5px] font-semibold">{d.domain}</div><div className="num text-[12px] text-mute">{d.ip ?? ''} {d.status ?? ''}</div></div>
            <Pill tone={d.owner === 'berth' ? 'acc' : undefined}>{t('wharf.owner_' + d.owner)}</Pill>
            {manage && <Button size="sm" variant="ghost" onClick={() => run('wharf.ddns.claim', { domain: d.domain, owner: d.owner === 'wharf' ? 'berth' : 'wharf' }, { host })}>{d.owner === 'wharf' ? t('wharf.take_ddns') : t('wharf.give_ddns')}</Button>}
          </Row>
        ))}
      </Box>}

      {v.data?.traffic && <Box title={t('wharf.router_view')}>
        <div className="grid gap-2 px-4 py-3 text-[12.5px] sm:grid-cols-3">
          {'wan_in_bytes' in v.data.traffic && <div>{t('wharf.wan_in')} <b className="num">{bytes(Number(v.data.traffic.wan_in_bytes))}</b></div>}
          {'wan_out_bytes' in v.data.traffic && <div>{t('wharf.wan_out')} <b className="num">{bytes(Number(v.data.traffic.wan_out_bytes))}</b></div>}
          {'scans' in v.data.traffic && <div>{t('wharf.scans')} <b className="num">{String(v.data.traffic.scans)}</b></div>}
          {Array.isArray(v.data.traffic.countries) && <div className="sm:col-span-3 text-mute">{(v.data.traffic.countries as { cc: string; conns: number }[]).map((c) => `${c.cc} ${c.conns}`).join(' · ')}</div>}
        </div>
      </Box>}

      {v.data?.devices && <Box title={t('wharf.devices')} count={v.data.devices.length}>
        {v.data.devices.map((d) => (
          <Row key={d.ip + (d.mac ?? '')}><Dot state={d.online ? 'ok' : 'off'} /><span className="min-w-0 flex-1 truncate text-[13px]">{d.name}</span><span className="num text-[12px] text-mute">{d.ip}{d.mac ? ' · ' + d.mac : ''}</span>
            {l.target_ips.includes(d.ip) && <Pill tone="acc">{t('wharf.this_host')}</Pill>}</Row>
        ))}
      </Box>}

      {l.scopes.includes('sso') && can(user, 'users.manage') && <Box title={t('wharf.user_map')} count={st.user_map?.length}>
        <div className="px-4 pt-3 text-[12px] text-mute">{t('wharf.user_map_hint')}</div>
        {(st.user_map ?? []).map((m) => (
          <Row key={m.wharf_user}><span className="flex-1 text-[13px]">Wharf <b>{m.wharf_user}</b> → Berth <b>{m.berth_user}</b></span>
            <Button size="sm" variant="ghost" onClick={async () => { await put('/api/v1/wharf/users', { wharf_user: m.wharf_user, berth_user: '' }); reload() }}>{t('common.delete')}</Button></Row>
        ))}
        <div className="flex gap-2 px-4 py-3">
          <Input className="w-40" placeholder={t('wharf.wharf_user')} value={mapW} onChange={(e) => setMapW(e.target.value)} />
          <Input className="w-40" placeholder={t('wharf.berth_user')} value={mapB} onChange={(e) => setMapB(e.target.value)} />
          <Button size="sm" disabled={!mapW || !mapB} onClick={async () => { try { await put('/api/v1/wharf/users', { wharf_user: mapW, berth_user: mapB }); setMapW(''); setMapB(''); reload() } catch (e) { setErr(errText(e)) } }}>{t('wharf.add_map')}</Button>
        </div>
      </Box>}

      {unpair && <UnpairDialog name={l.name} onClose={() => setUnpair(false)} onDone={reload} />}
    </>
  )
}

function PublishRow({ site, pub, scopes }: { site: Site; pub?: { mode: string; wan_port?: string }; scopes: string[] }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [mode, setMode] = useState(scopes.includes('forwards') ? 'forward' : 'sni')
  const [port, setPort] = useState('443')
  return (
    <Row>
      <span className="min-w-0 flex-1 truncate text-[13px] font-semibold" title={site.domains.join('\n')}>{domainsShort(site)}{site.options.note && <span className="font-normal text-mute"> · {site.options.note}</span>}</span>
      {pub ? <>
        <Pill tone="acc">{pub.mode === 'sni' ? t('wharf.via_sni') : t('wharf.via_forward', { port: pub.wan_port ?? '' })}</Pill>
        <Button size="sm" variant="ghost" onClick={() => run('wharf.unpublish', { site_id: site.id }, { host })}>{t('action.wharf.unpublish')}</Button>
      </> : <>
        <div className="w-32 shrink-0"><Select className="h-7 text-[12.5px]" value={mode} onChange={(e) => setMode(e.target.value)}>
          {scopes.includes('forwards') && <option value="forward">{t('wharf.mode_forward')}</option>}
          {scopes.includes('sni') && <option value="sni">{t('wharf.mode_sni')}</option>}
        </Select></div>
        {mode === 'forward' && <div className="w-20 shrink-0"><Input className="num h-7 text-[12.5px]" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} aria-label={t('wharf.wan_port')} /></div>}
        <Button size="sm" onClick={() => run('wharf.publish', { site_id: site.id, mode, wan_port: mode === 'forward' ? port : undefined }, { host })}>{t('action.wharf.publish')}</Button>
      </>}
    </Row>
  )
}

function UnpairDialog({ name, onClose, onDone }: { name: string; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const [keep, setKeep] = useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('action.wharf.unpair')}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="danger" onClick={() => run('wharf.unpair', { keep }, { onDone: () => { onClose(); onDone() } })}>{t('action.wharf.unpair')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p>{t('wharf.unpair_how', { name })}</p>
        <Switch checked={keep} onChange={setKeep} label={t('wharf.unpair_keep')} />
      </div>
    </Dialog>
  )
}

// who talks to the outside the most: containers and VMs, both directions; click for detail
function ObjectFlows() {
  const t = useT()
  const { host } = useHost()
  const [open, setOpen] = useState<string | null>(null)
  const d = useApi<{ objects: { obj: string; dir: 'in' | 'out'; conns: number; bytes: number }[] }>(`/api/v1/hosts/${host}/flows?range=24h`, [host])
  const by = new Map<string, { in: number; out: number; conns: number }>()
  for (const o of d.data?.objects ?? []) {
    const e = by.get(o.obj) ?? { in: 0, out: 0, conns: 0 }
    e[o.dir] += o.bytes
    e.conns += o.conns
    by.set(o.obj, e)
  }
  const list = [...by.entries()].sort((a, b) => b[1].in + b[1].out - (a[1].in + a[1].out)).slice(0, 12)
  const top = Math.max(...list.map(([, e]) => Math.max(e.in, e.out)), 1)
  return (
    <Box title={t('flows.objects_title')} right={<span>{t('flows.objects_hint')}</span>}>
      {list.length === 0 && <Empty title={t('flows.objects_none')}>{t('flows.objects_none_hint')}</Empty>}
      {list.map(([obj, e]) => {
        const [kind, name] = obj.split(':', 2)
        return (
          <Row key={obj} onClick={() => setOpen(obj)}>
            <div className="w-48 min-w-0 truncate text-[13px]"><span className="text-mute">{t('flows.kind_' + kind)}</span> <b className="font-semibold">{name}</b></div>
            <div className="min-w-0 flex-1 space-y-1">
              {(['out', 'in'] as const).map((dir) => (
                <div key={dir} className="grid grid-cols-[2.5rem_1fr_5rem] items-center gap-2 text-[11.5px]">
                  <span className="text-mute">{t('flows.short_' + dir)}</span>
                  <div className="h-1.5 overflow-hidden rounded-full bg-soft"><div className="h-full rounded-full bg-acc" style={{ width: Math.max(e[dir] ? 1 : 0, (e[dir] / top) * 100) + '%' }} /></div>
                  <span className="num text-right">{bytes(e[dir])}</span>
                </div>
              ))}
            </div>
          </Row>
        )
      })}
      {open && <Drawer open onOpenChange={(o) => !o && setOpen(null)} title={`${t('flows.kind_' + open.split(':')[0])} ${open.split(':').slice(1).join(':')}`}><FlowsView obj={open} /></Drawer>}
    </Box>
  )
}
