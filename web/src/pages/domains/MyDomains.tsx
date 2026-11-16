import { ChevronDown, ChevronRight, Globe, Plus, Rocket, Search } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { date } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { Records } from '../sites/DnsTab'
import { NewSiteDialog } from '../sites/NewSiteDialog'

export interface DomUse { type: 'site' | 'project' | 'tunnel'; id: string; name: string }
export interface DomSub { name: string; records?: string[]; points: 'here' | 'cloudflare' | 'elsewhere' | 'none' | 'unknown'; proxied: boolean; record_id?: string; record_type?: string; used_by: DomUse[]; tls?: string; cert_expires?: number }
export interface DomRoot { root: string; account?: string; provider?: string; zone_id?: string; expires?: number; registrar?: string; subs: DomSub[]; zone: boolean }

const DAY = 86400

// expiryTone: yellow inside 30 days, red inside 7 (or past).
export function expiryTone(exp: number | undefined, now: number): 'ok' | 'warn' | 'bad' | undefined {
  if (!exp) return undefined
  const left = exp - now
  return left < 7 * DAY ? 'bad' : left < 30 * DAY ? 'warn' : 'ok'
}

const POINT_TONE = { here: 'ok', cloudflare: 'acc', elsewhere: 'warn', none: 'bad', unknown: undefined } as const

// 我的域名: each registered domain once — its DNS account, when it runs out, and every name in use under it.
export function MyDomains() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<{ items: DomRoot[]; here: string[] }>(`/api/v1/hosts/${host}/domains/overview`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [q, setQ] = useState('')
  const [records, setRecords] = useState<DomRoot | null>(null)
  const [newSite, setNewSite] = useState<string[] | null>(null)
  const now = Date.now() / 1000
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('domains.loading')}</div>
  const here4 = d.data.here.find((ip) => !ip.includes(':'))
  const items = d.data.items.filter((r) => !q || r.root.includes(q) || r.subs.some((s) => s.name.includes(q)))
  const pointHere = (r: DomRoot, s: DomSub) => {
    if (!r.account || !r.zone_id || !here4) return
    const rec = { id: s.record_id, type: 'A', name: s.name, content: here4, ttl: 1, proxied: s.proxied }
    if (s.record_id && s.record_type === 'A') run('dns.record.update', { integration: r.account, zone_id: r.zone_id, record: rec }, { host })
    else run('dns.record.create', { integration: r.account, zone_id: r.zone_id, record: { ...rec, id: undefined } }, { host })
  }
  return (
    <div className="space-y-4">
      <Box title={t('domains.mine')} count={items.length} right={<div className="flex items-center gap-2">
        <div className="relative"><Search className="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-mute" /><Input className="h-7 w-44 pl-7 text-[12.5px]" placeholder={t('domains.search')} value={q} onChange={(e) => setQ(e.target.value.trim().toLowerCase())} aria-label={t('domains.search')} /></div>
        {manage && <Button size="sm" variant="primary" onClick={() => setNewSite([])}><Plus className="size-3.5" />{t('action.site.create')}</Button>}</div>}>
        <p className="border-b border-line px-4 py-2 text-[12.5px] text-mute">{t('domains.mine_hint', { ip: d.data.here.join(' / ') || '—' })}</p>
        {items.length === 0 && <Empty title={t('domains.none')}>{t('domains.none_hint')}</Empty>}
        {items.map((r) => {
          const tone = expiryTone(r.expires, now)
          const isOpen = open[r.root] ?? r.subs.length > 0
          const away = r.subs.filter((s) => s.points === 'none' || s.points === 'elsewhere').length
          return (
            <div key={r.root} className="border-b border-line last:border-b-0">
              <Row onClick={() => setOpen({ ...open, [r.root]: !isOpen })}>
                {r.subs.length ? (isOpen ? <ChevronDown className="size-4 text-mute" /> : <ChevronRight className="size-4 text-mute" />) : <span className="w-4" />}
                <Globe className="size-4 text-mute" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-[13.5px]"><b className="num">{r.root}</b>
                    {r.account ? <Pill>{t('dns.kind_' + r.provider)}</Pill> : <Pill tone="warn">{t('domains.no_account')}</Pill>}
                    {away > 0 && <Pill tone="warn">{t('domains.n_away', { n: away })}</Pill>}</div>
                  <div className="text-[12px] text-mute">
                    {r.subs.length ? t('domains.n_used', { n: r.subs.length }) : t('domains.unused')}
                    {r.registrar ? ' · ' + r.registrar : ''}
                  </div>
                </div>
                <span className="text-[12px]">{r.expires
                  ? <Pill tone={tone}>{t(r.expires < now ? 'domains.expired' : tone === 'ok' ? 'domains.expires' : 'domains.expires_soon', { when: date(r.expires), days: Math.max(0, Math.floor((r.expires - now) / DAY)) })}</Pill>
                  : <span className="text-mute">{t('domains.expiry_unknown')}</span>}</span>
                {r.account && r.zone_id && <Button size="sm" variant="ghost" onClick={(e) => { e.stopPropagation(); setRecords(r) }}>{t('domains.records')}</Button>}
                {!r.account && manage && <Link onClick={(e) => e.stopPropagation()} className="text-[12px] text-acc hover:underline" to={hostPath(host, 'domains/accounts')}>{t('domains.add_account')}</Link>}
              </Row>
              {isOpen && r.subs.map((s) => (
                <div key={s.name} className="flex flex-wrap items-center gap-2 border-t border-dashed border-line py-2 pl-14 pr-4 text-[12.5px]">
                  <span className="num min-w-40 font-medium">{s.name}</span>
                  <Pill tone={POINT_TONE[s.points]}>{t('domains.p_' + s.points)}</Pill>
                  {s.records && s.records.length > 0 && s.points !== 'here' && <span className="num text-mute">{s.records.slice(0, 2).join(', ')}</span>}
                  {s.tls && <span className="text-mute">{t('domains.tls_' + s.tls)}{s.cert_expires ? ' · ' + t('domains.cert_until', { when: date(s.cert_expires) }) : ''}</span>}
                  <span className="flex flex-1 flex-wrap justify-end gap-1.5">
                    {s.used_by.map((u) => (
                      <Link key={u.type + u.id} className="inline-flex items-center gap-1 rounded-md border border-line px-1.5 py-0.5 text-[12px] hover:border-acc hover:text-acc"
                        to={hostPath(host, u.type === 'site' ? 'sites/s/' + u.id : u.type === 'project' ? 'projects' : 'cloudflare')}>
                        {u.type === 'project' ? <Rocket className="size-3" /> : <Globe className="size-3" />}{u.name}
                      </Link>
                    ))}
                    {manage && r.account && here4 && (s.points === 'none' || s.points === 'elsewhere') && !s.name.startsWith('*.') &&
                      <Button size="sm" variant="primary" onClick={() => pointHere(r, s)}>{t('domains.point_here')}</Button>}
                  </span>
                </div>
              ))}
            </div>
          )
        })}
      </Box>
      {records && <Records acc={records.account!} zone={records.zone_id!} zoneName={records.root} manage={manage} onClose={() => setRecords(null)} />}
      {newSite && <NewSiteDialog domains={newSite} onClose={() => setNewSite(null)} onDone={() => void d.reload()} />}
    </div>
  )
}
