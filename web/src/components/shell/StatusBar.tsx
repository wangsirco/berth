import { useState } from 'react'
import { useApi, useInterval, useMedia } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { Activity } from 'lucide-react'
import { cx } from '../ui/cx'
import { ago } from '@/lib/format'
import { hostPath, useHost } from './host'
import { Link } from 'react-router-dom'
import { can, useAuth } from '@/lib/auth'
import type { UpdateStatus } from '@/pages/settings/Updates'

interface SB { online: boolean; queue: { running: number; waiting: number }; pending: number; wharf: { name: string; state: string; last_seen: number } | null; public_ip: null; last_backup: { at: number; status: string } | null; version: string }

export function StatusBar({ onTasks, onPending }: { onTasks: () => void; onPending: () => void }) {
  const t = useT()
  const { host } = useHost()
  const sb = useApi<SB>(`/api/v1/statusbar?host=${encodeURIComponent(host)}`, [host])
  useInterval(sb.reload, 15000)
  useEvents(['task.update', 'change.pending', 'change.done', 'backup.run'], () => void sb.reload())
  const { user } = useAuth()
  const upd = useApi<UpdateStatus>(can(user, 'settings.manage') ? '/api/v1/update/status' : null).data
  const phone = useMedia('(max-width: 640px)')
  const [open, setOpen] = useState(false)
  const d = sb.data
  const items = d && (
    <>
      <span className={cx('inline-flex items-center gap-1.5', d.online ? 'text-ok' : 'text-bad')}>● {d.online ? t('status.online') : t('status.offline')}</span>
      <button onClick={onTasks} className="hover:text-fg">{t('status.queue')} <b className="font-medium text-fg">{t('status.running', { n: d.queue.running })}</b> · {t('status.waiting', { n: d.queue.waiting })}</button>
      {d.pending > 0 && <button onClick={onPending} className="font-medium text-warn">{t('status.pending', { n: d.pending })}</button>}
      <span>Wharf <b className={cx('font-medium', d.wharf?.state === 'broken' ? 'text-bad' : 'text-fg')}>{d.wharf ? d.wharf.name + (d.wharf.state === 'broken' ? ' · ' + t('wharf.broken_short') : '') : t('common.not_configured')}</b></span>
      <span>{t('status.public_ip')} <b className="font-medium text-fg">{t('common.not_configured')}</b></span>
      <span>{t('status.last_backup')} <b className={cx('font-medium', d.last_backup?.status === 'failed' ? 'text-bad' : 'text-fg')}>
        {d.last_backup ? ago(d.last_backup.at, t) + (d.last_backup.status === 'failed' ? ' · ' + t('backup.st_failed') : d.last_backup.status === 'running' ? ' · ' + t('backup.st_running') : '') : t('common.none')}</b></span>
      <span className="num sm:ml-auto">v{d.version}</span>
      {upd?.last?.available && <Link to={hostPath('local', 'settings/updates')} className="font-medium text-acc hover:underline">{t('upd.badge', { v: upd.last.latest })}</Link>}
    </>
  )
  if (phone) {
    return (
      <footer className="fixed bottom-0 right-0 z-30 p-2">
        <button aria-label={t('status.title')} onClick={() => setOpen(!open)} className="flex size-9 items-center justify-center rounded-full border border-line bg-soft">
          <Activity className={cx('size-4', d?.online ? 'text-ok' : 'text-bad')} />
        </button>
        {open && <div className="absolute bottom-12 right-2 flex w-64 flex-col gap-2 rounded-[var(--radius-box)] border border-line bg-canvas p-3 text-[12px] text-mute shadow-lg">{items}</div>}
      </footer>
    )
  }
  return <footer className="fixed inset-x-0 bottom-0 z-30 flex h-[29px] items-center gap-4 border-t border-line bg-soft px-6 text-[12px] text-mute">{items}</footer>
}
