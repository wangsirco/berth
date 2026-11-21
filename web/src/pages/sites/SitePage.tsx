import { ArrowLeft, BarChart3, ExternalLink, FileText, FolderOpen, Globe, Link2, Lock, Network, Rocket, ScrollText, Shield, Stethoscope, Target, Boxes, Code2, LayoutDashboard } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ObjectActivity } from '@/components/ai/Activity'
import { useChange } from '@/components/change/ChangeDialog'
import { DomainDNS } from '@/components/dns/DomainDNS'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Input, Select } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { DiagnoseView } from './DiagnoseView'
import { registrable } from './SiteList'
import { NewSiteDialog } from './NewSiteDialog'
import { PhpSite } from './PhpSite'
import { SiteLog, SiteProtect } from './SiteExtras'
import { StatsView } from './StatsView'
import { siteName, targetText, type Site } from './types'

interface Detail { site: Site; relations: { from: { type: string; id: string }; to: { type: string; id: string; name?: string }; kind: string }[] }
type Sec = 'overview' | 'domains' | 'target' | 'https' | 'protect' | 'stats' | 'log' | 'diag' | 'rel' | 'php'

const SECS: [Sec, typeof Globe][] = [
  ['overview', LayoutDashboard], ['domains', Globe], ['target', Target], ['https', Lock], ['php', Code2], ['protect', Shield],
  ['stats', BarChart3], ['log', ScrollText], ['diag', Stethoscope], ['rel', Network],
]

// siteDir: where a static or PHP site keeps its files.
export const siteDir = (s: Site) => '/srv/berth/caddy/sites/' + s.domains[0] + (s.target.root_sub ? '/' + s.target.root_sub : '')

// jumpTo: the page that runs what the site points at (a project, a container, its folder).
export function jumpTo(host: string, s: Site): { to: string; label: string; Icon: typeof Globe } | null {
  switch (s.target.type) {
    case 'project': return { to: hostPath(host, 'projects'), label: 'sitep.jump_project', Icon: Rocket }
    case 'container': return { to: hostPath(host, 'containers/c/' + encodeURIComponent(s.target.container ?? '')), label: 'sitep.jump_container', Icon: Boxes }
    case 'static': case 'php': return { to: hostPath(host, 'files' + siteDir(s)), label: 'sitep.jump_files', Icon: FolderOpen }
  }
  return null
}

export const siteURL = (s: Site, d = s.domains[0]) => (s.tls.mode === 'off' ? 'http://' : 'https://') + d.replace('*.', 'www.')

