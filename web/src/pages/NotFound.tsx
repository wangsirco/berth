import { ArrowLeft, House, Search } from 'lucide-react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { ALL } from '@/components/shell/nav'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { useT } from '@/lib/i18n'

// A page the panel does not have: say so, and offer the pages that look closest to what was typed.
export default function NotFound() {
  const t = useT()
  const nav = useNavigate()
  const { host } = useHost()
  const path = useLocation().pathname
  const seg = (path.split('/')[2] ?? '').toLowerCase()
  const near = ALL.filter((x) => seg && (x.path.startsWith(seg.slice(0, 3)) || seg.startsWith(x.path.slice(0, 3)) || t('nav.' + x.key).includes(seg))).slice(0, 4)
  const openSearch = () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, ctrlKey: true }))
  return (
    <div className="mx-auto flex max-w-xl flex-col items-center py-16 text-center">
      <div className="num text-[64px] font-bold leading-none text-line">404</div>
      <h1 className="mt-4 text-[20px] font-semibold">{t('notfound.title')}</h1>
      <p className="mt-2 text-[13.5px] text-mute">{t('notfound.body')}</p>
      <p className="num mt-2 break-all rounded-lg bg-soft px-3 py-1 text-[12px] text-mute">{path}</p>
      <div className="mt-6 flex flex-wrap justify-center gap-2">
        <Link to={hostPath(host, 'overview')}><Button variant="primary"><House className="size-4" />{t('notfound.home')}</Button></Link>
        <Button onClick={() => nav(-1)}><ArrowLeft className="size-4" />{t('notfound.back')}</Button>
        <Button variant="ghost" onClick={openSearch}><Search className="size-4" />{t('notfound.search')}</Button>
      </div>
      {near.length > 0 && (
        <div className="mt-8 w-full">
          <p className="mb-2 text-[12.5px] text-mute">{t('notfound.maybe')}</p>
          <div className="flex flex-wrap justify-center gap-2">
            {near.map((x) => <Link key={x.key} to={hostPath(host, x.path)} className="rounded-full border border-line px-3 py-1 text-[13px] hover:border-acc hover:text-acc">{t('nav.' + x.key)}</Link>)}
          </div>
        </div>
      )}
    </div>
  )
}
