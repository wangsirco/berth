import { useEffect, useMemo, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Switch } from '@/components/ui/Field'
import { Notice, Pill } from '@/components/ui/Box'
import { splitTags, TagInput } from '@/components/ui/TagInput'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Candidate, CertRow, DiagLayer, Integration, Site, TargetType, TLSMode } from './types'
import { langOf } from './native/lang'
import { NeedsExtension, useBuiltin } from '../extensions/Builtin'

const blank: Site = { id: 0, domains: [], enabled: true, target: { type: 'container' }, tls: { mode: 'auto' }, options: { hsts: true, compress: true } }

const lines = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)

// NewSiteDialog creates a site, or edits one (site given). The change itself is previewed
// by the change dialog (config diff, Caddy validation, equivalent command).
export function NewSiteDialog({ site, preset, domains: presetDomains, onClose, onDone }: { site?: Site; preset?: Partial<Site['target']>; domains?: string[]; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [s, setS] = useState<Site>(() => {
    const init = structuredClone(site ?? blank)
    if (preset) init.target = { ...init.target, ...preset } as Site['target']
    return init
  })
  const [doms, setDoms] = useState<string[]>(site?.domains ?? presetDomains ?? [])
  const [domDraft, setDomDraft] = useState('')
  const domains = useMemo(() => [...doms, ...splitTags(domDraft).filter((x) => !doms.includes(x))], [doms, domDraft])
  const [ups, setUps] = useState<string[]>(site?.target.upstreams ?? [])
  const [upDraft, setUpDraft] = useState('')
  const upstreams = useMemo(() => [...ups, ...splitTags(upDraft).filter((x) => !ups.includes(x))], [ups, upDraft])
  const [dnsCheck, setDnsCheck] = useState<DiagLayer | null>(null)
  const [publish, setPublish] = useState<{ on: boolean; integration: string; zone: string; proxied: boolean }>({ on: false, integration: '', zone: '', proxied: true })
  const [advanced, setAdvanced] = useState(false)
  const cands = useApi<{ items: Candidate[] }>(`/api/v1/hosts/${host}/sites/targets`, [host])
  const natives = useApi<{ items: { name: string; kind?: string; version: string; port?: number }[] }>(`/api/v1/hosts/${host}/native`, [host])
  const integ = useApi<{ items: Integration[] }>('/api/v1/integrations')
  const certs = useApi<{ items: CertRow[] }>('/api/v1/certs')
  const ddns = useApi<{ items: { host: string; name: string; enabled: boolean; last_error?: string }[]; wharf?: { items: { domain: string }[] } }>(!site ? '/api/v1/ddns' : null)
  const ddnsName = ddns.data?.items.find((d) => d.host === host && d.enabled && !d.last_error)?.name ?? ddns.data?.wharf?.items[0]?.domain
  const pub = useApi<{ ipv4?: string }>(!site ? `/api/v1/hosts/${host}/public-ip` : null, [host])
  const cf = (integ.data?.items ?? []).filter((i) => i.kind === 'cloudflare')
  const zones = useApi<{ items: { id: string; name: string }[] }>(publish.on && publish.integration ? `/api/v1/integrations/${publish.integration}/zones` : null, [publish.integration, publish.on])
  const primary = domains[0] ?? ''

  // live DNS check (debounced)
  useEffect(() => {
    setDnsCheck(null)
    if (!/\./.test(primary) || primary.startsWith('*.')) return
    const id = setTimeout(() => {
      get<DiagLayer>(`/api/v1/hosts/${host}/dns-check?name=${encodeURIComponent(primary)}`).then(setDnsCheck).catch(() => {})
    }, 600)
    return () => clearTimeout(id)
  }, [primary, host])

  // a matching zone for the publish option
  useEffect(() => {
    if (!publish.on || !zones.data) return
    const z = zones.data.items.filter((z) => primary === z.name || primary.endsWith('.' + z.name)).sort((a, b) => b.name.length - a.name.length)[0]
    if (z && z.id !== publish.zone) setPublish((p) => ({ ...p, zone: z.id }))
  }, [zones.data, primary, publish.on, publish.zone])

  const set = (f: (x: Site) => void) => setS((cur) => { const n = structuredClone(cur); f(n); return n })
  const tgt = s.target
  const params = useMemo(() => {
    const p: Record<string, unknown> = { ...s, domains }
    if (tgt.type === 'address') p.target = { ...tgt, upstreams }
    if (!site) delete p.id
    if (!site && publish.on && publish.integration && publish.zone && pub.data?.ipv4) {
      p.dns = { integration: publish.integration, zone_id: publish.zone, type: 'A', content: pub.data.ipv4, proxied: publish.proxied }
    }
    return p
  }, [s, domains, upstreams, tgt, site, publish, pub.data])

  const [cms, setCms] = useState<'blank' | 'wordpress' | 'typecho' | 'discuz'>('blank')
  const [cmsDb, setCmsDb] = useState(0)
  const submit = () => run(site ? 'site.update' : 'site.create', params, {
    host, onDone: (c) => {
      onDone(); onClose()
      // a new PHP site: put the chosen app (or a test page) into it right away
      const id = (c.result as { id?: number } | undefined)?.id
      if (!site && tgt.type === 'php' && id) run('site.cms', { id, app: cms, ...(cms !== 'blank' ? { db: cmsDb } : {}) }, { host })
    },
  })
  const isLocal = /\.(lan|local|home\.arpa|internal)$/.test(primary)

  const tlsOptions: { value: TLSMode; title: string; sub: string }[] = [
    { value: 'auto', title: t('sites.tls_auto'), sub: t('sites.tls_auto_sub') },
    { value: 'dns', title: t('sites.tls_dns'), sub: t('sites.tls_dns_sub') },
    { value: 'internal', title: t('sites.tls_internal'), sub: t('sites.tls_internal_sub') },
    { value: 'custom', title: t('sites.tls_custom'), sub: t('sites.tls_custom_sub') },
    { value: 'off', title: t('sites.tls_off'), sub: t('sites.tls_off_sub') },
  ]

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} wide title={site ? t('sites.edit_title', { domain: site.domains[0] }) : t('sites.new_title')}
      description={site ? undefined : t('sites.new_hint')}
      footer={<>
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" onClick={submit} disabled={!primary}>{site ? t('sites.save') : t('sites.create')}</Button>
      </>}>
      <div className="space-y-4 px-4 py-3 text-[13.5px]">
        <Field label={t('sites.domains')} hint={t('sites.domains_hint')}>
          <TagInput value={doms} onChange={setDoms} draft={domDraft} onDraft={setDomDraft} primary primaryLabel={t('sites.primary')} makePrimaryLabel={t('sites.make_primary')}
            invalid={(d) => !/^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/i.test(d)}
            placeholder="shop.example.com" ariaLabel={t('sites.domains')} autoFocus={!site} />
          {dnsCheck && <p className={cx('mt-1 text-[12.5px]', dnsCheck.status === 'ok' ? 'text-ok' : dnsCheck.status === 'warn' ? 'text-warn' : 'text-bad')}>
            {dnsCheck.status === 'ok' ? '✓ ' : '! '}{dnsCheck.message}</p>}
          {dnsCheck && dnsCheck.status !== 'ok' && ddnsName && <p className="mt-1 text-[12.5px] text-mute">{t('sites.cname_to_ddns', { name: primary, ddns: ddnsName })}</p>}
        </Field>

        <Field label={t('sites.note')} hint={t('sites.note_hint')}>
          <Input value={s.options.note ?? ''} maxLength={60} placeholder={t('sites.note_ph')} onChange={(e) => { const v = e.target.value; set((x) => { x.options.note = v }) }} />
        </Field>

        <div>
          <div className="mb-1.5 text-[12.5px] font-semibold">{t('sites.target')}</div>
          <Segmented<TargetType> value={tgt.type} onChange={(v) => set((x) => { x.target = { type: v } })} options={[
            { value: 'project', label: t('sites.target_project') }, { value: 'container', label: t('sites.target_container') }, { value: 'address', label: t('sites.target_address') },
            { value: 'static', label: t('sites.target_static') }, { value: 'php', label: 'PHP' }, { value: 'redirect', label: t('sites.target_redirect') }]} />
          <div className="mt-2">
            {tgt.type === 'project' && (
              <div className="space-y-1 rounded-[var(--radius-box)] border border-line p-1">
                {(natives.data?.items ?? []).length === 0 && <p className="p-2 text-mute">{t('sites.no_projects')}</p>}
                {(natives.data?.items ?? []).map((n) => (
                  <button key={n.name} type="button" onClick={() => set((x) => { x.target = { type: 'project', project: n.name } })}
                    className={cx('flex w-full items-center gap-2 rounded px-2 py-1.5 text-left', tgt.project === n.name ? 'bg-tint' : 'hover:bg-soft')}>
                    <b className="font-semibold">{n.name}</b><span className="text-[12px] text-mute">{langOf(n.kind)} {n.version}{n.port ? '' : ' · ' + t('native.not_deployed')}</span>
                  </button>
                ))}
              </div>
            )}
            {tgt.type === 'container' && (
              <div className="max-h-64 space-y-1 overflow-auto rounded-[var(--radius-box)] border border-line p-1">
                {cands.data?.items.length === 0 && <p className="p-2 text-mute">{t('sites.no_containers')}</p>}
                {cands.data?.items.map((c) => {
                  const used = (c.used_by ?? []).filter((d) => !site?.domains.includes(d))
                  return (
                    <div key={c.container} className={cx('rounded px-2 py-1.5', tgt.container === c.container && 'bg-tint')}>
                      <div className="flex items-baseline gap-2">
                        <b className="font-semibold">{c.container}</b>
                        <span className="truncate text-[12px] text-mute">{c.image}</span>
                        {used.length > 0 && <span className="ml-auto text-[12px] text-warn">{t('sites.used_by', { site: used.join(', ') })}</span>}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-1">
                        {c.ports.map((p) => (
                          <button key={p} type="button" onClick={() => set((x) => { x.target = { type: 'container', container: c.container, port: p } })}
                            className={cx('num rounded border px-1.5 text-[12px]', tgt.container === c.container && tgt.port === p ? 'border-acc bg-acc text-white' : 'border-line hover:border-acc')}>{p}</button>
                        ))}
                        {(c.loopback ?? []).map((p) => <span key={'l' + p} className="num rounded border border-dashed border-line px-1.5 text-[12px] text-mute" title={t('sites.loopback_hint')}>{p} · 127.0.0.1</span>)}
                        {c.ports.length === 0 && <span className="text-[12px] text-mute">{t('sites.no_ports')}</span>}
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
            {tgt.type === 'address' && <Field label={t('sites.upstreams')} hint={t('sites.upstreams_hint')}>
              <TagInput value={ups} onChange={setUps} draft={upDraft} onDraft={setUpDraft} placeholder="127.0.0.1:3000" ariaLabel={t('sites.upstreams')} />
            </Field>}
            {tgt.type === 'static' && <div className="space-y-1">
              <p className="text-[12.5px] text-mute">{t('sites.static_hint', { dir: '/srv/berth/caddy/sites/' + (primary || '<domain>') })}</p>
              <Switch checked={!!tgt.spa} onChange={(v) => set((x) => { x.target.spa = v })} label={t('sites.spa')} />
            </div>}
            {tgt.type === 'php' && <PhpTarget php={tgt.php} native={!!tgt.native} rootSub={tgt.root_sub ?? ''} onRootSub={(v) => set((x) => { x.target.root_sub = v || undefined })} onPhp={(v, n) => set((x) => { x.target.php = v; x.target.native = n || undefined })} app={cms} onApp={setCms} db={cmsDb} onDb={setCmsDb} editing={!!site} />}
            {tgt.type === 'redirect' && <div className="grid gap-2 sm:grid-cols-[1fr_120px]">
              <Input value={tgt.to ?? ''} onChange={(e) => set((x) => { x.target.to = e.target.value })} placeholder="https://example.com" aria-label={t('sites.redirect_to')} />
              <Select value={String(tgt.code ?? 302)} onChange={(e) => set((x) => { x.target.code = Number(e.target.value) })} aria-label={t('sites.redirect_code')}>
                {[301, 302, 307, 308].map((c) => <option key={c} value={c}>{c}</option>)}
              </Select>
            </div>}
            {(tgt.type === 'container' || tgt.type === 'address') && <div className="mt-2 flex items-center gap-2">
              <span className="text-[12.5px] text-mute">{t('sites.scheme')}</span>
              <Segmented value={(tgt.scheme || 'http') as 'http' | 'https' | 'h2c'} onChange={(v) => set((x) => { x.target.scheme = v === 'http' ? '' : v })}
                options={[{ value: 'http', label: 'HTTP' }, { value: 'https', label: 'HTTPS' }, { value: 'h2c', label: 'gRPC (h2c)' }]} />
            </div>}
          </div>
        </div>

        <div>
          <div className="mb-1.5 text-[12.5px] font-semibold">{t('sites.tls')}</div>
          <div className="grid gap-1.5 sm:grid-cols-5">
            {tlsOptions.map((o) => (
              <button key={o.value} type="button" onClick={() => set((x) => { x.tls = { mode: o.value } })}
                className={cx('rounded-[var(--radius-box)] border px-2 py-1.5 text-left', s.tls.mode === o.value ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
                <div className="text-[13px] font-semibold">{o.title}</div>
                <div className="text-[11.5px] text-mute">{o.sub}</div>
              </button>
            ))}
          </div>
          {s.tls.mode === 'auto' && isLocal && <p className="mt-1 text-[12.5px] text-warn">{t('sites.tls_auto_local')}</p>}
          {s.tls.mode === 'dns' && <div className="mt-2">
            {cf.length === 0 && (integ.data?.items ?? []).length === 0 ? <Notice tone="warn">{t('sites.no_dns_account')}</Notice> : (
              <Select value={s.tls.dns ?? ''} onChange={(e) => set((x) => { x.tls.dns = e.target.value })} aria-label={t('sites.dns_account')}>
                <option value="">{t('sites.dns_account')}</option>
                {integ.data?.items.map((i) => <option key={i.id} value={i.id}>{i.name} ({i.kind})</option>)}
              </Select>)}
          </div>}
          {s.tls.mode === 'custom' && <Select className="mt-2" value={String(s.tls.cert_id ?? '')} onChange={(e) => set((x) => { x.tls.cert_id = Number(e.target.value) })} aria-label={t('sites.cert')}>
            <option value="">{t('sites.cert')}</option>
            {certs.data?.items.map((c) => <option key={c.id} value={c.id}>{c.name} · {c.info.domains.join(', ')}</option>)}
          </Select>}
        </div>

        {!site && !isLocal && (
          <div>
            <div className="mb-1.5 text-[12.5px] font-semibold">{t('sites.public')}</div>
            {cf.length === 0 ? <p className="text-[12.5px] text-mute">{t('sites.public_no_cf')}</p> : (
              <div className="space-y-1.5">
                <Switch checked={publish.on} onChange={(v) => setPublish((p) => ({ ...p, on: v, integration: p.integration || cf[0].id }))}
                  label={t('sites.publish_dns', { ip: pub.data?.ipv4 ?? '…' })} />
                {publish.on && <div className="flex flex-wrap items-center gap-2 pl-1">
                  <Select value={publish.integration} onChange={(e) => setPublish((p) => ({ ...p, integration: e.target.value, zone: '' }))} aria-label={t('sites.dns_account')}>
                    {cf.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
                  </Select>
                  <Select value={publish.zone} onChange={(e) => setPublish((p) => ({ ...p, zone: e.target.value }))} aria-label={t('sites.zone')}>
                    <option value="">{t('sites.zone')}</option>
                    {zones.data?.items.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}
                  </Select>
                  <Switch checked={publish.proxied} onChange={(v) => setPublish((p) => ({ ...p, proxied: v }))} label={t('sites.cf_proxied')} />
                </div>}
              </div>
            )}
            <p className="mt-1 text-[12px] text-mute">{t('sites.public_later')}</p>
          </div>
        )}

        <div>
          <button type="button" className="text-[12.5px] text-acc hover:underline" onClick={() => setAdvanced(!advanced)}>{advanced ? t('sites.hide_options') : t('sites.show_options')}</button>
          {advanced && <SiteOptions s={s} set={set} />}
        </div>
        <p className="text-[12px] text-mute">{t('sites.preview_note')}</p>
      </div>
    </Dialog>
  )
}

// SiteOptions: the less common settings, grouped.
function SiteOptions({ s, set }: { s: Site; set: (f: (x: Site) => void) => void }) {
  const bx = useBuiltin()
  const t = useT()
  const o = s.options
  const list = (v?: string[]) => (v ?? []).join(' ')
  const hdr = (v?: { op: string; name: string; value?: string }[]) => (v ?? []).map((h) => (h.op === 'del' ? `-${h.name}` : `${h.op === 'add' ? '+' : ''}${h.name}: ${h.value ?? ''}`)).join('\n')
  const parseHdr = (txt: string) => txt.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
    if (l.startsWith('-')) return { op: 'del' as const, name: l.slice(1).trim() }
    const add = l.startsWith('+')
    const [n, ...v] = (add ? l.slice(1) : l).split(':')
    return { op: add ? 'add' as const : 'set' as const, name: n.trim(), value: v.join(':').trim() }
  })
  const [reqH, setReqH] = useState(hdr(o.req_headers))
  const [respH, setRespH] = useState(hdr(o.resp_headers))
  const [paths, setPaths] = useState(JSON.stringify(o.paths ?? [], null, 1))
  const [pathErr, setPathErr] = useState(false)
  return (
    <div className="mt-2 grid gap-3 rounded-[var(--radius-box)] border border-line p-3 sm:grid-cols-2">
      <div className="space-y-1.5">
        <h4 className="text-[12.5px] font-semibold">{t('sites.grp_basic')}</h4>
        <Switch checked={!o.no_https_redirect} onChange={(v) => set((x) => { x.options.no_https_redirect = !v })} label={t('sites.opt_https_redirect')} />
        <Switch checked={!!o.hsts} onChange={(v) => set((x) => { x.options.hsts = v })} label="HSTS" />
        <Switch checked={!!o.compress} onChange={(v) => set((x) => { x.options.compress = v })} label={t('sites.opt_compress')} />
        <Switch checked={!!o.cache_static} onChange={(v) => set((x) => { x.options.cache_static = v })} label={t('sites.opt_cache_static')} />
        <Switch checked={!!o.real_ip_cloudflare} onChange={(v) => set((x) => { x.options.real_ip_cloudflare = v })} label={t('sites.opt_real_ip')} />
        <Field label={t('sites.opt_www')}>
          <Select value={o.www ?? ''} onChange={(e) => set((x) => { x.options.www = e.target.value as '' })}>
            <option value="">{t('sites.www_none')}</option><option value="to_www">{t('sites.www_to_www')}</option><option value="to_apex">{t('sites.www_to_apex')}</option>
          </Select>
        </Field>
      </div>
      <div className="space-y-1.5 sm:col-span-2">
        <h4 className="text-[12.5px] font-semibold">WAF</h4>
        {bx.data && !bx.data.waf.installed && !o.waf ? <NeedsExtension id="waf" /> : <div className="grid gap-2 sm:grid-cols-3">
          <Field label={t('sites.waf_mode')} hint={t('sites.waf_hint')}>
            <Select value={o.waf?.mode ?? ''} onChange={(e) => set((x) => { x.options.waf = e.target.value ? { ...(x.options.waf ?? {}), mode: e.target.value as 'block' } : null })}>
              <option value="">{t('sites.waf_off')}</option><option value="detect">{t('sites.waf_detect')}</option><option value="block">{t('sites.waf_block')}</option>
            </Select>
          </Field>
          {o.waf && <>
            <Field label={t('sites.waf_paranoia')}>
              <Select value={String(o.waf.paranoia ?? 1)} onChange={(e) => set((x) => { x.options.waf = { ...x.options.waf!, paranoia: Number(e.target.value) } })}>
                {[1, 2, 3, 4].map((n) => <option key={n} value={n}>{t('sites.waf_pl', { n })}</option>)}
              </Select>
            </Field>
            <Field label={t('sites.waf_exclude_rules')}><Input className="num" defaultValue={(o.waf.exclude_rules ?? []).join(' ')} placeholder="942100 941100"
              onBlur={(e) => set((x) => { x.options.waf = { ...x.options.waf!, exclude_rules: lines(e.target.value).map(Number).filter((n) => n > 0) } })} /></Field>
            <Field label={t('sites.waf_exclude_paths')}><Input className="num" defaultValue={(o.waf.exclude_paths ?? []).join(' ')} placeholder="/api/upload"
              onBlur={(e) => set((x) => { x.options.waf = { ...x.options.waf!, exclude_paths: lines(e.target.value) } })} /></Field>
          </>}
        </div>}
      </div>
      <div className="space-y-1.5">
        <h4 className="text-[12.5px] font-semibold">{t('sites.grp_access')}</h4>
        <Field label={t('sites.opt_allow_ips')} hint={t('sites.ips_hint')}><Input defaultValue={list(o.allow_ips)} onBlur={(e) => set((x) => { x.options.allow_ips = lines(e.target.value) })} /></Field>
        <Field label={t('sites.opt_deny_ips')}><Input defaultValue={list(o.deny_ips)} onBlur={(e) => set((x) => { x.options.deny_ips = lines(e.target.value) })} /></Field>
        <Field label={t('sites.opt_rate_limit')} hint={t('sites.rate_limit_hint')}>
          <div className="flex items-center gap-2 text-[12.5px]">
            <Input type="number" min={0} className="w-24" defaultValue={o.rate_limit?.requests ?? ''} placeholder="0"
              onBlur={(e) => set((x) => { const n = Number(e.target.value) || 0; x.options.rate_limit = n > 0 ? { requests: n, window: x.options.rate_limit?.window || 60 } : null })} />
            <span>{t('sites.rl_per')}</span>
            <Select className="w-28" value={String(o.rate_limit?.window ?? 60)} onChange={(e) => set((x) => { if (x.options.rate_limit) x.options.rate_limit = { ...x.options.rate_limit, window: Number(e.target.value) } })}>
              {[[1, 'rl_1s'], [10, 'rl_10s'], [60, 'rl_1m'], [3600, 'rl_1h']].map(([v, k]) => <option key={v} value={v}>{t('sites.' + k)}</option>)}
            </Select>
          </div>
        </Field>
        <Field label={t('sites.opt_cors')} hint={t('sites.cors_hint')}><Input defaultValue={list(o.cors?.origins)} onBlur={(e) => set((x) => { const v = lines(e.target.value); x.options.cors = v.length ? { origins: v } : null })} /></Field>
        <Field label={t('sites.opt_timeouts')}>
          <div className="flex gap-2">
            <Input type="number" min={0} defaultValue={o.timeouts?.dial ?? ''} placeholder={t('sites.dial_s')} onBlur={(e) => set((x) => { x.options.timeouts = { ...x.options.timeouts, dial: Number(e.target.value) || 0 } })} />
            <Input type="number" min={0} defaultValue={o.timeouts?.read ?? ''} placeholder={t('sites.read_s')} onBlur={(e) => set((x) => { x.options.timeouts = { ...x.options.timeouts, read: Number(e.target.value) || 0 } })} />
          </div>
        </Field>
        <Field label={t('sites.opt_log_days')}><Input type="number" min={1} max={365} defaultValue={o.log_days ?? 14} onBlur={(e) => set((x) => { x.options.log_days = Number(e.target.value) || 14 })} /></Field>
      </div>
      <div className="space-y-1.5 sm:col-span-2">
        <h4 className="text-[12.5px] font-semibold">{t('sites.grp_headers')}</h4>
        <p className="text-[12px] text-mute">{t('sites.headers_hint')}</p>
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label={t('sites.req_headers')}><textarea className="num h-20 w-full rounded-[var(--radius-box)] border border-line bg-bg p-2 text-[12px]" value={reqH} onChange={(e) => setReqH(e.target.value)} onBlur={() => set((x) => { x.options.req_headers = parseHdr(reqH) })} /></Field>
          <Field label={t('sites.resp_headers')}><textarea className="num h-20 w-full rounded-[var(--radius-box)] border border-line bg-bg p-2 text-[12px]" value={respH} onChange={(e) => setRespH(e.target.value)} onBlur={() => set((x) => { x.options.resp_headers = parseHdr(respH) })} /></Field>
        </div>
        <Field label={t('sites.path_rules')} hint={t('sites.path_rules_hint')}>
          <textarea className={cx('num h-24 w-full rounded-[var(--radius-box)] border bg-bg p-2 text-[12px]', pathErr ? 'border-bad' : 'border-line')} value={paths}
            onChange={(e) => setPaths(e.target.value)}
            onBlur={() => { try { const v = JSON.parse(paths || '[]'); setPathErr(false); set((x) => { x.options.paths = v }) } catch { setPathErr(true) } }} />
        </Field>
        {(o.basic_auth?.length ?? 0) > 0 && <p className="text-[12px] text-mute">{t('sites.basic_auth_set', { n: o.basic_auth!.length })} <Pill>bcrypt</Pill></p>}
      </div>
    </div>
  )
}

interface PhpRuntime { version: string; installed: boolean; running: boolean }

// The PHP part of a new site: which runtime, and what to put in it.
function PhpTarget({ php, native, rootSub, onRootSub, onPhp, app, onApp, db, onDb, editing }: { php?: string; native: boolean; rootSub: string; onRootSub: (v: string) => void; onPhp: (v: string, native: boolean) => void; app: string; onApp: (a: 'blank' | 'wordpress' | 'typecho' | 'discuz') => void; db: number; onDb: (n: number) => void; editing: boolean }) {
  const t = useT()
  const { host } = useHost()
  const rts = useApi<{ items: PhpRuntime[] }>(`/api/v1/hosts/${host}/php`, [host])
  const nrts = useApi<{ items: { version: string; installed: boolean }[] }>(`/api/v1/hosts/${host}/php/native`, [host])
  const nativeVers = (nrts.data?.items ?? []).filter((r) => r.installed).map((r) => r.version)
  const dbs = useApi<{ items: { id: number; name: string; engine: string }[] }>(`/api/v1/hosts/${host}/databases`, [host])
  const installed = (rts.data?.items ?? []).filter((r) => r.installed)
  const mysql = (dbs.data?.items ?? []).filter((d) => d.engine === 'mysql' || d.engine === 'mariadb')
  // a native runtime first (each site gets its own sandboxed PHP), else a container one
  useEffect(() => {
    if (php) return
    if (nativeVers[0]) onPhp(nativeVers[nativeVers.length - 1].replace('.', ''), true)
    else if (installed[0]) onPhp(installed[0].version.replace('.', ''), false)
  }, [installed.length, nativeVers.length]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (!db && mysql[0]) onDb(mysql[0].id) }, [mysql.length]) // eslint-disable-line react-hooks/exhaustive-deps
  if (rts.data && nrts.data && installed.length === 0 && nativeVers.length === 0) return <p className="text-[12.5px] text-warn">{t('php.none_installed')}</p>
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-[13px]">
        <span className="text-mute">{t('php.version')}</span>
        <Select value={(native ? 'n' : '') + (php ?? '')} onChange={(e) => onPhp(e.target.value.replace(/^n/, ''), e.target.value.startsWith('n'))} aria-label={t('php.version')}>
          {[...nativeVers].reverse().map((v) => <option key={'n' + v} value={'n' + v.replace('.', '')}>PHP {v} · {t('phps.native')}</option>)}
          {installed.map((r) => <option key={r.version} value={r.version.replace('.', '')}>PHP {r.version} · {t('phps.container')}</option>)}
        </Select>
      </div>
      {native && <div className="flex items-center gap-2 text-[13px]"><span className="text-mute">{t('phps.root_sub')}</span>
        <Input className="num h-8 w-40" value={rootSub} placeholder={t('phps.root_sub_ph')} onChange={(e) => onRootSub(e.target.value.trim())} aria-label={t('phps.root_sub')} /></div>}
      <p className="text-[12px] text-mute">{native ? t('phps.native_pick_hint') : t('phps.container_pick_hint')}</p>
      {!editing && <>
        <Segmented value={app as 'blank' | 'wordpress' | 'typecho' | 'discuz'} onChange={onApp} options={[{ value: 'blank', label: t('php.app_blank') }, { value: 'wordpress', label: 'WordPress' }, { value: 'typecho', label: 'Typecho' }, { value: 'discuz', label: 'Discuz!' }]} />
        {app !== 'blank' && (mysql.length === 0 ? <p className="text-[12.5px] text-warn">{t('php.need_db')}</p> :
          <div className="flex items-center gap-2 text-[13px]"><span className="text-mute">{t('php.db')}</span>
            <Select value={String(db)} onChange={(e) => onDb(Number(e.target.value))}>{mysql.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}</Select></div>)}
        <p className="text-[12px] text-mute">{t('php.hint_' + app)}</p>
      </>}
    </div>
  )
}
