import { useEffect, useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Field, Input, Select } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { get, post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { usePick, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { ReauthProvider, useReauth } from '../account/reauth'
import { NewSiteDialog } from '../sites/NewSiteDialog'
import type { DiagLayer } from '../sites/types'
import { TabBar } from '@/components/ui/Tabs'

type L10n = Record<string, string>
interface FieldDef { key: string; type: string; default?: string; options?: string[]; required?: boolean; generate?: boolean; label: L10n; hint?: L10n }
interface CatalogApp {
  id: string; name: L10n; summary: L10n; category: string; version: string; website?: string; arch: string[]
  verified: { arm64_16k: 'ok' | 'crash' | 'untested'; at?: string; note?: string }
  mem_mb: number; disk_mb: number; site?: { service: string; port: number }; ports: { name: string; port: number; field: string; http?: boolean; public?: boolean; proto?: string }[]; fields: FieldDef[]; installed: number
}
interface Instance {
  name: string; app_id: string; version: string; latest?: string; values: Record<string, string>; runtime?: string; installed_at: number
  site?: { container: string; port: number }; access?: string; sites?: { id: number; domain: string; enabled: boolean }[]
}
interface Check { code: string; message: string; block: boolean }

const letterColor = (id: string) => ['bg-acc', 'bg-acc-2', 'bg-warn', 'bg-ok'][id.length % 4]

function AppIcon({ app }: { app: CatalogApp }) {
  return <span className={cx('grid size-9 shrink-0 place-items-center rounded-[var(--radius-box)] text-[15px] font-semibold text-white', letterColor(app.id))} aria-hidden>{app.name.en.slice(0, 1)}</span>
}

function Verified({ v }: { v: CatalogApp['verified'] }) {
  const t = useT()
  if (v.arm64_16k === 'ok') return <Pill tone="ok">{t('store.v_ok')}</Pill>
  if (v.arm64_16k === "crash") return <span title={v.note}><Pill tone="warn">{t("store.v_crash")}</Pill></span>
  return <Pill>{t('store.v_untested')}</Pill>
}

export default function StorePage() {
  return <ReauthProvider><Store /></ReauthProvider>
}

function Store() {
  const t = useT()
  const pick = usePick()
  const { host } = useHost()
  const tab = (useParams()['*'] ?? '').split('/')[0]
  const cat = useApi<{ items: CatalogApp[]; categories: string[] }>('/api/v1/store/apps')
  const [q, setQ] = useState('')
  const [category, setCategory] = useState('')
  const [open, setOpen] = useState<CatalogApp | null>(null)
  const [siteFor, setSiteFor] = useState<{ container: string; port: number } | null>(null)
  const items = useMemo(() => (cat.data?.items ?? []).filter((a) => (!category || a.category === category) &&
    (!q || (pick(a.name) + ' ' + pick(a.summary) + ' ' + a.id).toLowerCase().includes(q.toLowerCase()))), [cat.data, category, q, pick])
  return (
    <div className="space-y-3">
      <TabBar label={t('nav.store')} value={tab} items={['', 'installed'].map((x) => ({ value: x, label: t('store.tab_' + (x || 'browse')), to: hostPath(host, 'store' + (x ? '/' + x : '')) }))} />
      {tab === '' && <>
        <div className="flex flex-wrap items-center gap-2">
          <Input className="w-60" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('store.search')} aria-label={t('store.search')} />
          <div className="flex flex-wrap gap-1">
            {['', ...(cat.data?.categories ?? [])].map((c) => (
              <button key={c} onClick={() => setCategory(c)} className={cx('rounded-full border px-2.5 py-0.5 text-[12.5px]', category === c ? 'border-acc bg-tint' : 'border-line text-mute hover:text-fg')}>{t('store.cat_' + (c || 'all'))}</button>
            ))}
          </div>
        </div>
        {cat.error && <Notice tone="bad">{cat.error.message}</Notice>}
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {items.map((a) => (
            <button key={a.id} onClick={() => setOpen(a)} className="flex gap-3 rounded-[var(--radius-box)] border border-line p-3 text-left hover:border-acc">
              <AppIcon app={a} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5"><b className="truncate text-[13.5px]">{pick(a.name)}</b>{a.installed > 0 && <Pill tone="acc">{t('store.installed_n', { n: a.installed })}</Pill>}</div>
                <p className="line-clamp-2 text-[12.5px] text-mute">{pick(a.summary)}</p>
                <div className="mt-1 flex flex-wrap gap-1"><Verified v={a.verified} /><span className="text-[11.5px] text-mute">{a.mem_mb} MB</span></div>
              </div>
            </button>
          ))}
        </div>
        {items.length === 0 && cat.data && <Empty title={t('store.none')} />}
        <p className="text-[12px] text-mute">{t('store.note')}</p>
      </>}
      {tab === 'installed' && <Installed catalog={cat.data?.items ?? []} onSite={setSiteFor} />}
      {open && <InstallDialog app={open} onClose={() => setOpen(null)} onSite={setSiteFor} />}
      {siteFor && <NewSiteDialog preset={{ type: 'container', container: siteFor.container, port: siteFor.port }} onClose={() => setSiteFor(null)} onDone={() => setSiteFor(null)} />}
    </div>
  )
}

function InstallDialog({ app, onClose, onSite }: { app: CatalogApp; onClose: () => void; onSite: (s: { container: string; port: number }) => void }) {
  const t = useT()
  const pick = usePick()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const [name, setName] = useState(app.id)
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(app.fields.map((f) => [f.key, f.default ?? ''])))
  const [checks, setChecks] = useState<Check[]>([])
  const [done, setDone] = useState<Change | null>(null)
  // who reaches it: only this server, the LAN (IP:port), or everyone through a site
  const web = app.ports.find((p) => p.http)
  const [access, setAccess] = useState<'local' | 'lan' | 'public'>('local')
  const [domain, setDomain] = useState('')
  const [dnsCheck, setDnsCheck] = useState<DiagLayer | null>(null)
  const addr = useApi<{ ipv4?: string; lan?: string }>(web ? `/api/v1/hosts/${host}/public-ip` : null, [host])
  useEffect(() => {
    setDnsCheck(null)
    if (access !== 'public' || !/\./.test(domain)) return
    const id = setTimeout(() => {
      get<DiagLayer>(`/api/v1/hosts/${host}/dns-check?name=${encodeURIComponent(domain)}`).then(setDnsCheck).catch(() => {})
    }, 600)
    return () => clearTimeout(id)
  }, [domain, access, host])
  useEffect(() => {
    const id = setTimeout(() => {
      post<{ checks: Check[] }>(`/api/v1/hosts/${host}/store/check`, { app: app.id, name, values: vals }).then((r) => setChecks(r.checks)).catch(() => setChecks([]))
    }, 400)
    return () => clearTimeout(id)
  }, [vals, name, host, app.id])
  const blocked = checks.some((c) => c.block)
  const site = done?.result?.site as { container: string; port: number } | undefined
  const port = web ? vals[web.field] : ''
  const openUrl = done && (access === 'public' && done.result?.site_created ? `https://${domain}` : access === 'lan' && addr.data?.lan ? `http://${addr.data.lan}:${port}` : '')
  const params = { app: app.id, name, values: vals, access, ...(access === 'public' ? { site: { domains: [domain.trim().toLowerCase()], tls: { mode: 'auto' } } } : {}) }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={<span className="flex items-center gap-2"><AppIcon app={app} />{pick(app.name)} <span className="text-[12.5px] font-normal text-mute">{app.version}</span></span>}
      description={pick(app.summary)}
      footer={done ? <>
        <span className="text-[12.5px] text-mute">{done.result?.healthy === false ? t('app.unhealthy') : done.result?.site_error ? t('store.site_failed') : t('store.installed_ok')}</span>
        <Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>
        {openUrl && <a href={openUrl} target="_blank" rel="noreferrer"><Button variant="primary">{t('store.open_url', { url: openUrl.replace(/^https?:\/\//, '') })}</Button></a>}
        {site && access === 'local' && <Button variant="primary" onClick={() => { onSite(site); onClose() }}>{t('store.make_site')}</Button>}
      </> : <>
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={blocked || !can(user, 'store.install') || (access === 'public' && !/\./.test(domain))}
          onClick={() => run('app.install', params, { host, onDone: (c) => setDone(c) })}>{access === 'public' ? t('store.install_and_site') : t('action.app.install')}</Button>
      </>}>
      <div className="space-y-3 px-4 py-3 text-[13.5px]">
        <div className="flex flex-wrap gap-2 text-[12.5px] text-mute">
          <Verified v={app.verified} /><span>{t('store.needs', { mem: app.mem_mb, disk: app.disk_mb })}</span><span>· {app.arch.join(' / ')}</span>
          {app.website && <a className="text-acc hover:underline" href={app.website} target="_blank" rel="noreferrer">{t('store.website')}</a>}
        </div>
        {app.verified.arm64_16k === 'crash' && <Notice tone="warn">{t('store.crash_hint')}</Notice>}
        {checks.length > 0 && <div className="space-y-1">{checks.map((c, i) => <Notice key={i} tone={c.block ? 'bad' : 'warn'}>{c.message}</Notice>)}</div>}
        <Field label={t('store.instance')} hint={t('store.instance_hint')}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div className="grid gap-2 sm:grid-cols-2">
          {app.fields.map((f) => (
            <Field key={f.key} label={pick(f.label) + (f.required ? ' *' : '')} hint={f.hint ? pick(f.hint) : undefined}>
              {f.type === 'select' ? (
                <Select value={vals[f.key]} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })}>{f.options!.map((o) => <option key={o}>{o}</option>)}</Select>
              ) : (
                <Input type={f.type === 'password' ? 'password' : f.type === 'number' || f.type === 'port' ? 'number' : 'text'} autoComplete="off" value={vals[f.key]}
                  placeholder={f.type === 'password' && f.generate ? t('store.generated') : ''} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })} />
              )}
            </Field>
          ))}
        </div>
        {web && (
          <div>
            <div className="mb-1.5 font-semibold">{t('store.access')}</div>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              {(['local', 'lan', 'public'] as const).filter((m) => m !== 'public' || app.site?.service).map((m) => (
                <button key={m} type="button" onClick={() => setAccess(m)} className={cx('rounded-[var(--radius-box)] border p-2.5 text-left', access === m ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                  <div className="font-semibold">{t('store.access_' + m)}</div>
                  <div className="num text-[11.5px] text-mute">{m === 'local' ? `127.0.0.1:${port}` : m === 'lan' ? `http://${addr.data?.lan ?? t('store.server_ip')}:${port}` : t('store.access_public_hint')}</div>
                </button>
              ))}
            </div>
            {access === 'lan' && <p className="mt-1.5 text-[12px] text-mute">{addr.data?.ipv4 && addr.data.ipv4 === addr.data.lan ? <span className="text-warn">{t('store.lan_is_public', { ip: addr.data.ipv4 })}</span> : t('store.lan_hint')}</p>}
            {access === 'public' && (
              <div className="mt-2">
                <Field label={t('store.domain')} hint={addr.data?.ipv4 ? t('store.domain_hint', { ip: addr.data.ipv4 }) : undefined}>
                  <Input className="num" value={domain} placeholder={`${name}.example.com`} onChange={(e) => setDomain(e.target.value.trim())} />
                </Field>
                {dnsCheck && <p className={cx('mt-1 text-[12.5px]', dnsCheck.status === 'ok' ? 'text-ok' : dnsCheck.status === 'warn' ? 'text-warn' : 'text-bad')}>{dnsCheck.status === 'ok' ? '✓ ' : '! '}{dnsCheck.message}</p>}
                <p className="mt-1 text-[12px] text-mute">{t('store.public_hint')}</p>
              </div>
            )}
          </div>
        )}
        <p className="text-[12px] text-mute">{t('store.data_note', { dir: '/srv/berth/apps/' + name })}</p>
      </div>
    </Dialog>
  )
}

