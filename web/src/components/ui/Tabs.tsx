import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { cx } from './cx'

export interface TabItem<T extends string> { value: T; label: ReactNode; count?: number; to?: string }

// TabBar: a page's sections, under its title — large, underlined, and kept at the top while the
// page scrolls. Items with `to` are links (sections with their own URL); the rest call onChange.
export function TabBar<T extends string>({ value, items, onChange, label, right }: { value: T; items: TabItem<T>[]; onChange?: (v: T) => void; label?: string; right?: ReactNode }) {
  return (
    <nav aria-label={label} className="sticky top-0 z-20 -mx-3 border-b border-line bg-canvas/95 px-3 backdrop-blur sm:-mx-8 sm:px-8">
      <div className="-mb-px flex items-end gap-2">
      <div role="tablist" className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
        {items.map((it) => {
          const on = it.value === value
          const cls = cx('relative shrink-0 whitespace-nowrap px-3 pb-2.5 pt-2 text-[14px] transition-colors',
            on ? 'font-semibold text-fg after:absolute after:inset-x-2 after:bottom-0 after:h-[3px] after:rounded-full after:bg-acc' : 'text-mute hover:text-fg')
          const body = <>{it.label}{it.count != null && it.count > 0 && <span className={cx('num ml-1.5 rounded-full px-1.5 text-[11.5px]', on ? 'bg-acc text-white' : 'bg-soft text-mute')}>{it.count}</span>}</>
          return it.to
            ? <Link key={it.value} to={it.to} role="tab" aria-selected={on} className={cls}>{body}</Link>
            : <button key={it.value} type="button" role="tab" aria-selected={on} onClick={() => onChange?.(it.value)} className={cls}>{body}</button>
        })}
      </div>
      {right && <div className="flex shrink-0 items-center gap-1.5 pb-1.5">{right}</div>}
      </div>
    </nav>
  )
}
