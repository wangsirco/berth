import { Search } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Pkg { name: string; version: string; summary?: string; installed?: string }

// Tools people reach for on a fresh server.
const COMMON = ['htop', 'btop', 'vim', 'nano', 'git', 'wget', 'curl', 'unzip', 'p7zip', 'tmux', 'screen', 'ncdu', 'iotop', 'iftop', 'nethogs', 'rsync', 'tree', 'jq', 'lsof', 'mtr', 'bind-utils', 'traceroute', 'telnet', 'nmap-ncat', 'fail2ban', 'ffmpeg-free', 'ImageMagick', 'sqlite']

// System packages: search the repositories, install or remove, and see what was installed by hand.
export function Packages() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const [q, setQ] = useState('')
  const [term, setTerm] = useState('')
  useEffect(() => { const id = setTimeout(() => setTerm(q.trim()), 350); return () => clearTimeout(id) }, [q])
  const found = useApi<{ items: Pkg[] }>(term.length >= 2 ? `/api/v1/hosts/${host}/packages?q=${encodeURIComponent(term)}` : null, [host, term])
  const mine = useApi<{ items: Pkg[] }>(`/api/v1/hosts/${host}/packages?user=1`, [host])
  useEvents(['change.done'], () => { void mine.reload(); if (term.length >= 2) void found.reload() })
  const [filter, setFilter] = useState('')
  const act = (p: Pkg) => manage && (p.installed
    ? <Button size="sm" variant="ghost" className="text-bad" onClick={() => run('pkg.remove', { names: [p.name] }, { host })}>{t('action.pkg.remove')}</Button>
    : <Button size="sm" onClick={() => run('pkg.install', { names: [p.name] }, { host })}>{t('action.pkg.install')}</Button>)
  const row = (p: Pkg, pill = true) => (
    <div key={p.name} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2"><span className="num font-semibold">{p.name}</span>{pill && p.installed && <Pill tone="ok">{t('pkg.installed')}</Pill>}</div>
        {p.summary && <div className="truncate text-[12px] text-mute">{p.summary}</div>}
      </div>
      <span className="num w-44 truncate text-right text-mute max-sm:hidden">{p.installed || p.version}</span>
      {act(p)}
    </div>
  )
  const mineList = (mine.data?.items ?? []).filter((p) => !filter || p.name.includes(filter.toLowerCase()))
  return (
    <div className="space-y-3">
      <Box title={t('pkg.search_title')} right={<span className="text-[12px] text-mute">{t('pkg.search_hint')}</span>}>
        <div className="flex items-center gap-2 px-4 py-2.5">
          <Search className="size-4 text-mute" />
          <Input className="flex-1" value={q} placeholder={t('pkg.search_ph')} onChange={(e) => setQ(e.target.value)} aria-label={t('pkg.search_title')} />
        </div>
        <div className="flex flex-wrap gap-1.5 border-b border-line px-4 pb-2.5">
          <span className="text-[12px] text-mute">{t('pkg.common')}</span>
          {COMMON.map((c) => <button key={c} className="num rounded-full border border-line px-2 py-0.5 text-[11.5px] hover:border-acc" onClick={() => setQ(c)}>{c}</button>)}
        </div>
        {found.error && <div className="p-3"><Notice tone="warn">{found.error.message}</Notice></div>}
        {term.length >= 2 && found.loading && !found.data && <p className="px-4 py-3 text-[12.5px] text-mute">{t('pkg.searching')}</p>}
        {term.length >= 2 && found.data && found.data.items.length === 0 && <Empty title={t('pkg.none', { q: term })} />}
        {term.length >= 2 && found.data && <div className="max-h-[50vh] overflow-auto">{found.data.items.map((p) => row(p))}</div>}
      </Box>
      <Box title={t('pkg.mine')} count={mine.data?.items.length} right={<Input className="h-7 w-40" placeholder={t('pkg.filter')} value={filter} onChange={(e) => setFilter(e.target.value)} />}>
        <p className="border-b border-line px-4 py-2 text-[12px] text-mute">{t('pkg.mine_hint')}</p>
        {mine.error && <div className="p-3"><Notice tone="warn">{mine.error.message}</Notice></div>}
        <div className="max-h-[50vh] overflow-auto">{mineList.map((p) => row(p, false))}</div>
      </Box>
    </div>
  )
}