function Installed({ catalog, onSite }: { catalog: CatalogApp[]; onSite: (s: { container: string; port: number }) => void }) {
  const t = useT()
  const pick = usePick()
  const run = useChange()
  const reauth = useReauth()
  const { host } = useHost()
  const list = useApi<{ items: Instance[] }>(`/api/v1/hosts/${host}/apps`, [host])
  const [secrets, setSecrets] = useState<{ name: string; values: Record<string, string> } | null>(null)
  const [guide, setGuide] = useState<string | null>(null)
  useEvents(['change.done'], () => void list.reload())
  const byId = Object.fromEntries(catalog.map((a) => [a.id, a]))
  const showSecrets = async (name: string) => {
    const r = await reauth(() => post<{ values: Record<string, string> }>(`/api/v1/hosts/${host}/apps/${name}/secrets`))
    setSecrets({ name, values: r.values })
  }
  return (
    <Box title={t('store.tab_installed')} count={list.data?.items.length}>
      {list.data?.items.length === 0 && <Empty title={t('store.none_installed')} />}
      {list.data?.items.map((x) => {
        const app = byId[x.app_id]
        const web = app?.ports.find((p) => p.http)
        return (
          <Row key={x.name}>
            {app && <AppIcon app={app} />}
            <div className="min-w-0 flex-1">
              <div className="text-[13.5px] font-semibold">{x.name} <span className="font-normal text-mute">· {app ? pick(app.name) : x.app_id} {x.version}</span></div>
              <div className="text-[12px] text-mute">{t('store.installed_at', { ago: ago(x.installed_at, t) })}{web ? ` · ${x.access === 'lan' ? '0.0.0.0' : '127.0.0.1'}:${x.values[web.field]}` : ''}{x.runtime === 'krun' ? ' · ' + t('compat.mode_krun') : ''}</div>
              {(x.sites?.length ?? 0) > 0 && <div className="flex flex-wrap gap-x-3 text-[12px]">{x.sites!.map((st) => (
                <a key={st.id} href={`https://${st.domain}`} target="_blank" rel="noreferrer" className={cx('num hover:underline', st.enabled ? 'text-acc' : 'text-mute line-through')}>🌐 {st.domain}</a>))}</div>}
            </div>
            {x.latest && <Button size="sm" variant="primary" onClick={() => run('app.upgrade', { name: x.name }, { host })}>{t('store.upgrade_to', { v: x.latest })}</Button>}
            {app?.ports.some((p) => p.public) && <Button size="sm" variant="primary" onClick={() => setGuide(x.name)}>{t('store.guide')}</Button>}
            {x.site && !(x.sites?.length) && <Button size="sm" onClick={() => onSite(x.site!)}>{t('store.make_site')}</Button>}
            <Button size="sm" variant="ghost" onClick={() => void showSecrets(x.name)}>{t('store.show_secrets')}</Button>
            <Button size="sm" variant="ghost" onClick={() => run('app.remove', { name: x.name }, { host, confirmName: x.name })}>{t('action.app.remove')}</Button>
          </Row>
        )
      })}
      {guide && <GuideDialog name={guide} onClose={() => setGuide(null)} />}
      {secrets && <Dialog open onOpenChange={(o) => !o && setSecrets(null)} title={t('store.secrets_title', { name: secrets.name })} footer={<Button className="ml-auto" onClick={() => setSecrets(null)}>{t('common.close')}</Button>}>
        <div className="num space-y-1 px-4 py-3 text-[12.5px]">{Object.entries(secrets.values).map(([k, v]) => <div key={k} className="flex gap-2"><span className="w-40 shrink-0 text-mute">{k}</span><span className="break-all select-all">{v}</span></div>)}</div>
      </Dialog>}
    </Box>
  )
}

