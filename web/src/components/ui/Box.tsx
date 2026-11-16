import type { ReactNode } from 'react'
import { cx } from './cx'

// Box is the bordered list box from the design (.box / .bh / .row).
export function Box({ title, count, right, children, className }: { title?: ReactNode; count?: number; right?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx('rounded-[var(--radius-box)] border border-line bg-card', className)}>
      {(title || right) && (
        <header className="flex items-center gap-2.5 rounded-t-[var(--radius-box)] border-b border-line bg-soft px-3.5 py-2">
          {title && <h2 className="text-[13.5px] font-semibold">{title}</h2>}
          {count !== undefined && <Pill>{count}</Pill>}
          {right && <div className="ml-auto flex items-center gap-2 text-[12.5px] text-mute">{right}</div>}
        </header>
      )}
      {children}
    </section>
  )
}

export function Row({ children, className, onClick }: { children: ReactNode; className?: string; onClick?: () => void }) {
  return (
    <div onClick={onClick} className={cx('flex items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0', onClick && 'cursor-pointer hover:bg-soft', className)}>
      {children}
    </div>
  )
}

export function Pill({ children, tone, className }: { children: ReactNode; tone?: 'ok' | 'warn' | 'bad' | 'acc'; className?: string }) {
  const t = { ok: 'text-ok border-ok/50', warn: 'text-warn border-warn/50', bad: 'text-bad border-bad/50', acc: 'text-acc border-acc/50' }
  return <span className={cx('inline-flex items-center whitespace-nowrap rounded-full border border-line px-2 text-xs leading-5 text-mute', tone && t[tone], className)}>{children}</span>
}

export function Dot({ state, className }: { state: 'ok' | 'warn' | 'bad' | 'off' | 'busy'; className?: string }) {
  const c = { ok: 'bg-ok', warn: 'bg-[#d4a72c]', bad: 'bg-bad', off: 'bg-[#8c959f]', busy: 'bg-acc-2 animate-[berth-pulse_1.6s_ease-in-out_infinite]' }
  return <span aria-hidden className={cx('inline-block size-2.5 shrink-0 rounded-full', c[state], className)} />
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="num rounded border border-line px-1 text-[11px] text-mute">{children}</kbd>
}

export function Empty({ title, children, action }: { title: ReactNode; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="px-6 py-10 text-center">
      <p className="text-[14px] font-semibold">{title}</p>
      {children && <p className="mx-auto mt-1.5 max-w-md text-[13px] leading-relaxed text-mute">{children}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

export function Notice({ tone = 'acc', children }: { tone?: 'acc' | 'warn' | 'bad'; children: ReactNode }) {
  const t = { acc: 'border-acc/40 bg-tint', warn: 'border-warn/50 bg-[color-mix(in_srgb,var(--warn)_8%,var(--canvas))]', bad: 'border-bad/50 bg-[color-mix(in_srgb,var(--bad)_7%,var(--canvas))]' }
  return <div className={cx('rounded-[var(--radius-box)] border px-3.5 py-2.5 text-[13px]', t[tone])}>{children}</div>
}
