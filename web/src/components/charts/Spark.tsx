import type { Point } from '@/lib/types'

// sparkPoints maps values onto a width × height box (y grows downwards).
export function sparkPoints(values: number[], width: number, height: number, max?: number) {
  if (values.length === 0) return ''
  const top = Math.max(max ?? Math.max(...values), 1e-9)
  const step = values.length > 1 ? width / (values.length - 1) : 0
  return values.map((v, i) => `${+(i * step).toFixed(2)},${+(height - 1 - (Math.max(0, v) / top) * (height - 2)).toFixed(2)}`).join(' ')
}

export function Spark({ values, width = 110, height = 22, max, muted, label }: { values: number[]; width?: number; height?: number; max?: number; muted?: boolean; label?: string }) {
  return (
    <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" role="img" aria-label={label} className="block">
      <polyline fill="none" stroke={muted ? '#8c959f' : 'var(--acc)'} strokeWidth="1.5" vectorEffect="non-scaling-stroke" points={sparkPoints(values, width, height, max)} />
    </svg>
  )
}

// AreaChart: a filled line with a max label and a hover readout.
export function AreaChart({ points, height = 64, max, format }: { points: Point[]; height?: number; max?: number; format: (v: number) => string }) {
  const w = 300
  const vals = points.map((p) => p.v)
  const top = max ?? Math.max(...vals, 0)
  const line = sparkPoints(vals, w, height, top)
  return (
    <div className="group relative">
      <svg width="100%" height={height} viewBox={`0 0 ${w} ${height}`} preserveAspectRatio="none" className="block" role="img" aria-label={format(vals[vals.length - 1] ?? 0)}>
        {line && <polygon points={`0,${height} ${line} ${w},${height}`} fill="var(--acc)" opacity=".12" />}
        <polyline fill="none" stroke="var(--acc)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" points={line} />
      </svg>
      <span className="num absolute right-0 top-0 text-[10.5px] text-mute">{format(top)}</span>
    </div>
  )
}
