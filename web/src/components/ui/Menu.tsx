import { Menu as M } from '@base-ui/react/menu'
import type { ReactElement, ReactNode } from 'react'
import { cx } from './cx'

export type MenuEntry = { label: ReactNode; onClick: () => void; danger?: boolean; disabled?: boolean } | 'sep'

export function Menu({ trigger, items, align = 'end' }: { trigger: ReactElement; items: MenuEntry[]; align?: 'start' | 'end' }) {
  return (
    <M.Root>
      <M.Trigger render={trigger} />
      <M.Portal>
        <M.Positioner sideOffset={4} align={align} className="z-50">
          <M.Popup className="min-w-44 rounded-[var(--radius-box)] border border-line bg-canvas py-1 text-[13px] shadow-[0_8px_24px_rgba(66,74,83,.18)] outline-none">
            {items.map((it, i) =>
              it === 'sep' ? (
                <div key={i} className="my-1 border-t border-line" />
              ) : (
                <M.Item key={i} disabled={it.disabled} onClick={it.onClick}
                  className={cx('flex cursor-default items-center gap-2 px-3 py-1.5 outline-none data-[highlighted]:bg-soft data-[disabled]:opacity-50', it.danger && 'text-bad')}>
                  {it.label}
                </M.Item>
              ),
            )}
          </M.Popup>
        </M.Positioner>
      </M.Portal>
    </M.Root>
  )
}
