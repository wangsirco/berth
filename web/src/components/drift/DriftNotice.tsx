import { useChange } from '@/components/change/ChangeDialog'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { can, useAuth } from '@/lib/auth'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Drift { path: string; kind: string; object: string; state: 'changed' | 'missing' | 'appeared'; since: number }

// Files Berth manages that someone changed outside the panel (方案 §4.4). Shown where the
// object lives (a compose project, the system page); each offers the diff, then adopting
// the file as it is or putting Berth's version back.
export function DriftNotice({ host, kind, object }: { host: string; kind?: string; object?: string }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const d = useApi<{ items: Drift[] }>(can(user, 'system.view') ? `/api/v1/hosts/${host}/drift` : null, [host])
  useEvents(['drift', 'change.done'], () => void d.reload())
  const items = (d.data?.items ?? []).filter((x) => (!kind || x.kind === kind) && (!object || x.object === object))
  if (items.length === 0) return null
  const manage = can(user, 'system.manage')
  return (
    <Notice tone="warn">
      <div className="space-y-1.5">
        <b>{t('drift.title', { n: items.length })}</b>
        {items.map((x) => (
          <div key={x.path} className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <span className="num min-w-0 flex-1 truncate" title={x.path}>{x.path}</span>
            <span className="text-mute">{t('drift.state_' + x.state)} · {datetime(x.since)}</span>
            {manage && <>
              <Button size="sm" onClick={() => run('drift.accept', { path: x.path }, { host })}>{t('drift.accept')}</Button>
              <Button size="sm" onClick={() => run('drift.restore', { path: x.path }, { host })}>{t('drift.restore')}</Button>
            </>}
          </div>
        ))}
        <div className="text-[12px] text-mute">{t('drift.hint')}</div>
      </div>
    </Notice>
  )
}
