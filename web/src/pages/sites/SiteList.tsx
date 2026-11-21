import { ChevronDown, ChevronRight, ExternalLink, Lock, LockOpen, Waypoints } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Spark } from '@/components/charts/Spark'
import { hostPath } from '@/components/shell/host'
import { Box, Dot, Pill } from '@/components/ui/Box'
import { cx } from '@/components/ui/cx'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { jumpTo, siteURL } from './SitePage'
import type { Stats } from './StatsView'
import { siteName, targetText, type Site } from './types'

const MULTI = new Set(['co.nz', 'net.nz', 'org.nz', 'ac.nz', 'govt.nz', 'geek.nz', 'school.nz', 'com.au', 'net.au', 'org.au', 'co.uk', 'org.uk', 'me.uk', 'ac.uk',
  'com.cn', 'net.cn', 'org.cn', 'gov.cn', 'com.hk', 'org.hk', 'com.tw', 'org.tw', 'co.jp', 'ne.jp', 'or.jp', 'com.sg', 'com.my', 'co.kr', 'com.br', 'co.in', 'co.za'])

// registrable: the domain a name was registered as (shop.example.co.nz → example.co.nz).
export function registrable(name: string) {
  const n = name.toLowerCase().replace(/^\*\./, '')
  if (/^[\d.]+$/.test(n) || n.includes(':') || /\.(lan|local|home\.arpa|internal|test|localhost)$/.test(n)) return ''
  const p = n.split('.')
  if (p.length < 2) return ''
  return p.slice(MULTI.has(p.slice(-2).join('.')) ? -3 : -2).join('.')
}

export const groupOf = (s: Site) => s.options.group?.trim() || registrable(s.domains[0] ?? '') || ''

export function SiteVisitors({ host, id }: { host: string; id: number }) {
  const t = useT()
  const s = useApi<Stats>(`/api/v1/hosts/${host}/sites/${id}/stats?range=24h`, [id])
  if (!s.data) return <span className="w-[150px]" />
  const humans = s.data.total.humans ?? s.data.total.uniq
  return (
    <span className="flex items-center gap-2" title={t('sitel.visitors_tip', { humans, bots: s.data.total.bots, req: s.data.total.req })}>
      <Spark values={s.data.series.map((p) => p.humans ?? p.uniq ?? 0)} label={String(humans)} />
      <span className="w-16 text-right text-[12px]"><b className="num font-semibold">{humans}</b> <span className="text-mute">{t('sitel.people')}</span></span>
    </span>
  )
}

// SiteList: sites in groups (the registered domain, or a group name set on the site), each row
// with how it is reached, its certificate, real visitors and shortcuts.
export function SiteList({ host, sites }: { host: string; sites: Site[] }) {
  const t = useT()
  const nav = useNavigate()
  const cf = useApi<{ routes: { site_id: number }[] }>(`/api/v1/hosts/${host}/cloudflare`, [host])
  const tunneled = new Set((cf.data?.routes ?? []).map((r) => r.site_id))
  const [closed, setClosed] = useState<Record<string, boolean>>({})
  const groups = new Map<string, Site[]>()
  for (const s of sites) groups.set(groupOf(s), [...(groups.get(groupOf(s)) ?? []), s])
  const named = [...groups].filter(([g, l]) => g && (l.length > 1 || l.some((s) => s.options.group))).sort(([a], [b]) => a.localeCompare(b))
  const rest = sites.filter((s) => !named.some(([, l]) => l.includes(s)))
  const row = (s: Site) => {
    const jump = jumpTo(host, s)
    const tls = s.tls.mode !== 'off'
    return (
      <div key={s.id} role="link" tabIndex={0} onClick={() => nav(hostPath(host, 'sites/s/' + s.id))} onKeyDown={(e) => e.key === 'Enter' && nav(hostPath(host, 'sites/s/' + s.id))}
        className="grid cursor-pointer grid-cols-[auto_1fr_auto] items-center gap-x-3 gap-y-1 border-b border-line px-4 py-2.5 last:border-b-0 hover:bg-soft lg:grid-cols-[auto_minmax(180px,1.4fr)_minmax(150px,1fr)_auto_auto_auto_auto]">
        <Dot state={s.enabled ? 'ok' : 'off'} />
        <div className="min-w-0">
          <div className="flex items-baseline gap-1.5 text-[13.5px]" title={s.domains.join('\n')}>
            <span className="truncate font-semibold">{siteName(s)}</span>
            {s.domains.length > 1 && <span className="num shrink-0 rounded bg-soft px-1.5 text-[11.5px] text-mute">{t('sites.more_domains', { n: s.domains.length - 1 })}</span>}
          </div>
          <div className="num truncate text-[12px] text-mute">{s.options.note ? s.domains[0] : s.domains.slice(1).join(' · ') || ' '}</div>
        </div>
        <div className="col-start-2 min-w-0 text-[12.5px] lg:col-start-auto" onClick={(e) => e.stopPropagation()}>
          {jump ? <Link to={jump.to} className="flex min-w-0 items-center gap-1.5 hover:text-acc" title={t(jump.label)}>
            <jump.Icon className="size-3.5 shrink-0 text-mute" aria-hidden /><span className="num truncate">{targetText(s)}</span></Link>
            : <span className="num truncate text-mute">→ {targetText(s)}</span>}
        </div>
        <span className="hidden lg:inline-flex">{tunneled.has(s.id) ? <Pill tone="acc"><Waypoints className="mr-0.5 size-3" />{t('sitel.tunnel')}</Pill> : <Pill>{t('sitel.direct')}</Pill>}</span>
        <span className="hidden lg:inline-flex"><Pill tone={tls ? 'ok' : 'warn'}>{tls ? <Lock className="mr-0.5 size-3" /> : <LockOpen className="mr-0.5 size-3" />}{t('sites.tls_' + s.tls.mode)}</Pill></span>
        <span className="hidden sm:inline-flex"><SiteVisitors host={host} id={s.id} /></span>
        <span className="row-span-2 flex justify-end lg:row-span-1" onClick={(e) => e.stopPropagation()}>
          <a href={siteURL(s)} target="_blank" rel="noreferrer" title={t('sitep.open')} aria-label={t('sitep.open')} className="rounded-[8px] p-1.5 text-mute hover:bg-canvas hover:text-acc"><ExternalLink className="size-4" /></a>
        </span>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      {named.map(([g, l]) => (
        <Box key={g} title={<button type="button" className="flex items-center gap-1" onClick={() => setClosed({ ...closed, [g]: !closed[g] })}>
          {closed[g] ? <ChevronRight className="size-4" /> : <ChevronDown className="size-4" />}<span className="num">{g}</span></button>} count={l.length}>
          {!closed[g] && l.map(row)}
        </Box>
      ))}
      {rest.length > 0 && <Box title={named.length ? t('sitel.others') : t('nav.sites')} count={rest.length}>{rest.map(row)}</Box>}
      <p className={cx('px-1 text-[12px] text-mute', named.length === 0 && 'hidden')}>{t('sitel.group_hint')}</p>
    </div>
  )
}
