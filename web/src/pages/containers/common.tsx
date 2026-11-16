import { MoreHorizontal } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Menu, type MenuEntry } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { can, useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import type { Container } from '@/lib/types'
import { requestTerminal } from '../terminal/store'

export const stateDot = (s: string): 'ok' | 'warn' | 'bad' | 'off' => (s === 'running' ? 'ok' : s === 'restarting' || s === 'paused' ? 'warn' : s === 'dead' ? 'bad' : 'off')

export function openTerminal(container?: string) {
  requestTerminal({ target: container ? 'container' : 'host', container })
}

// ContainerMenu: the per-row actions, each one a previewed change.
export function ContainerMenu({ c, onDone, onLogs }: { c: Container; onDone: () => void; onLogs?: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const manage = can(user, 'containers.manage')
  const k = (kind: string, extra: object = {}) => () => run(kind, { name: c.name, ...extra }, { host, onDone })
  const running = c.state === 'running'
  const items: MenuEntry[] = [
    ...(running ? [{ label: t('action.container.restart'), onClick: k('container.restart'), disabled: !manage }, { label: t('action.container.stop'), onClick: k('container.stop'), disabled: !manage }]
      : [{ label: t('action.container.start'), onClick: k('container.start'), disabled: !manage }]),
    ...(onLogs ? [{ label: t('containers.logs'), onClick: onLogs }] : []),
    ...(running && can(user, 'terminal.use') && manage ? [{ label: t('containers.shell'), onClick: () => openTerminal(c.name) }] : []),
    'sep',
    { label: t('action.container.remove'), onClick: k('container.remove'), danger: true, disabled: !manage },
  ]
  // the wrapper keeps clicks from reaching the row (which opens the detail drawer); an
  // onClick on the trigger itself would replace the menu's own handler
  return (
    <span onClick={(e) => e.stopPropagation()}>
      <Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} items={items} />
    </span>
  )
}

// Exposure: who can reach a container, in one line (site domains link out).
export function Exposure({ c, full }: { c: Container; full?: boolean }) {
  const t = useT()
  const e = c.exposure ?? 'internal'
  const tone = e === 'public' ? 'text-ok' : e === 'open' ? 'text-warn' : 'text-mute'
  return (
    <span className={cx('inline-flex flex-wrap items-center gap-x-2 text-[12px]', tone)} title={t('containers.exposure_hint_' + e)}>
      <span>{t('containers.exposure_' + e)}</span>
      {c.sites?.map((s) => (
        <a key={s.id} href={`https://${s.domain}`} target="_blank" rel="noreferrer" onClick={(ev) => ev.stopPropagation()}
          className={cx('num hover:underline', s.enabled ? 'text-acc' : 'text-mute line-through')}>{s.domain}</a>
      ))}
      {(c.wan?.length ?? 0) > 0 && <span className="num text-mute">{t('containers.wan', { ports: c.wan!.join(' ') })}</span>}
      {full && <span className="text-mute">· {t('containers.exposure_hint_' + e)}</span>}
    </span>
  )
}
