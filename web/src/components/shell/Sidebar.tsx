import {
  Activity, ArchiveRestore, AtSign, CalendarClock, Cloud, Rocket, Bell, Boxes, ChevronsUpDown, Code2, Database, Folder, GitBranch, Globe, House, KeyRound, LogOut, Monitor, MonitorSmartphone,
  Moon, Puzzle, ScrollText, Search, Server, ServerCog, Settings, ShieldCheck, Sparkles, SquareTerminal, Store, Sun, UserRound, Users, Waypoints, type LucideIcon,
} from 'lucide-react'
import { WharfMark } from '@/components/brand/WharfMark'
import { TaskButton } from './TaskButton'
import { useTokenActivity } from '@/components/ai/Activity'
import { NavLink, useNavigate } from 'react-router-dom'
import { askAI, useAIReady } from '@/components/ai/AiDrawer'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { LOCALES, useLocale, useT } from '@/lib/i18n'
import { useTheme } from '@/lib/theme'
import { extName, type Ext } from '@/pages/extensions/ExtensionsPage'
import { BerthLockup } from '@/components/brand/BerthMark'
import { SignalFlag } from '../flag/FlagIcon'
import { Kbd } from '../ui/Box'
import { Menu } from '../ui/Menu'
import { cx } from '../ui/cx'
import { hostPath, useHost } from './host'
import { navGroups } from './nav'

export const ICONS: Record<string, LucideIcon> = {
  overview: House, sites: Globe, projects: Rocket, domains: AtSign, containers: Boxes, store: Store, vms: Monitor, db: Database, files: Folder,
  pipeline: GitBranch, dev: Code2, remote: MonitorSmartphone, backup: ArchiveRestore, network: ShieldCheck, monitor: Bell,
  audit: ScrollText, system: Server, hosts: ServerCog, extensions: Puzzle, plugins: Puzzle, cron: CalendarClock, cloudflare: Cloud, proxy: Waypoints, wharf: WharfMark as unknown as LucideIcon, users: Users, tokens: KeyRound, settings: Settings,
}

// names that read better in the sidebar than the page titles do
export const SIDE = new Set(['overview', 'containers', 'network', 'monitor', 'users', 'audit', 'pipeline', 'dev'])

