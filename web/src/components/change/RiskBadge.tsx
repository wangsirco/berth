import type { Risk } from '@/lib/types'
import { useT } from '@/lib/i18n'
import { cx } from '../ui/cx'

// Risk is shown with a word and a shape, never colour alone (方案 §10.2).
const shapes: Record<Risk, { shape: string; path: string; tone: string }> = {
  low: { shape: 'circle', path: 'M6 1a5 5 0 1 0 0 10A5 5 0 0 0 6 1z', tone: 'text-ok border-ok/50' },
  medium: { shape: 'triangle', path: 'M6 1l5 9.5H1z', tone: 'text-warn border-warn/50' },
  high: { shape: 'diamond', path: 'M6 .8l5.2 5.2L6 11.2.8 6z', tone: 'text-bad border-bad/50' },
  destructive: { shape: 'octagon', path: 'M3.8 1h4.4L11 3.8v4.4L8.2 11H3.8L1 8.2V3.8z', tone: 'text-white bg-bad border-bad' },
}

export function RiskBadge({ risk }: { risk: Risk }) {
  const t = useT()
  const s = shapes[risk] ?? shapes.low
  return (
    <span className={cx('inline-flex items-center gap-1 rounded-full border px-2 text-xs leading-5', s.tone)}>
      <svg viewBox="0 0 12 12" className="size-3" data-shape={s.shape} aria-hidden><path d={s.path} fill="currentColor" /></svg>
      {t('risk.' + risk)}
    </span>
  )
}
