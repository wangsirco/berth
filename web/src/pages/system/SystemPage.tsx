import { useState } from 'react'
import { Navigate } from 'react-router-dom'
import { HostDashboard } from '../overview/HostDashboard'
import { Packages } from './Packages'
import { Snapshots } from './Snapshots'
import { DriftNotice } from '@/components/drift/DriftNotice'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select, Switch } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes, datetime, duration } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { Nics } from './Nics'
import { Processes, Services } from './Services'
import { Updates } from './Disks'
import { Disks } from './Storage'
import { Access } from './Access'
import { Kernel } from './Kernel'
import { TabBar } from '@/components/ui/Tabs'

export interface SysInfo {
  hostname: string; os: string; kernel: string; arch: string; model?: string; cpu?: string; cores: number; mem_total: number; page_size: number
  virt?: string; boot_time: number; timezone: string; ntp: boolean; ntp_synced: boolean; time: number; need_reboot: boolean; scheduled?: string
  pkg_manager?: string; net_manager?: string
}
type Tab = 'overview' | 'nics' | 'services' | 'processes' | 'access' | 'kernel' | 'disks' | 'updates' | 'packages' | 'snapshots'
const TABS: Tab[] = ['overview', 'nics', 'services', 'processes', 'access', 'kernel', 'disks', 'updates', 'packages', 'snapshots']
// tabs that became pages of their own
const MOVED: Record<string, string> = { jobs: 'cron', migrate: 'backup/migrate' }

// The server itself (方案 §5.13), one page with tabs like the other areas.
export default function SystemPage() {
  const t = useT()
  const [tab, setTab] = useState<Tab>(() => (new URLSearchParams(location.search).get('tab') as Tab) || 'overview')
  const { host } = useHost()
  const go = (x: Tab) => { setTab(x); history.replaceState(null, '', '?tab=' + x) }
  if (MOVED[tab]) return <Navigate to={hostPath(host, MOVED[tab])} replace />
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.system')} value={tab} onChange={go} items={TABS.map((x) => ({ value: x, label: t('system.tab_' + x) }))} />
      {tab === 'overview' && <><DriftNotice host={host} /><div className="mb-6"><HostDashboard /></div><Overview /></>}
      {tab === 'nics' && <Nics />}
      {tab === 'services' && <Services />}
      {tab === 'processes' && <Processes />}
      {tab === 'access' && <Access />}
      {tab === 'kernel' && <Kernel />}
      {tab === 'disks' && <Disks />}
      {tab === 'updates' && <Updates />}
      {tab === 'packages' && <Packages />}
      {tab === 'snapshots' && <Snapshots />}
    </div>
  )
}