// Sidebar: host, search, the grouped pages and the account. Used fixed on wide screens and in a drawer on narrow ones.
export function Sidebar({ onSearch, onTerminal, onTasks, counts, panelName, onNavigate }: {
  onSearch: () => void; onTerminal: () => void; onTasks: (changeId?: string) => void; counts: Record<string, number>; panelName: string; onNavigate?: () => void
}) {
  const t = useT()
  const nav = useNavigate()
  const { user, logout } = useAuth()
  const { host, hosts } = useHost()
  const { theme, setTheme } = useTheme()
  const [locale, setLocale] = useLocale()
  const aiReady = useAIReady()
  const aiWorking = (useTokenActivity().data?.items ?? []).some((x) => x.working)
  const info = hosts.find((h) => h.name === host)
  const exts = useApi<{ items: Ext[] }>(can(user, 'extensions.use') ? '/api/v1/extensions' : null)
  const groups = navGroups((perm) => !perm || can(user, perm))
  const go = (p: string) => { nav(p); onNavigate?.() }
  const sys = info?.caps ? [info.caps.OS && `${info.caps.OS} ${info.caps.OSVersion}`, info.caps.Arch].filter(Boolean).join(' · ') : ''
  const link = (key: string, path: string, label: string, Icon: LucideIcon) => (
    <NavLink key={key} to={hostPath(host, path)} onClick={onNavigate}
      className={({ isActive }) => cx('flex items-center gap-2.5 rounded-[9px] px-2.5 py-[7px] text-[14px]',
        isActive ? 'bg-card font-semibold shadow-[0_0_0_1px_var(--line)] [&>svg]:text-acc' : 'hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)] [&>svg]:text-mute')}>
      <Icon className="size-[17px] shrink-0" strokeWidth={1.75} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {key === 'tokens' && aiWorking && <span className="relative inline-flex size-2" title={t('ai.someone_working')}><span className="absolute inline-flex size-full animate-ping rounded-full bg-acc opacity-60" /><span className="relative inline-flex size-2 rounded-full bg-acc" /></span>}
      {counts[key] !== undefined && <span className="num text-[12px] text-mute">{counts[key]}</span>}
    </NavLink>
  )
  return (
    <div className="flex h-full w-full flex-col px-3 py-4">
      <div className="flex items-center gap-2.5 px-2 pb-3.5">
        <BerthLockup size={24} />
        {panelName && panelName !== 'berth' && <span className="min-w-0 truncate text-[12.5px] text-mute" title={panelName}>{panelName}</span>}
      </div>
      <Menu align="start" trigger={
        <button className="mb-2 flex w-full items-center gap-2.5 rounded-xl border border-line bg-card px-2.5 py-2 text-left" aria-label={t('shell.switch_host')}>
          <span className={cx('size-2 shrink-0 rounded-full', info?.online === false ? 'bg-mute' : 'bg-ok')} />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[13.5px] font-semibold leading-tight">{host}</span>
            {sys && <span className="block truncate text-[12px] text-mute">{sys}</span>}
          </span>
          <ChevronsUpDown className="size-4 shrink-0 text-mute" />
        </button>}
        items={hosts.map((h) => ({ label: <span className="inline-flex items-center gap-2"><SignalFlag name={h.name} size={14} />{h.name}{!h.online && <span className="text-xs text-mute">{t('shell.offline')}</span>}</span>, onClick: () => go(hostPath(h.name, 'overview')) }))} />
      <button onClick={onSearch} className="mb-1 flex items-center gap-2 rounded-[9px] px-2.5 py-[7px] text-[13px] text-mute hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)]">
        <Search className="size-4" /><span className="flex-1 text-left">{t('shell.search')}</span><Kbd>⌘K</Kbd>
      </button>
      <TaskButton onOpen={onTasks} />
      <nav aria-label={t('shell.nav')} className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
        {groups.map((g) => (
          <div key={g.key}>
            {g.key !== 'home' && <div className="px-2.5 pb-1 pt-3.5 text-[11.5px] tracking-wide text-mute">{t('navgrp.' + g.key)}</div>}
            {g.items.map((x) => link(x.key, x.path, t((SIDE.has(x.key) ? 'side.' : 'nav.') + x.key), ICONS[x.key] ?? Activity))}
            {g.key === 'server' && (exts.data?.items ?? []).map((e) => link('ext-' + e.id, 'ext/' + e.id, extName(e, locale), Puzzle))}
          </div>
        ))}
      </nav>
      <div className="mt-2 flex items-center gap-1.5 border-t border-line pt-2.5">
        <Menu align="start" trigger={
          <button className="flex min-w-0 flex-1 items-center gap-2 rounded-[9px] px-1.5 py-1 text-left hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)]" aria-label={t('shell.account')}>
            <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-full bg-tint text-[12px] font-bold text-acc">{(user?.display_name || user?.username || '?').slice(0, 1).toUpperCase()}</span>
            <span className="truncate text-[13px]">{user?.display_name || user?.username}</span>
          </button>}
          items={[
            { label: <><UserRound className="size-4" />{t('shell.account')}</>, onClick: () => go('/account') },
            'sep',
            { label: <><Sun className="size-4" />{t('theme.light')}{theme === 'light' && ' ✓'}</>, onClick: () => setTheme('light') },
            { label: <><Moon className="size-4" />{t('theme.dark')}{theme === 'dark' && ' ✓'}</>, onClick: () => setTheme('dark') },
            { label: <><Monitor className="size-4" />{t('theme.system')}{theme === 'system' && ' ✓'}</>, onClick: () => setTheme('system') },
            'sep',
            ...LOCALES.map((l) => ({ label: <>{t('locale.' + l)}{locale === l && ' ✓'}</>, onClick: () => setLocale(l) })),
            'sep',
            { label: <><LogOut className="size-4" />{t('shell.logout')}</>, onClick: () => void logout() },
          ]} />
        {aiReady && <button onClick={() => askAI('', '')} aria-label={t('ai.title')} className="rounded-[9px] p-1.5 text-mute hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)] hover:text-fg"><Sparkles className="size-4" /></button>}
        {can(user, 'terminal.use') && <button onClick={onTerminal} aria-label={t('shell.terminal')} title={t('shell.terminal') + ' (`)'} className="rounded-[9px] p-1.5 text-mute hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)] hover:text-fg"><SquareTerminal className="size-4" /></button>}
      </div>
    </div>
  )
}
