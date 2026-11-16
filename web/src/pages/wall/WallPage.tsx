import { useState } from 'react'
import { Globe, LogOut } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'

interface Item { host: string; id: number; name: string; url: string; app_id?: string; names?: Record<string, string> }

// The app wall (方案 §10.4 简要模式): the sites' icons and names, one tap opens them.
// No switches, no settings — for family members who only want to open the apps.
export default function WallPage() {
  const t = useT()
  const [locale] = useLocale()
  const { user, logout } = useAuth()
  const nav = useNavigate()
  const d = useApi<{ items: Item[]; name: string }>('/api/v1/wall')
  return (
    <div className="min-h-screen bg-soft">
      <header className="mx-auto flex max-w-4xl items-center gap-3 px-6 pt-8 pb-4">
        <h1 className="flex-1 text-[20px] font-semibold">{d.data?.name || 'Berth'}</h1>
        <span className="text-[13px] text-mute">{user?.display_name}</span>
        {!user?.simple && <button className="text-[13px] text-acc hover:underline" onClick={() => nav('/local/overview')}>{t('wall.panel')}</button>}
        <button aria-label={t('shell.logout')} title={t('shell.logout')} className="rounded-md p-1.5 text-mute hover:bg-canvas" onClick={() => void logout().then(() => nav('/login'))}><LogOut className="size-4" /></button>
      </header>
      <main className="mx-auto grid max-w-4xl grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-4 px-6 pb-12">
        {d.data?.items.map((it) => <Tile key={it.host + it.id} it={it} name={it.names?.[locale] || it.names?.en || it.name} />)}
        {d.data && d.data.items.length === 0 && <p className="col-span-full py-16 text-center text-[14px] text-mute">{t('wall.empty')}</p>}
      </main>
    </div>
  )
}

function Tile({ it, name }: { it: Item; name: string }) {
  const [broken, setBroken] = useState(false)
  return (
    <a href={it.url} target="_blank" rel="noopener noreferrer" data-wall-tile
      className="flex flex-col items-center gap-2.5 rounded-[var(--radius-box)] border border-line bg-canvas px-3 py-5 text-center hover:border-acc focus-visible:outline-2 focus-visible:outline-acc">
      <span className="grid size-14 place-items-center">
        {broken ? <Globe className="size-10 text-mute" /> : <img src={`/api/v1/wall/icon/${it.host}/${it.id}`} alt="" className="max-h-14 max-w-14" onError={() => setBroken(true)} />}
      </span>
      <span className="w-full truncate text-[13.5px] font-medium">{name}</span>
    </a>
  )
}
