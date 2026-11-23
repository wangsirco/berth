import { useEffect, useState } from 'react'
import { Check, ChevronDown, Copy, Globe, House, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { LocalNameDialog } from './LocalNameGuide'
import { reachable, type Where } from './MainName'

// AccessCard: where the panel opens — its LAN name, its public name (both without a port once
// the edge serves them) and the fallback address, with whether this device opens each.
export function AccessCard({ publicField }: { publicField: React.ReactNode }) {
  const t = useT()
  const w = useApi<Where>('/api/v1/access/where')
  const [ok, setOk] = useState<Record<string, boolean | undefined>>({})
  const [guide, setGuide] = useState(false)
  const [more, setMore] = useState(false)
  useEffect(() => {
    for (const u of [w.data?.lan, w.data?.public]) if (u) void reachable(u, 2500).then((r) => setOk((o) => ({ ...o, [u]: r })))
  }, [w.data?.lan, w.data?.public])
  const d = w.data
  if (!d) return null
  const row = (icon: React.ReactNode, label: string, url: string, extra?: React.ReactNode) => (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line py-2.5 first:border-t-0" data-access-row={label}>
      <span className="flex w-24 items-center gap-1.5 text-[12.5px] text-mute">{icon}{label}</span>
      {url ? <a href={url} className="num text-[13.5px] font-semibold text-acc hover:underline">{url}</a> : <span className="text-[13px] text-mute">{t('access.not_set')}</span>}
      {url && ok[url] === true && <span className="inline-flex items-center gap-1 text-[12px] text-ok"><Check className="size-3.5" />{t('access.opens_here')}</span>}
      {url && ok[url] === false && <span className="inline-flex items-center gap-1 text-[12px] text-warn"><TriangleAlert className="size-3.5" />{t('access.not_here')}</span>}
      {url && <button type="button" aria-label={t('common.copy')} className="text-mute hover:text-fg" onClick={() => void navigator.clipboard?.writeText(url)}><Copy className="size-3.5" /></button>}
      <span className="ml-auto flex gap-2">{extra}</span>
    </div>
  )
  return (
    <div className="pb-2" data-access-card>
      {row(<House className="size-3.5" />, t('access.lan'), d.lan, <Button size="sm" onClick={() => setGuide(true)}>{ok[d.lan] === false ? t('access.fix_device') : t('access.rename')}</Button>)}
      {row(<Globe className="size-3.5" />, t('access.public'), d.public)}
      <div className="pl-[6.75rem]">{publicField}</div>
      <button type="button" className="mt-1 flex items-center gap-1 text-[12px] text-mute hover:text-fg" onClick={() => setMore(!more)}>
        <ChevronDown className={cx('size-3.5 transition-transform', more && 'rotate-180')} />{t('access.fallback')}
      </button>
      {more && <p className="mt-1 text-[12.5px] text-mute"><span className="num text-fg">{d.fallback}</span> — {t('access.fallback_d')}</p>}
      <p className="mt-2 text-[12px] text-mute">{t('access.one_name')}</p>
      {guide && <LocalNameDialog onClose={() => { setGuide(false); void w.reload() }} />}
    </div>
  )
}
