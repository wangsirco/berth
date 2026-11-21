import { Check, CircleAlert, CircleX, Loader2, Waypoints } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { get } from '@/lib/api'
import { useT } from '@/lib/i18n'

interface Rec { id?: string; type: string; name: string; content: string; ttl?: number; proxied?: boolean }
export interface NameDNS { name: string; root: string; account?: string; provider?: string; zone_id?: string; records: Rec[]; points: 'here' | 'tunnel' | 'elsewhere' | 'none' | 'unknown'; tunnel: boolean; suggest?: Rec }

const isLocal = (d: string) => /\.(lan|local|home\.arpa|internal|test|localhost)$/i.test(d) || /^[\d.]+$/.test(d) || d.includes(':')

// DomainDNS: for each name of a site, where its DNS points and, when a DNS account holds the
// zone, one click to create or fix the record. Every button is a change with a preview, so
// DNS only moves after a second confirmation.
export function DomainDNS({ host, names, manage }: { host: string; names: string[]; manage: boolean }) {
  const t = useT()
  const run = useChange()
  const list = names.filter((n) => n && !isLocal(n) && !n.startsWith('*.'))
  const key = list.join(',')
  const [items, setItems] = useState<NameDNS[] | null>(null)
  const [busy, setBusy] = useState(false)
  const load = () => {
    if (!key) { setItems([]); return }
    setBusy(true)
    get<{ items: NameDNS[] }>(`/api/v1/hosts/${encodeURIComponent(host)}/domains/lookup?names=${encodeURIComponent(key)}`)
      .then((r) => setItems(r.items)).catch(() => setItems([])).finally(() => setBusy(false))
  }
  useEffect(() => {
    const id = setTimeout(load, 600) // typing: wait for a pause
    return () => clearTimeout(id)
  }, [key, host]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!key) return null
  return (
    <div className="space-y-1.5 rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12.5px]">
      <div className="flex items-center gap-2 font-semibold">{t('dnsl.title')}{busy && <Loader2 className="size-3.5 animate-spin text-mute" aria-hidden />}</div>
      {(items ?? []).map((x) => {
        const cur = x.records.filter((r) => r.type !== 'TXT')
        const ok = x.points === 'here' || x.points === 'tunnel'
        const Icon = ok ? Check : x.points === 'none' ? CircleAlert : x.points === 'elsewhere' ? CircleX : CircleAlert
        const tone = ok ? 'text-ok' : x.points === 'elsewhere' ? 'text-bad' : 'text-warn'
        const sug = x.suggest
        const first = cur.find((r) => r.id) // a record the account can change
        return (
          <div key={x.name} className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Icon className={cx('size-3.5 shrink-0', tone)} aria-hidden />
            <span className="num font-medium">{x.name}</span>
            <span className={tone}>{t('dnsl.p_' + x.points)}</span>
            {x.tunnel && <span className="inline-flex items-center gap-0.5 text-acc"><Waypoints className="size-3" />{t('dnsl.tunnel')}</span>}
            {cur.length > 0 && <span className="num text-mute">{cur.map((r) => `${r.type} ${r.content}${r.proxied ? ' ☁' : ''}`).join(' · ')}</span>}
            {!x.account && x.points !== 'here' && x.points !== 'tunnel' && <span className="text-mute">{t('dnsl.no_account', { root: x.root })}</span>}
            {manage && x.account && sug && !ok && (
              x.points === 'none' || !first
                ? <Button size="sm" onClick={() => run('dns.record.create', { integration: x.account, zone_id: x.zone_id, record: sug }, { host, onDone: load })}>{t('dnsl.create', { type: sug.type, content: sug.content })}</Button>
                : <Button size="sm" onClick={() => run('dns.record.update', { integration: x.account, zone_id: x.zone_id, record: { ...sug, id: first.id } }, { host, onDone: load })}>{t('dnsl.change', { type: sug.type, content: sug.content })}</Button>
            )}
          </div>
        )
      })}
      {items && items.some((x) => x.account) && <p className="text-[11.5px] text-mute">{t('dnsl.confirm_note')}</p>}
    </div>
  )
}
