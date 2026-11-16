import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Button } from '@/components/ui/Button'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { can, useAuth } from '@/lib/auth'

// A firewall change waits for confirmation; without it the agent restores the old rules.
export function FirewallPending({ host }: { host: string }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const allowed = can(user, 'network.view')
  const fw = useApi<{ state: { pending: boolean; rollback_at?: number } }>(allowed ? `/api/v1/hosts/${host}/firewall` : null, [host])
  const [until, setUntil] = useState(0)
  const [now, setNow] = useState(() => Date.now() / 1000)
  useEffect(() => { setUntil(fw.data?.state?.pending ? fw.data.state.rollback_at ?? 0 : 0) }, [fw.data])
  useEvents(['firewall.pending'], (_t, d: { host: string; rollback_at: number }) => { if (d.host === host) setUntil(d.rollback_at) })
  useEffect(() => {
    if (!until) return
    const id = setInterval(() => setNow(Date.now() / 1000), 1000)
    return () => clearInterval(id)
  }, [until])
  const left = Math.ceil(until - now)
  useEffect(() => { if (until && left <= -2) { setUntil(0); void fw.reload() } }, [left, until, fw])
  if (!until || left < -1) return null
  return (
    <div role="alert" className="sticky top-0 z-30 flex flex-wrap items-center gap-2 border-b border-warn bg-warn/15 px-4 py-2 text-[13px]">
      <b>{t('firewall.pending_title')}</b>
      <span className="text-mute">{t('firewall.pending_hint', { n: Math.max(left, 0) })}</span>
      {can(user, 'network.manage') && <span className="ml-auto flex gap-2">
        <Button size="sm" onClick={() => run('firewall.rollback', {}, { host, onDone: () => setUntil(0) })}>{t('action.firewall.rollback')}</Button>
        <Button size="sm" variant="primary" onClick={() => run('firewall.confirm', {}, { host, onDone: () => setUntil(0) })}>{t('action.firewall.confirm')}</Button>
      </span>}
    </div>
  )
}
