import { Menu as MenuIcon } from 'lucide-react'
import { lazy, Suspense, useEffect, useState } from 'react'
import { Navigate, Outlet, useNavigate, useParams } from 'react-router-dom'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Container, HostInfo } from '@/lib/types'
import { AiDrawer } from '@/components/ai/AiDrawer'
import { ChangeProvider, setOpenTasks } from '../change/ChangeDialog'
import { Empty } from '../ui/Box'
import { Button } from '../ui/Button'
import { Drawer } from '../ui/Dialog'
import { CommandPalette } from './CommandPalette'
import { HostCtx, hostPath } from './host'
import { StatusBar } from './StatusBar'
import { FirewallPending } from './FirewallPending'
import { TaskDrawer } from './TaskDrawer'
import { Sidebar } from './Sidebar'
import { onRequest as onTerminalRequest } from '@/pages/terminal/store'
import { PageTitle } from './PageTitle'

const TerminalPanel = lazy(() => import('@/pages/terminal/TerminalPanel'))

function typing(e: KeyboardEvent) {
  const el = e.target
  if (!(el instanceof HTMLElement)) return false
  return (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) || !!el.closest('.xterm, .cm-editor'))
}

export function Shell() {
  const t = useT()
  const nav = useNavigate()
  const { user } = useAuth()
  const { host = 'local' } = useParams()
  const hosts = useApi<{ items: HostInfo[]; name: string }>('/api/v1/hosts')
  const ctrs = useApi<{ items: Container[] }>(can(user, 'containers.view') ? `/api/v1/hosts/${encodeURIComponent(host)}/containers` : null, [host])
  const [palette, setPalette] = useState(false)
  const [term, setTerm] = useState(false)
  const [tasks, setTasks] = useState(false)
  const [menu, setMenu] = useState(false)
  const [focusTask, setFocusTask] = useState<string>()
  useEffect(() => setOpenTasks((id) => { setFocusTask(id); setTasks(true) }), [])
  useEffect(() => onTerminalRequest(() => setTerm(true)), [])
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette((p) => !p) }
      else if (e.key === '`' && !e.metaKey && !e.ctrlKey && !e.altKey && !typing(e) && can(user, 'terminal.use')) { e.preventDefault(); setTerm((v) => !v) }
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [user])
  const counts: Record<string, number> = {}
  if (ctrs.data) counts.containers = ctrs.data.items.length
  const side = { panelName: hosts.data?.name ?? '', counts, onSearch: () => { setMenu(false); setPalette(true) }, onTerminal: () => { setMenu(false); setTerm((v) => !v) }, onTasks: (id?: string) => { setMenu(false); setFocusTask(id); setTasks(true) } }
  return (
    <HostCtx.Provider value={{ host, hosts: hosts.data?.items ?? [{ name: host, online: true, last_seen: 0 }] }}>
      <ChangeProvider host={host}>
        <div className="flex min-h-full pb-[29px]">
          <aside className="sticky top-0 hidden h-[calc(100vh-29px)] w-[244px] shrink-0 border-r border-line bg-soft lg:flex">
            <Sidebar {...side} />
          </aside>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 border-b border-line bg-soft px-3 py-2 lg:hidden">
              <button onClick={() => setMenu(true)} aria-label={t('shell.menu')} className="rounded-[9px] p-1.5 hover:bg-canvas"><MenuIcon className="size-5" /></button>
              <span className="font-semibold">{host}</span>
            </div>
            <main className="mx-auto w-full max-w-[1280px] px-3 py-4 sm:px-8 sm:py-6">{host !== 'local' && !hosts.data?.items.some((h) => h.name === host)
              ? hosts.data && <Empty title={t('shell.no_host', { host })} action={<Button onClick={() => nav('/local/overview')}>{t('shell.to_local')}</Button>} />
              : <><FirewallPending host={host} />{user?.needs_2fa && <div role="alert" className="mb-4 flex flex-wrap items-center gap-2 rounded-[var(--radius-box)] border border-bad bg-bad/10 px-3.5 py-2.5 text-[13px]"><b>{t('pub.banner')}</b><span className="text-mute">{t('pub.banner_d')}</span><Button size="sm" variant="primary" className="ml-auto" onClick={() => nav('/account')}>{t('pub.banner_btn')}</Button></div>}<PageTitle /><Outlet /></>}</main>
          </div>
        </div>
        <Drawer open={menu} onOpenChange={setMenu} side="left" title={t('shell.nav')} className="bg-soft">
          <Sidebar {...side} onNavigate={() => setMenu(false)} />
        </Drawer>
        <StatusBar onTasks={() => setTasks(true)} onPending={() => nav(hostPath(host, 'audit?status=pending_approval'))} />
        <TaskDrawer open={tasks} onOpenChange={setTasks} focus={focusTask} />
        <CommandPalette open={palette} onOpenChange={setPalette} />
        <AiDrawer />
        <Drawer open={term} onOpenChange={setTerm} side="bottom" title={t('shell.terminal')}>
          {term && <Suspense fallback={<div className="p-4 text-mute">{t('common.loading')}</div>}><TerminalPanel compact /></Suspense>}
        </Drawer>
      </ChangeProvider>
    </HostCtx.Provider>
  )
}

export function RequireAuth({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth()
  if (loading) return null
  if (!user) return <Navigate to="/login" replace />
  if (user.simple && !['/wall', '/account', '/screen'].some((p) => location.pathname.startsWith(p))) return <Navigate to="/wall" replace />
  return <>{children}</>
}