// SitePage: one site's settings on a page of its own, sections on the left.
export function SitePage({ id, onChanged }: { id: number; onChanged: () => void }) {
  const t = useT()
  const nav = useNavigate()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const d = useApi<Detail>(`/api/v1/hosts/${host}/sites/${id}`, [id])
  const cf = useApi<{ routes: { site_id: number }[]; tunnel?: { name: string } }>(`/api/v1/hosts/${host}/cloudflare`, [host])
  useEvents(['change.done'], () => { void d.reload(); void cf.reload() })
  const [sec, setSec] = useState<Sec>(() => (new URLSearchParams(location.search).get('sec') as Sec) || 'overview')
  const [edit, setEdit] = useState(false)
  const s = d.data?.site
  const manage = can(user, 'sites.manage')
  const done = () => { void d.reload(); onChanged() }
  if (d.error) return <Notice tone="bad">{d.error.message}</Notice>
  if (!s) return <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const viaTunnel = !!cf.data?.routes.some((r) => r.site_id === id)
  const hasDNS = d.data?.relations.some((r) => r.kind === 'publishes')
  const jump = jumpTo(host, s)
  const secs = SECS.filter(([k]) => k !== 'php' || s.target.type === 'php')
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => nav(hostPath(host, 'sites'))}><ArrowLeft className="size-3.5" />{t('nav.sites')}</Button>
        <h2 className="min-w-0 truncate text-[17px] font-semibold">{siteName(s)}</h2>
        {s.options.note && <span className="num truncate text-[12.5px] text-mute">{s.domains[0]}</span>}
        <Pill tone={s.enabled ? 'ok' : undefined}>{s.enabled ? t('sites.enabled') : t('sites.disabled')}</Pill>
        {viaTunnel && <Pill tone="acc">{t('cf.via_tunnel')}</Pill>}
        <span className="ml-auto flex flex-wrap gap-1.5">
          <a href={siteURL(s)} target="_blank" rel="noreferrer"><Button size="sm"><ExternalLink className="size-3.5" />{t('sitep.open')}</Button></a>
          {jump && <Link to={jump.to}><Button size="sm"><jump.Icon className="size-3.5" />{t(jump.label)}</Button></Link>}
          {manage && <Button size="sm" onClick={() => run('site.toggle', { id, enabled: !s.enabled }, { host, onDone: done })}>{s.enabled ? t('sites.disable') : t('sites.enable')}</Button>}
          {manage && <Button size="sm" variant="danger" onClick={() => run('site.delete', { id, with_dns: hasDNS }, { host, onDone: () => { onChanged(); nav(hostPath(host, 'sites')) }, confirmName: s.domains[0] })}>{t('action.site.delete')}</Button>}
        </span>
      </div>
      <ObjectActivity type="site" id={id} />
      <div className="grid gap-4 md:grid-cols-[190px_1fr]">
        <nav aria-label={t('sitep.sections')} className="flex gap-1 overflow-x-auto md:sticky md:top-4 md:flex-col md:self-start">
          {secs.map(([k, Icon]) => (
            <button key={k} type="button" aria-current={sec === k ? 'page' : undefined} onClick={() => setSec(k)}
              className={cx('flex shrink-0 items-center gap-2 rounded-[var(--radius-box)] px-3 py-2 text-left text-[13px]', sec === k ? 'bg-tint font-semibold text-acc' : 'hover:bg-soft')}>
              <Icon className="size-4 shrink-0" aria-hidden /><span>{t('sitep.s_' + k)}</span>
            </button>
          ))}
        </nav>
        <div className="min-w-0 space-y-4">
          {sec === 'overview' && <Overview s={s} host={host} viaTunnel={viaTunnel} manage={manage} go={setSec} onEdit={() => setEdit(true)} />}
          {sec === 'domains' && <DomainsEditor s={s} host={host} manage={manage} onDone={done} />}
          {sec === 'target' && <Box title={t('sitep.s_target')} right={manage && <Button size="sm" onClick={() => setEdit(true)}>{t('sites.edit')}</Button>}>
            <KV k={t('sites.target')} v={<span className="num">{t('sites.target_' + s.target.type)} · {targetText(s)}{s.target.scheme ? ` (${s.target.scheme})` : ''}</span>} />
            {jump && <KV k={t('sitep.where')} v={<Link className="text-acc hover:underline" to={jump.to}>{t(jump.label)}</Link>} />}
            {(s.target.type === 'static' || s.target.type === 'php') && <KV k={t('sitep.dir')} v={<span className="num">{siteDir(s)}</span>} />}
            <p className="px-4 py-3 text-[12.5px] text-mute">{t('sitep.target_hint')}</p>
          </Box>}
          {sec === 'https' && <Box title={t('sitep.s_https')} right={manage && <Button size="sm" onClick={() => setEdit(true)}>{t('sites.edit')}</Button>}>
            <KV k={t('sites.tls')} v={t('sites.tls_' + s.tls.mode) + (s.tls.dns ? ` · ${s.tls.dns}` : '') + (s.tls.cert_id ? ` · #${s.tls.cert_id}` : '')} />
            <KV k="HTTP → HTTPS" v={s.tls.mode !== 'off' && !s.options.no_https_redirect ? '✓' : '—'} />
            <KV k="HSTS" v={s.options.hsts ? '✓' : '—'} />
            {viaTunnel && <p className="px-4 py-3 text-[12.5px] text-mute">{t('sitep.tunnel_tls')}</p>}
          </Box>}
          {sec === 'php' && <Box title="PHP"><PhpSite s={s} /></Box>}
          {sec === 'protect' && <Box title={t('sitep.s_protect')}><SiteProtect s={s} onDone={done} /></Box>}
          {sec === 'stats' && <Box title={t('sitep.s_stats')}><StatsView host={host} siteId={id} /></Box>}
          {sec === 'log' && <Box title={t('sitep.s_log')}><SiteLog id={id} /></Box>}
          {sec === 'diag' && <Box title={t('sitep.s_diag')}><DiagnoseView host={host} siteId={id} /></Box>}
          {sec === 'rel' && <Box title={t('sitep.s_rel')}>
            <div className="p-4 text-[13px]">
              {(d.data?.relations ?? []).length === 0 && <p className="text-mute">{t('containers.no_relations')}</p>}
              <ul className="space-y-1">{d.data?.relations.map((r, i) => <li key={i}><span className="text-mute">{t('rel.' + r.kind)}</span> {t('obj.' + r.to.type)} <b className="num font-medium">{r.to.name || r.to.id}</b></li>)}</ul>
            </div>
          </Box>}
        </div>
      </div>
      {edit && <NewSiteDialog site={s} onClose={() => setEdit(false)} onDone={done} />}
    </div>
  )
}

