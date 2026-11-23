import { useRef, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { cx } from '@/components/ui/cx'
import { bytes } from '@/lib/format'

// FlowChart: download and upload over time — two thin lines (download filled lightly), a
// hover crosshair with both values, recessive axes. Download: the accent; upload: the blue.
export interface FlowPoint { t: number; down: number; up: number }

const W = 640

export function FlowChart({ points, height = 140, label, time }: { points: FlowPoint[]; height?: number; label: { down: string; up: string }; time: (t: number) => string }) {
  const [hover, setHover] = useState<number | null>(null)
  const box = useRef<HTMLDivElement>(null)
  if (points.length === 0) return <div className="flex items-center justify-center text-[12px] text-mute" style={{ height }}>—</div>
  const top = Math.max(...points.map((p) => Math.max(p.down, p.up)), 1)
  const x = (i: number) => (points.length > 1 ? (i / (points.length - 1)) * W : W / 2)
  const y = (v: number) => height - 4 - (v / top) * (height - 12)
  const line = (k: 'down' | 'up') => points.map((p, i) => `${x(i).toFixed(1)},${y(p[k]).toFixed(1)}`).join(' ')
  const move = (e: React.MouseEvent) => {
    const r = box.current!.getBoundingClientRect()
    const i = Math.round(((e.clientX - r.left) / r.width) * (points.length - 1))
    setHover(Math.max(0, Math.min(points.length - 1, i)))
  }
  const h = hover === null ? null : points[hover]
  return (
    <div ref={box} className="relative select-none" onMouseMove={move} onMouseLeave={() => setHover(null)} data-flow-chart>
      <svg width="100%" height={height} viewBox={`0 0 ${W} ${height}`} preserveAspectRatio="none" className="block" role="img" aria-label={`${label.down} ${bytes(points.reduce((s, p) => s + p.down, 0))}`}>
        {[0.5, 1].map((f) => <line key={f} x1="0" x2={W} y1={y(top * f)} y2={y(top * f)} stroke="var(--line)" strokeWidth="1" vectorEffect="non-scaling-stroke" />)}
        <polygon points={`0,${height} ${line('down')} ${W},${height}`} fill="var(--acc)" opacity=".1" />
        <polyline fill="none" stroke="var(--acc)" strokeWidth="2" vectorEffect="non-scaling-stroke" points={line('down')} />
        <polyline fill="none" stroke="var(--berth-vm)" strokeWidth="2" vectorEffect="non-scaling-stroke" points={line('up')} />
        {h && <line x1={x(hover!)} x2={x(hover!)} y1="0" y2={height} stroke="var(--mute)" strokeWidth="1" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />}
      </svg>
      <span className="num absolute right-0 top-0 text-[10.5px] text-mute">{bytes(top)}</span>
      <div className="mt-1 flex items-center gap-3 text-[11.5px] text-mute">
        <span className="inline-flex items-center gap-1"><span className="inline-block h-0.5 w-3 bg-acc" />{label.down}</span>
        <span className="inline-flex items-center gap-1"><span className="inline-block h-0.5 w-3 bg-[var(--berth-vm)]" />{label.up}</span>
        <span className="num ml-auto">{time(points[0].t)} – {time(points[points.length - 1].t)}</span>
      </div>
      {h && (
        <div className="pointer-events-none absolute top-1 z-10 rounded-[var(--radius-box)] border border-line bg-card px-2.5 py-1.5 text-[12px] shadow" style={{ left: `min(calc(${(x(hover!) / W) * 100}% + 8px), calc(100% - 150px))` }}>
          <div className="num text-mute">{time(h.t)}</div>
          <div>{label.down} <b className="num">{bytes(h.down)}</b></div>
          <div>{label.up} <b className="num">{bytes(h.up)}</b></div>
        </div>
      )}
    </div>
  )
}

// BarList: ranked rows with a thin bar; each row opens something
export function BarList({ rows, empty = '—' }: { rows: { key: string; label: React.ReactNode; value: number; text: string; sub?: string; onClick?: () => void }[]; empty?: string }) {
  const top = Math.max(...rows.map((r) => r.value), 1)
  if (rows.length === 0) return <div className="py-3 text-[12.5px] text-mute">{empty}</div>
  return (
    <div>
      {rows.map((r) => (
        <button key={r.key} type="button" disabled={!r.onClick} onClick={r.onClick} data-bar-row={r.key}
          className={cx('flex w-full items-center gap-2.5 border-t border-line py-2 text-left text-[13px] first:border-t-0', r.onClick && 'hover:bg-soft')}>
          <span className="w-28 min-w-0 truncate">{r.label}</span>
          <span className="flex-1"><span className="block h-1.5 rounded-sm bg-acc" style={{ width: `${Math.max(2, (r.value / top) * 100)}%` }} /></span>
          <span className="num w-20 text-right text-[12px] text-mute">{r.text}</span>
          {r.onClick && <ChevronRight className="size-3.5 shrink-0 text-mute" />}
        </button>
      ))}
    </div>
  )
}