interface Guide {
  app_id: string; ports: { name: string; proto: string; port: number; public: boolean }[]; addresses: string[]; public_ip?: string
  wharf?: { name: string; can_forward: boolean }; published?: { wan_port: string; proto: string; target_ip: string }[] | null
  key?: string; devices?: { id: string; ip?: string; created: string }[]
}

// After installing an app other devices connect to: what to type into them, and the ports.
function GuideDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const g = useApi<Guide>(`/api/v1/hosts/${host}/apps/${name}/guide`, [host, name])
  useEvents(['change.done'], () => void g.reload())
  const d = g.data
  const kv = (k: string, v: React.ReactNode) => <div className="flex gap-3 py-1"><span className="w-28 shrink-0 text-mute">{k}</span><span className="num min-w-0 flex-1 break-all select-all">{v}</span></div>
  const port = (n: string) => d?.ports.find((p) => p.name === n)?.port
  const withPort = (addr: string, p: number | undefined, def: number) => (p && p !== def ? `${addr}:${p}` : addr)
  const rustdesk = d?.app_id === 'rustdesk-server'
  const where = [...(d?.addresses ?? []).map((a) => [t('store.guide_lan'), a]), ...(d?.public_ip ? [[t('store.guide_wan'), d.public_ip]] : [])]
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={t('store.guide_title', { name })} footer={<Button className="ml-auto" onClick={onClose}>{t('common.close')}</Button>}>
      <div className="space-y-4 px-4 py-3 text-[13px]">
        {!d && <div className="text-mute">{g.error ? g.error.message : t('common.loading')}</div>}
        {d && rustdesk && <section>
          <div className="mb-1 font-semibold">{t('store.rd_client')}</div>
          <div className="mb-2 text-[12px] text-mute">{t('store.rd_client_hint')}</div>
          {where.map(([label, addr]) => <div key={addr} className="mb-2 rounded-[var(--radius-box)] border border-line px-3 py-1.5">
            <div className="text-[12px] font-semibold">{label}</div>
            {kv(t('store.rd_id_server'), withPort(addr, port('id'), 21116))}
            {kv(t('store.rd_relay'), withPort(addr, port('relay'), 21117))}
          </div>)}
          {kv('Key', d.key || t('store.rd_key_pending'))}
        </section>}
        {d && rustdesk && <section>
          <div className="mb-1 font-semibold">{t('store.rd_devices', { n: d.devices?.length ?? 0 })}</div>
          {(d.devices?.length ?? 0) === 0 ? <div className="text-[12px] text-mute">{t('store.rd_devices_none')}</div>
            : <div className="max-h-48 overflow-auto">{d.devices!.map((x) => <div key={x.id} className="num flex gap-3 py-0.5 text-[12.5px]"><span className="w-32">{x.id}</span><span className="text-mute">{x.ip ?? ''}</span><span className="ml-auto text-mute">{x.created}</span></div>)}</div>}
        </section>}
        {d && <section>
          <div className="mb-1 font-semibold">{t('store.guide_ports')}</div>
          <div className="num text-[12.5px]">{d.ports.filter((p) => p.public).map((p) => `${p.port}/${p.proto}`).join(' · ')}</div>
          <div className="mt-1 text-[12px] text-mute">{t('store.guide_ports_hint')}</div>
        </section>}
        {d?.wharf?.can_forward && <section>
          <div className="mb-1 font-semibold">{t('store.guide_wharf', { name: d.wharf.name })}</div>
          {(d.published?.length ?? 0) > 0
            ? <div className="flex flex-wrap items-center gap-2"><span className="num text-[12.5px]">{d.published!.map((f) => `${f.wan_port}/${f.proto} → ${f.target_ip}`).join(' · ')}</span>
              <Button size="sm" className="ml-auto" onClick={() => run('app.unpublish', { name }, { host })}>{t('action.app.unpublish')}</Button></div>
            : <div className="flex flex-wrap items-center gap-2"><span className="flex-1 text-[12px] text-mute">{t('store.guide_wharf_hint')}</span>
              <Button size="sm" variant="primary" onClick={() => run('app.publish', { name }, { host })}>{t('action.app.publish')}</Button></div>}
        </section>}
      </div>
    </Dialog>
  )
}