function KV({ k, v }: { k: string; v: React.ReactNode }) {
  return <div className="grid grid-cols-[130px_1fr] gap-2 border-b border-line px-4 py-2 text-[13px] last:border-b-0"><span className="text-mute">{k}</span><span className="min-w-0 break-words">{v}</span></div>
}

function Overview({ s, host, viaTunnel, manage, go, onEdit }: { s: Site; host: string; viaTunnel: boolean; manage: boolean; go: (s: Sec) => void; onEdit: () => void }) {
  const t = useT()
  const o = s.options
  const redirects = o.redirect?.hosts ?? []
  const flags = [
    s.tls.mode !== 'off' && !o.no_https_redirect && 'HTTP→HTTPS', o.hsts && 'HSTS', o.compress && 'gzip/zstd', o.cache_static && t('sites.opt_cache_static'),
    (o.allow_ips?.length ?? 0) > 0 && t('sites.opt_allow_ips'), o.cors && 'CORS', (o.basic_auth?.length ?? 0) > 0 && t('sites.basic_auth'),
    o.rate_limit && t('sites.rl_pill', { n: o.rate_limit.requests, s: o.rate_limit.window }), o.waf && 'WAF · ' + t('sites.waf_' + o.waf.mode), o.hotlink && t('sitep.hotlink'),
  ].filter(Boolean) as string[]
  return (
    <>
      <Box title={t('sitep.s_overview')} right={manage && <Button size="sm" onClick={onEdit}>{t('sites.edit')}</Button>}>
        <KV k={t('sitep.primary')} v={<a className="text-acc hover:underline" href={siteURL(s)} target="_blank" rel="noreferrer">{s.domains[0]}</a>} />
        {s.domains.length > 1 && <KV k={t('sitep.others')} v={<div className="flex flex-wrap gap-1">{s.domains.slice(1).map((x) => <Pill key={x}>{x}{redirects.includes(x) ? ' → ' + (o.redirect?.code ?? 301) : ''}</Pill>)}</div>} />}
        <KV k={t('sites.target')} v={<span className="num">{t('sites.target_' + s.target.type)} · {targetText(s)}</span>} />
        <KV k={t('sitep.group')} v={<GroupField s={s} host={host} manage={manage} />} />
        <KV k={t('sitep.reach')} v={viaTunnel ? t('sitep.reach_tunnel') : t('sitep.reach_direct')} />
        <KV k={t('sites.tls')} v={t('sites.tls_' + s.tls.mode)} />
        <KV k={t('sites.options')} v={flags.length ? <div className="flex flex-wrap gap-1">{flags.map((f) => <Pill key={f}>{f}</Pill>)}</div> : '—'} />
        <KV k={t('sites.log')} v={<span className="num text-[12px]">/srv/berth/caddy/logs/{s.domains[0]}.log · {t('sites.keep_days', { n: o.log_days ?? 14 })}</span>} />
      </Box>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {([['domains', Link2], ['stats', BarChart3], ['protect', Shield], ['log', FileText], ['diag', Stethoscope], ['https', Lock]] as const).map(([k, Icon]) => (
          <button key={k} type="button" onClick={() => go(k)} className="flex items-start gap-2.5 rounded-[var(--radius-box)] border border-line bg-card p-3 text-left hover:border-acc">
            <Icon className="mt-0.5 size-4 shrink-0 text-acc" aria-hidden />
            <span><span className="block text-[13px] font-semibold">{t('sitep.s_' + k)}</span><span className="block text-[12px] text-mute">{t('sitep.d_' + k)}</span></span>
          </button>
        ))}
      </div>
      <Box title={t('sitep.s_stats')}><StatsView host={host} siteId={s.id} /></Box>
    </>
  )
}

