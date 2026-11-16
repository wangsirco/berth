import { useState } from 'react'
import { ObjectActivity } from '@/components/ai/Activity'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Pill } from '@/components/ui/Box'
import { Segmented } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { DiagnoseView } from './DiagnoseView'
import { NewSiteDialog } from './NewSiteDialog'
import { PhpSite } from './PhpSite'
import { SiteLog, SiteProtect } from './SiteExtras'
import { StatsView } from './StatsView'
import { siteName, targetText, type Site } from './types'

interface Detail { site: Site; relations: { from: { type: string; id: string }; to: { type: string; id: string; name?: string }; kind: string }[] }

export function SiteDrawer({ id, onClose, onChanged }: { id: number; onClose: () => void; onChanged: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const [tab, setTab] = useState<'info' | 'php' | 'stats' | 'log' | 'protect' | 'diag' | 'rel'>('info')
  const [edit, setEdit] = useState(false)
  const d = useApi<Detail>(`/api/v1/hosts/${host}/sites/${id}`, [id])
  const s = d.data?.site
  const manage = can(user, 'sites.manage')
  const done = () => { void d.reload(); onChanged() }
  const kv = (k: string, v: React.ReactNode) => <div className="grid grid-cols-[110px_1fr] gap-2 border-b border-line px-4 py-2 text-[13px]"><span className="text-mute">{k}</span><span className="min-w-0 break-words">{v}</span></div>
  const hasDNS = d.data?.relations.some((r) => r.kind === 'publishes')
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={s ? siteName(s) : '…'}
      right={s && manage && (
        <div className="flex gap-1.5">
          <Button size="sm" onClick={() => setEdit(true)}>{t('sites.edit')}</Button>
          <Button size="sm" onClick={() => run('site.toggle', { id, enabled: !s.enabled }, { host, onDone: done })}>{s.enabled ? t('sites.disable') : t('sites.enable')}</Button>
          <Button size="sm" variant="danger" onClick={() => run('site.delete', { id, with_dns: hasDNS }, { host, onDone: onClose, confirmName: s.domains[0] })}>{t('action.site.delete')}</Button>
        </div>
      )}>
      <ObjectActivity type="site" id={id} className="mx-4 mt-3" />
      <div className="border-b border-line px-4 py-2">
        <Segmented value={tab} onChange={setTab} options={[{ value: 'info', label: t('sites.tab_info') }, ...(s?.target.type === 'php' ? [{ value: 'php' as const, label: 'PHP' }] : []), { value: 'stats', label: t('sites.tab_stats') }, { value: 'log', label: t('sites.tab_log') }, { value: 'protect', label: t('sites.tab_protect') }, { value: 'diag', label: t('sites.tab_diag') }, { value: 'rel', label: t('sites.tab_rel') }]} />
      </div>
      {tab === 'info' && s && <div className="pb-4">
        {s.options.note && kv(t('sites.note'), s.options.note)}
        {kv(t('sites.domains'), <div className="space-y-0.5">{s.domains.map((x) => <div key={x}><a className="text-acc hover:underline" href={(s.tls.mode === 'off' ? 'http://' : 'https://') + x.replace('*.', 'www.')} target="_blank" rel="noreferrer">{x}</a></div>)}</div>)}
        {kv(t('sites.status'), s.enabled ? <Pill tone="ok">{t('sites.enabled')}</Pill> : <Pill>{t('sites.disabled')}</Pill>)}
        {kv(t('sites.target'), <span className="num">{t('sites.target_' + s.target.type)} · {targetText(s)}{s.target.scheme ? ` (${s.target.scheme})` : ''}</span>)}
        {kv(t('sites.tls'), t('sites.tls_' + s.tls.mode) + (s.tls.dns ? ` · ${s.tls.dns}` : '') + (s.tls.cert_id ? ` · #${s.tls.cert_id}` : ''))}
        {kv(t('sites.options'), <div className="flex flex-wrap gap-1">
          {!s.options.no_https_redirect && s.tls.mode !== 'off' && <Pill>HTTP→HTTPS</Pill>}
          {s.options.hsts && <Pill>HSTS</Pill>}{s.options.compress && <Pill>gzip/zstd</Pill>}{s.options.cache_static && <Pill>{t('sites.opt_cache_static')}</Pill>}
          {s.options.www && <Pill>{t('sites.www_' + s.options.www)}</Pill>}{(s.options.allow_ips?.length ?? 0) > 0 && <Pill>{t('sites.opt_allow_ips')}</Pill>}
          {s.options.cors && <Pill>CORS</Pill>}{(s.options.paths?.length ?? 0) > 0 && <Pill>{t('sites.path_rules')} ×{s.options.paths!.length}</Pill>}
          {(s.options.basic_auth?.length ?? 0) > 0 && <Pill>{t('sites.basic_auth')}</Pill>}
          {s.options.rate_limit && <Pill>{t('sites.rl_pill', { n: s.options.rate_limit.requests, s: s.options.rate_limit.window })}</Pill>}
          {s.options.waf && <Pill tone={s.options.waf.mode === 'block' ? 'acc' : undefined}>WAF · {t('sites.waf_' + s.options.waf.mode)}</Pill>}
        </div>)}
        {kv(t('sites.log'), <span className="num text-[12px]">/srv/berth/caddy/logs/{s.domains[0]}.log · {t('sites.keep_days', { n: s.options.log_days ?? 14 })}</span>)}
      </div>}
      {tab === 'php' && s && <PhpSite s={s} />}
      {tab === 'stats' && <StatsView host={host} siteId={id} />}
      {tab === 'log' && <SiteLog id={id} />}
      {tab === 'protect' && s && <SiteProtect s={s} onDone={done} />}
      {tab === 'diag' && <DiagnoseView host={host} siteId={id} />}
      {tab === 'rel' && <div className="p-4 text-[13px]">
        {(d.data?.relations ?? []).length === 0 && <p className="text-mute">{t('containers.no_relations')}</p>}
        <ul className="space-y-1">{d.data?.relations.map((r, i) => <li key={i}><span className="text-mute">{t('rel.' + r.kind)}</span> {t('obj.' + r.to.type)} <b className="num font-medium">{r.to.name || r.to.id}</b></li>)}</ul>
      </div>}
      {edit && s && <NewSiteDialog site={s} onClose={() => setEdit(false)} onDone={done} />}
    </Drawer>
  )
}