function Overview() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<SysInfo>(`/api/v1/hosts/${host}/system/info`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [tzs, setTzs] = useState<string[] | null>(null)
  const [name, setName] = useState<string | null>(null)
  const [delay, setDelay] = useState('0')
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const i = d.data
  const loadTz = () => { if (!tzs) get<string[]>(`/api/v1/hosts/${host}/system/timezones`).then(setTzs).catch(() => setTzs([])) }
  const kv = (k: string, v: React.ReactNode) => <div className="grid grid-cols-[8rem_1fr] gap-3 border-b border-line px-4 py-2 text-[13px] last:border-b-0"><span className="text-mute">{k}</span><span className="min-w-0 break-words">{v}</span></div>
  const [schedAction, schedAt] = (i.scheduled ?? '').split('@')
  return (
    <div className="space-y-4">
      {i.need_reboot && <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span className="flex-1">{t('system.need_reboot')}</span>
        {manage && <Button size="sm" onClick={() => run('system.power', { action: 'reboot', minutes: 0 }, { host, confirmName: i.hostname })}>{t('system.reboot_now')}</Button>}</div></Notice>}
      {i.scheduled && <Notice tone="warn"><div className="flex flex-wrap items-center gap-2"><span className="flex-1">{t('system.scheduled_' + schedAction, { at: datetime(Number(schedAt)) })}</span>
        {manage && <Button size="sm" onClick={() => run('system.power', { action: 'cancel' }, { host })}>{t('system.cancel_power')}</Button>}</div></Notice>}
      <div className="grid gap-4 lg:grid-cols-2">
        <Box title={t('system.machine')}>
          {kv(t('system.hostname'), name === null
            ? <span className="flex items-center gap-2"><b>{i.hostname}</b>{manage && <button className="text-acc hover:underline" onClick={() => setName(i.hostname)}>{t('vms.change')}</button>}</span>
            : <span className="flex gap-2"><Input className="w-48" value={name} onChange={(e) => setName(e.target.value)} />
              <Button size="sm" variant="primary" onClick={() => run('system.hostname', { name }, { host, onDone: () => setName(null) })}>{t('common.save')}</Button>
              <Button size="sm" variant="ghost" onClick={() => setName(null)}>{t('common.cancel')}</Button></span>)}
          {i.model && kv(t('system.model'), i.model)}
          {kv('CPU', <>{i.cpu || '—'} · {t('overview.cores', { n: i.cores })} · {i.arch}</>)}
          {kv(t('overview.memory'), bytes(i.mem_total))}
          {kv(t('system.os'), i.os)}
          {kv(t('system.kernel'), <span className="num">{i.kernel}{i.page_size !== 4096 && <span className="ml-2 text-mute">{t('system.page_size', { kb: i.page_size / 1024 })}</span>}</span>)}
          {i.virt && i.virt !== 'none' && kv(t('system.virt'), i.virt)}
          {kv(t('system.booted'), <>{datetime(i.boot_time)} <span className="text-mute">· {t('system.up_for', { d: duration(i.time - i.boot_time, t) })}</span></>)}
          {kv(t('system.managers'), [i.pkg_manager, i.net_manager].filter(Boolean).join(' · ') || '—')}
        </Box>
        <div className="space-y-4">
          <Box title={t('system.time')}>
            {kv(t('system.now'), <span className="num">{datetime(i.time)}</span>)}
            {kv(t('system.timezone'), manage
              ? <Select className="w-64" value={i.timezone} onFocus={loadTz} onMouseDown={loadTz} onChange={(e) => run('system.timezone', { timezone: e.target.value }, { host })}>
                {(tzs ?? [i.timezone]).map((z) => <option key={z}>{z}</option>)}
              </Select>
              : i.timezone)}
            {kv(t('system.ntp'), <span className="flex items-center gap-3">
              {manage ? <Switch checked={i.ntp} onChange={(on) => run('system.ntp', { on }, { host })} label={i.ntp ? t('system.ntp_on') : t('system.ntp_off')} /> : (i.ntp ? t('system.ntp_on') : t('system.ntp_off'))}
              <span className={i.ntp_synced ? 'text-ok' : 'text-warn'}>{i.ntp_synced ? t('system.synced') : t('system.not_synced')}</span></span>)}
          </Box>
          {manage && (
            <Box title={t('system.power')}>
              <div className="flex flex-wrap items-center gap-2 px-4 py-3 text-[13px]">
                <Select className="w-36" value={delay} onChange={(e) => setDelay(e.target.value)}>
                  {['0', '5', '30', '60', '240'].map((m) => <option key={m} value={m}>{m === '0' ? t('system.now_power') : t('system.in_minutes', { n: m })}</option>)}
                </Select>
                <Button onClick={() => run('system.power', { action: 'reboot', minutes: Number(delay) }, { host, confirmName: i.hostname })}>{t('system.reboot')}</Button>
                <Button variant="danger" onClick={() => run('system.power', { action: 'poweroff', minutes: Number(delay) }, { host, confirmName: i.hostname })}>{t('system.poweroff')}</Button>
                <span className="w-full text-[12px] text-mute">{t('system.power_hint')}</span>
              </div>
            </Box>
          )}
        </div>
      </div>
    </div>
  )
}
