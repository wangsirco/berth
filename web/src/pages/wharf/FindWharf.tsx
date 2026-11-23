import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ChevronDown, Radar } from 'lucide-react'
import { WharfMark } from '@/components/brand/WharfMark'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { post } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Where } from '@/components/access/MainName'
import { WharfAsk } from '../links/WharfAsk'

// AdoptChoice: said before pairing, not after — with a Wharf that registers names the panel
// moves from host.local to host.lan, and Passkeys need adding again on the new name.
export function AdoptChoice({ adopt, setAdopt }: { adopt: boolean; setAdopt: (v: boolean) => void }) {
  const t = useT()
  const w = useApi<Where>('/api/v1/access/where')
  if (!w.data) return null
  const now = w.data.lan_name
  const base = now.replace(/\.(local|lan|home\.arpa|internal)$/, '')
  const next = base + '.lan'
  if (!now || now === next) return null
  return (
    <div className="rounded-[var(--radius-box)] border border-acc bg-acc/10 px-3.5 py-2.5 text-[12.5px]" data-adopt-choice>
      <label className="flex cursor-pointer items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={adopt} onChange={(e) => setAdopt(e.target.checked)} />
        <span><b>{t('adopt.title', { next })}</b>{' '}<span className="rounded bg-acc px-1 py-0.5 text-[10.5px] font-semibold text-white">{t('welcome.recommended')}</span>
          <span className="mt-1 block text-mute">{t('adopt.d', { now, next })}</span>
          {adopt && w.data.passkeys_here > 0 && <span className="mt-1 block text-warn">{t('adopt.passkey', { now, next })}</span>}
        </span>
      </label>
    </div>
  )
}

// FindWharf: the router's Wharf, found by itself, bound right here (like Bluetooth)
export function FindWharf({ manual }: { manual: React.ReactNode }) {
  const t = useT()
  const [items, setItems] = useState<{ addr: string; gateway?: boolean }[] | null>(null)
  const [more, setMore] = useState(false)
  const [adopt, setAdopt] = useState(true)
  const find = () => { setItems(null); void post<{ items: { addr: string; gateway?: boolean }[] }>('/api/v1/wharf/find', {}).then((r) => setItems(r.items)).catch(() => setItems([])) }
  useEffect(find, [])
  const f = items?.[0]
  return (
    <div className="space-y-3 text-[13px]" data-find-wharf>
      {!items && <div className="flex items-center gap-2 text-mute"><Radar className="size-4 animate-spin" />{t('findw.looking')}</div>}
      {f && <div className="space-y-2.5 rounded-[var(--radius-box)] border border-line p-3" data-found="wharf">
        <div className="flex items-center gap-3">
          <WharfMark className="size-6" />
          <div className="min-w-0 flex-1"><div className="font-semibold">{t('links.found_wharf')}{f.gateway && <span className="ml-2 text-[11.5px] font-normal text-mute">{t('links.is_router')}</span>}</div><div className="num text-[12px] text-mute">{f.addr}</div></div>
        </div>
        <AdoptChoice adopt={adopt} setAdopt={setAdopt} />
        <WharfAsk addr={f.addr} adopt={adopt} onManual={() => setMore(true)} />
      </div>}
      {items && !f && <Notice>{t('findw.none')} <Link className="text-acc hover:underline" to="/local/links">{t('findw.scan_all')}</Link></Notice>}
      <button type="button" className="flex items-center gap-1 text-[12.5px] text-mute hover:text-fg" onClick={() => setMore(!more)}>
        <ChevronDown className={cx('size-3.5 transition-transform', more && 'rotate-180')} />{t('findw.manual')}
      </button>
      {more && manual}
      {items && <Button size="sm" variant="ghost" onClick={find}>{t('links.rescan')}</Button>}
    </div>
  )
}
