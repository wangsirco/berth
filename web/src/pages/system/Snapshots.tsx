import { Camera, RotateCcw } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { can, useAuth } from '@/lib/auth'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Snap { id: string; reason: string; kernel: string; created: number; auto: boolean; boot: boolean }
interface Status { supported: boolean; why?: string; root_sub?: string; snapshots: Snap[]; pending?: string; previous: string[] }

// System snapshots (btrfs): taken before updates and package changes; roll back at the next boot.
export function Snapshots() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<Status>(`/api/v1/hosts/${host}/system/snapshots`, [host])
  useEvents(['change.done'], () => void d.reload())
  const info = useApi<{ hostname: string }>(`/api/v1/hosts/${host}/system/info`, [host])
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <p className="p-6 text-[13px] text-mute">{t('common.loading')}</p>
  const st = d.data
  if (!st.supported) return <Notice tone="warn">{t('snapui.unsupported', { why: t(st.why ?? 'snap.not_btrfs') })}</Notice>
  return (
    <div className="space-y-4">
      <Notice>{t('snapui.intro')}</Notice>
      {st.pending && (
        <Notice tone="warn">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex-1">{t('snapui.pending', { id: st.pending })}</span>
            {manage && <>
              <Button size="sm" variant="primary" onClick={() => run('system.power', { action: 'reboot', minutes: 0 }, { host, confirmName: info.data?.hostname })}>{t('system.reboot_now')}</Button>
              <Button size="sm" onClick={() => run('system.snapshot.undo', {}, { host })}>{t('action.system.snapshot.undo')}</Button>
            </>}
          </div>
        </Notice>
      )}
      <Box title={t('snapui.title')} count={st.snapshots.length}
        right={manage && <Button size="sm" variant="primary" onClick={() => run('system.snapshot.create', { reason: t('snapui.by_hand') }, { host })}><Camera className="size-3.5" />{t('action.system.snapshot.create')}</Button>}>
        {st.snapshots.length === 0 && <Empty title={t('snapui.none')}>{t('snapui.none_hint')}</Empty>}
        {st.snapshots.map((s) => (
          <div key={s.id} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2"><span className="num font-semibold">{datetime(s.created)}</span>
                {s.auto ? <Pill>{t('snapui.auto')}</Pill> : <Pill tone="acc">{t('snapui.manual')}</Pill>}
                {st.pending === s.id && <Pill tone="warn">{t('snapui.after_reboot')}</Pill>}</div>
              <div className="text-[12px] text-mute">{s.reason || '—'} · {t('snapui.kernel', { k: s.kernel })}{s.boot ? ' · ' + t('snapui.with_boot') : ''}</div>
            </div>
            {manage && !st.pending && <Button size="sm" onClick={() => run('system.snapshot.rollback', { id: s.id }, { host, confirmName: info.data?.hostname })}><RotateCcw className="size-3.5" />{t('action.system.snapshot.rollback')}</Button>}
            {manage && st.pending !== s.id && <Button size="sm" variant="ghost" className="text-bad" onClick={() => run('system.snapshot.delete', { id: s.id }, { host })}>{t('action.system.snapshot.delete')}</Button>}
          </div>
        ))}
      </Box>
      {st.previous.length > 0 && (
        <Box title={t('snapui.previous')}>
          <p className="border-b border-line px-4 py-2 text-[12px] text-mute">{t('snapui.previous_hint')}</p>
          {st.previous.map((p) => (
            <div key={p} className="flex items-center gap-3 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0">
              <span className="num flex-1">{p}</span>
              {manage && <Button size="sm" variant="ghost" className="text-bad" onClick={() => run('system.snapshot.delete_previous', { name: p }, { host })}>{t('action.system.snapshot.delete_previous')}</Button>}
            </div>
          ))}
        </Box>
      )}
    </div>
  )
}
