import { useT } from '@/lib/i18n'

// RingMeter: one ratio against its limit, drawn as a ring (the round dials of router
// panels). The track is a light step of the fill's own hue; the fill turns warning /
// serious past a threshold and then also says so in words, never by colour alone.
export function RingMeter({ value, label, sub, size = 96, warnAt = 0.8, badAt = 0.92, title }: {
  value: number | null; label: string; sub?: string; size?: number; warnAt?: number; badAt?: number; title?: string
}) {
  const t = useT()
  const v = value == null || !isFinite(value) ? null : Math.max(0, Math.min(1, value))
  const level = v == null ? 'none' : v >= badAt ? 'bad' : v >= warnAt ? 'warn' : 'ok'
  const color = level === 'bad' ? 'var(--bad)' : level === 'warn' ? 'var(--warn)' : 'var(--acc)'
  const stroke = Math.max(6, size / 11)
  const r = (size - stroke) / 2
  const c = 2 * Math.PI * r
  const pct = v == null ? '—' : Math.round(v * 100) + '%'
  return (
    <div className="flex flex-col items-center gap-1 text-center" title={title ?? `${label} ${pct}${sub ? ' · ' + sub : ''}`}
      role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={v == null ? undefined : Math.round(v * 100)}>
      <div className="relative" style={{ width: size, height: size }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="block -rotate-90">
          <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke={color} strokeOpacity={0.16} strokeWidth={stroke} />
          {v != null && v > 0 && (
            <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke={color} strokeWidth={stroke} strokeLinecap="round"
              strokeDasharray={`${v * c} ${c}`} style={{ transition: 'stroke-dasharray .6s ease' }} />
          )}
        </svg>
        <div className="num absolute inset-0 flex items-center justify-center font-semibold" style={{ fontSize: Math.round(size / 4.6) }}>{pct}</div>
      </div>
      <div className="text-[12.5px] font-semibold">{label}</div>
      {sub && <div className="num text-[11.5px] text-mute">{sub}</div>}
      {level === 'warn' && <div className="text-[11px] text-warn">{t('chart.high')}</div>}
      {level === 'bad' && <div className="text-[11px] text-bad">{t('chart.very_high')}</div>}
    </div>
  )
}