// DomainsEditor: the site's names as rows, one of them primary; the others either serve
// the site too or send visitors on to the primary name (301 / 302, path kept).
function DomainsEditor({ s, host, manage, onDone }: { s: Site; host: string; manage: boolean; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const [doms, setDoms] = useState<string[]>(s.domains)
  const [redir, setRedir] = useState<string[]>(s.options.redirect?.hosts ?? [])
  const [code, setCode] = useState<number>(s.options.redirect?.code ?? 301)
  const [add, setAdd] = useState('')
  useEffect(() => { setDoms(s.domains); setRedir(s.options.redirect?.hosts ?? []); setCode(s.options.redirect?.code ?? 301) }, [s])
  const valid = (d: string) => /^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(d)
  const addOne = () => {
    const n = add.trim().toLowerCase().replace(/^https?:\/\//, '').replace(/\/.*$/, '')
    if (n && valid(n) && !doms.includes(n)) setDoms([...doms, n])
    setAdd('')
  }
  const primary = doms[0]
  // the www / bare twin of the primary name, which most sites want redirected
  const twin = primary && !primary.startsWith('*.') ? (primary.startsWith('www.') ? primary.slice(4) : 'www.' + primary) : ''
  const dirty = JSON.stringify(doms) !== JSON.stringify(s.domains) || JSON.stringify(redir.filter((x) => doms.includes(x) && x !== primary)) !== JSON.stringify(s.options.redirect?.hosts ?? []) || (redir.length > 0 && code !== (s.options.redirect?.code ?? 301))
  const save = () => {
    const hosts = redir.filter((x) => doms.includes(x) && x !== primary)
    run('site.update', { ...s, domains: doms, options: { ...s.options, redirect: hosts.length ? { hosts, code } : undefined } }, { host, onDone })
  }
  const role = (d: string) => d === primary ? 'primary' : redir.includes(d) ? 'redirect' : 'serve'
  const setRole = (d: string, r: string) => {
    if (r === 'primary') { setDoms([d, ...doms.filter((x) => x !== d)]); setRedir(redir.filter((x) => x !== d)); return }
    setRedir(r === 'redirect' ? [...redir.filter((x) => x !== d), d] : redir.filter((x) => x !== d))
  }
  return (
    <Box title={t('sitep.s_domains')} count={doms.length}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p className="text-[12.5px] text-mute">{t('sitep.domains_hint')}</p>
        <div className="overflow-hidden rounded-[var(--radius-box)] border border-line">
          {doms.map((d) => (
            <div key={d} className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2 last:border-b-0">
              <a className="num min-w-0 flex-1 truncate font-medium hover:underline" href={siteURL(s, d)} target="_blank" rel="noreferrer">{d}</a>
              {manage ? (
                <Select className="h-8 w-auto text-[12.5px]" value={role(d)} onChange={(e) => setRole(d, e.target.value)} aria-label={t('sitep.role')}>
                  <option value="primary">{t('sitep.role_primary')}</option>
                  {d !== primary && <option value="serve">{t('sitep.role_serve')}</option>}
                  {d !== primary && <option value="redirect">{t('sitep.role_redirect', { to: primary })}</option>}
                </Select>
              ) : <Pill>{t('sitep.role_' + role(d), { to: primary })}</Pill>}
              {manage && doms.length > 1 && <Button size="sm" variant="ghost" onClick={() => { setDoms(doms.filter((x) => x !== d)); setRedir(redir.filter((x) => x !== d)) }}>{t('common.delete')}</Button>}
            </div>
          ))}
        </div>
        {manage && <div className="flex flex-wrap items-center gap-2">
          <Input className="num w-72" value={add} onChange={(e) => setAdd(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addOne() } }} placeholder="www.example.com" aria-label={t('sitep.add_domain')} />
          <Button size="sm" onClick={addOne} disabled={!valid(add.trim().replace(/^https?:\/\//, '').replace(/\/.*$/, ''))}>{t('sitep.add_domain')}</Button>
          {twin && !doms.includes(twin) && <Button size="sm" variant="ghost" onClick={() => { setDoms([...doms, twin]); setRedir([...redir, twin]) }}>{t('sitep.add_twin', { name: twin, to: primary })}</Button>}
        </div>}
        {manage && redir.some((x) => doms.includes(x) && x !== primary) && (
          <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <span>{t('sitep.redirect_code')}</span>
            <Select className="h-8 w-auto text-[12.5px]" value={String(code)} onChange={(e) => setCode(Number(e.target.value))} aria-label={t('sitep.redirect_code')}>
              <option value="301">{t('sitep.code_301')}</option><option value="302">{t('sitep.code_302')}</option>
            </Select>
          </div>
        )}
        <DomainDNS host={host} names={doms} manage={manage} />
        {manage && <div className="flex items-center gap-2">
          <Button variant="primary" disabled={!dirty || doms.length === 0} onClick={save}>{t('sitep.save_domains')}</Button>
          {dirty && <Button variant="ghost" onClick={() => { setDoms(s.domains); setRedir(s.options.redirect?.hosts ?? []); setCode(s.options.redirect?.code ?? 301) }}>{t('common.cancel')}</Button>}
          {(s.target.type === 'static' || s.target.type === 'php') && doms[0] !== s.domains[0] && <span className="text-[12px] text-warn">{t('sitep.primary_dir_warn', { dir: siteDir(s) })}</span>}
        </div>}
      </div>
    </Box>
  )
}

// GroupField: which group the site list files the site under (its registered domain unless set).
function GroupField({ s, host, manage }: { s: Site; host: string; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const [edit, setEdit] = useState(false)
  const [v, setV] = useState(s.options.group ?? '')
  const auto = registrable(s.domains[0] ?? '')
  if (!edit) return (
    <span className="flex flex-wrap items-center gap-2">
      <span className="num">{s.options.group || auto || '—'}</span>
      {!s.options.group && auto && <span className="text-[12px] text-mute">{t('sitep.group_auto')}</span>}
      {manage && <Button size="sm" variant="ghost" onClick={() => setEdit(true)}>{t('sites.edit')}</Button>}
    </span>
  )
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Input className="w-56" value={v} onChange={(e) => setV(e.target.value)} placeholder={auto || t('sitep.group_ph')} aria-label={t('sitep.group')} autoFocus />
      <Button size="sm" variant="primary" onClick={() => run('site.update', { ...s, options: { ...s.options, group: v.trim() || undefined } }, { host, onDone: () => setEdit(false) })}>{t('common.save')}</Button>
      <Button size="sm" variant="ghost" onClick={() => { setV(s.options.group ?? ''); setEdit(false) }}>{t('common.cancel')}</Button>
      <span className="w-full text-[12px] text-mute">{t('sitep.group_hint')}</span>
    </span>
  )
}
