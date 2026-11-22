import { Dialog as D } from '@base-ui/react/dialog'
import { X } from 'lucide-react'
import type { ReactNode } from 'react'
import { useT } from '@/lib/i18n'
import { cx } from './cx'

const backdrop = 'fixed inset-0 z-40 bg-black/35 backdrop-blur-[2px] data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 transition-opacity duration-150'
const shadow = 'shadow-[0_1px_3px_rgba(31,35,40,.12),0_8px_24px_rgba(66,74,83,.18)]'

// Dialog: a centred modal, 12px corners (方案 §10.3).
export function Dialog({ open, onOpenChange, title, description, children, footer, wide }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: ReactNode; description?: ReactNode; children?: ReactNode; footer?: ReactNode; wide?: boolean
}) {
  const t = useT()
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Backdrop className={backdrop} />
        <D.Popup className={cx('fixed left-1/2 top-[8vh] z-50 flex max-h-[84vh] w-[calc(100%-1.5rem)] -translate-x-1/2 flex-col rounded-[var(--radius-dialog)] border border-line bg-canvas outline-none',
          wide ? 'max-w-3xl' : 'max-w-lg', shadow, 'transition-[opacity,transform] duration-150 data-[starting-style]:translate-y-1 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0')}>
          <div className="flex items-start gap-3 border-b border-line px-4 pb-2.5 pt-3.5">
            <div className="min-w-0 flex-1">
              <D.Title className="text-[15px] font-semibold">{title}</D.Title>
              {description && <D.Description className="mt-0.5 text-[12.5px] text-mute">{description}</D.Description>}
            </div>
            <D.Close aria-label={t('common.close')} className="rounded p-1 text-mute hover:bg-soft"><X className="size-4" /></D.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">{children}</div>
          {footer && <div className="flex items-center gap-2 border-t border-line px-4 py-3">{footer}</div>}
        </D.Popup>
      </D.Portal>
    </D.Root>
  )
}

// Drawer: right half-screen panel for details and editing; full screen on phones.
export function Drawer({ open, onOpenChange, title, right, children, footer, side = 'right', className }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: ReactNode; right?: ReactNode; children: ReactNode; footer?: ReactNode; side?: 'right' | 'bottom' | 'left'; className?: string
}) {
  const t = useT()
  const pos = side === 'right'
    ? 'inset-y-0 right-0 w-full lg:w-[50vw] lg:max-w-[880px] border-l data-[starting-style]:translate-x-6 data-[ending-style]:translate-x-6'
    : side === 'left'
      ? 'inset-y-0 left-0 w-[280px] max-w-[85vw] border-r data-[starting-style]:-translate-x-6 data-[ending-style]:-translate-x-6'
      : 'inset-x-0 bottom-[29px] h-[min(46vh,520px)] border-t data-[starting-style]:translate-y-6 data-[ending-style]:translate-y-6'
  const modal = side !== 'bottom'
  return (
    <D.Root open={open} onOpenChange={onOpenChange} modal={modal}>
      <D.Portal>
        {modal && <D.Backdrop className={backdrop} />}
        <D.Popup className={cx('fixed z-50 flex flex-col border-line bg-canvas outline-none transition-[opacity,transform] duration-150 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0', pos, shadow, className)}>
          <div className="flex items-center gap-2 border-b border-line bg-soft px-4 py-2.5">
            <D.Title className="min-w-0 flex-1 truncate text-[14px] font-semibold">{title}</D.Title>
            {right}
            <D.Close aria-label={t('common.close')} className="rounded p-1 text-mute hover:bg-canvas"><X className="size-4" /></D.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">{children}</div>
          {footer && <div className="flex items-center gap-2 border-t border-line px-4 py-3">{footer}</div>}
        </D.Popup>
      </D.Portal>
    </D.Root>
  )
}
