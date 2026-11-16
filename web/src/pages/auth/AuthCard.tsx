import type { ReactNode } from 'react'
import { BerthLockup } from '@/components/brand/BerthMark'

// AuthCard frames sign-in, setup and approval screens.
export function AuthCard({ title, sub, children, wide }: { title: ReactNode; sub?: ReactNode; children: ReactNode; wide?: boolean }) {
  return (
    <div className="flex min-h-full items-start justify-center bg-soft px-4 py-[10vh]">
      <div className={wide ? 'w-full max-w-xl' : 'w-full max-w-sm'}>
        <div className="mb-4 flex items-center justify-center gap-2 text-[18px]">
          <BerthLockup size={36} />
        </div>
        <div className="rounded-[var(--radius-dialog)] border border-line bg-canvas">
          <div className="border-b border-line px-5 pb-3 pt-4">
            <h1 className="text-[16px] font-semibold">{title}</h1>
            {sub && <p className="mt-1 text-[13px] leading-relaxed text-mute">{sub}</p>}
          </div>
          <div className="px-5 py-4">{children}</div>
        </div>
      </div>
    </div>
  )
}
