import { Activity } from 'lucide-react'
import { useLocation } from 'react-router-dom'
import { useT } from '@/lib/i18n'
import { ALL } from './nav'
import { ICONS, SIDE } from './Sidebar'

// PageTitle: the name of the page you are on (the same name and icon as in the sidebar) and one
// line on what it is for. The home page has its own greeting instead.
export function PageTitle() {
  const t = useT()
  const loc = useLocation()
  const seg = loc.pathname.split('/')[2] ?? ''
  const item = ALL.find((x) => x.path === seg)
  if (!item || item.key === 'overview') return null
  const Icon = ICONS[item.key] ?? Activity
  const desc = t('pagedesc.' + item.key)
  return (
    <div className="mb-3 flex items-center gap-3">
      <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-tint text-acc"><Icon className="size-[18px]" /></span>
      <div className="min-w-0">
        <h1 className="text-[19px] font-semibold leading-tight">{t((SIDE.has(item.key) ? 'side.' : 'nav.') + item.key)}</h1>
        {/* shown with CSS so it never competes with the page's own text (tab names, buttons) */}
        {desc !== 'pagedesc.' + item.key && <p data-desc={desc} className="truncate text-[12.5px] text-mute after:content-[attr(data-desc)]" />}
      </div>
    </div>
  )
}
