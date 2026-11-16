import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select, Switch } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import type { Tuning } from './Kernel'

interface Upd { name: string; version: string; repo?: string; security: boolean; kernel: boolean }

export function Updates() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<{ items: Upd[]; checked_at: number; error?: string }>(`/api/v1/hosts/${host}/system/updates`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [checking, setChecking] = useState(false)
  const refresh = async () => { setChecking(true); try { await get(`/api/v1/hosts/${host}/system/updates?refresh=1`); await d.reload() } finally { setChecking(false) } }
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('system.checking_updates')}</div>
  const items = [...d.data.items].sort((a, b) => Number(b.security) - Number(a.security) || Number(b.kernel) - Number(a.kernel) || a.name.localeCompare(b.name))
  const sec = items.filter((i) => i.security).length
  const kernel = items.some((i) => i.kernel)
  return (
    <div className="space-y-3">
      {d.data.error && <Notice tone="warn">{d.data.error}</Notice>}
      <AutoUpdates manage={manage} />
      <Box title={t('system.tab_updates')} count={items.length} right={
        <div className="flex items-center gap-2">
          <span>{d.data.checked_at ? t('system.checked', { ago: ago(d.data.checked_at, t) }) : ''}</span>
          <Button size="sm" variant="ghost" disabled={checking} onClick={() => void refresh()}>{checking ? t('system.checking_updates') : t('system.check_now')}</Button>
          {manage && sec > 0 && <Button size="sm" onClick={() => run('system.updates', { security_only: true }, { host })}>{t('system.install_security', { n: sec })}</Button>}
          {manage && items.length > 0 && <Button size="sm" variant="primary" onClick={() => run('system.updates', { security_only: false }, { host })}>{t('system.install_all')}</Button>}
        </div>}>
        {items.length === 0 && <Empty title={t('system.up_to_date')} />}
        {kernel && <div className="border-b border-line px-4 py-2 text-[12.5px] text-warn">{t('system.kernel_note')}</div>}
        <div className="max-h-[60vh] overflow-auto">
          {items.map((u) => (
            <div key={u.name} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0">
              <span className="num min-w-0 flex-1 truncate">{u.name}</span>
              {u.security && <Pill tone="bad">{t('system.security')}</Pill>}
              {u.kernel && <Pill tone="warn">{t('system.kernel_pkg')}</Pill>}
              <span className="num w-48 truncate text-right text-mute">{u.version}</span>
              <span className="w-32 truncate text-right text-[11.5px] text-mute max-sm:hidden">{u.repo}</span>
            </div>
          ))}
        </div>
      </Box>
    </div>
  )
}

// Automatic updates in a nightly window (dnf5-automatic / unattended-upgrades).
function AutoUpdates({ manage }: { manage: boolean }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const d = useApi<Tuning>(`/api/v1/hosts/${host}/system/tuning`, [host])
  useEvents(['change.done'], () => void d.reload())
  const au = d.data?.auto_update
  const [type, setType] = useState<'security' | 'all'>('security')
  const [time, setTime] = useState('04:00')
  useEffect(() => { if (au) { setType(au.type || 'security'); setTime(au.time || '04:00') } }, [au])
  if (!au || !au.family) return null
  const on = au.installed && au.enabled
  const dirty = on && (type !== au.type || time !== (au.time || '04:00'))
  return (
    <Box>
      <div className="flex flex-wrap items-center gap-3 px-4 py-2.5 text-[13px]">
        <Switch disabled={!manage} checked={on} onChange={(v) => run('system.auto_updates', { enabled: v, type, time }, { host })} label={t('system.auto_updates')} />
        <Select disabled={!manage} value={type} onChange={(e) => setType(e.target.value as 'security' | 'all')}>
          <option value="security">{t('system.auto_security')}</option>
          <option value="all">{t('system.auto_all')}</option>
        </Select>
        <span className="text-mute">{t('system.auto_from')}</span>
        <Input disabled={!manage} type="time" className="w-28" value={time} onChange={(e) => setTime(e.target.value)} />
        {dirty && manage && <Button size="sm" variant="primary" onClick={() => run('system.auto_updates', { enabled: true, type, time }, { host })}>{t('common.apply')}</Button>}
        <span className="ml-auto text-[12px] text-mute">{t('system.auto_hint')}</span>
      </div>
    </Box>
  )
}
