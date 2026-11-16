import { useEffect, useRef, useState } from 'react'
import { get } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

// RunLog shows a change's task log live; "::step" lines become headings, "::note" lines
// are translated.
export function RunLog({ changeId }: { changeId: string }) {
  const t = useT()
  const tid = 'tsk_' + changeId.replace(/^chg_/, '')
  const [lines, setLines] = useState<{ seq: number; line: string }[]>([])
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    setLines([])
    get<{ items: { seq: number; line: string }[] }>(`/api/v1/tasks/${tid}/logs`).then((r) => setLines(r.items)).catch(() => {})
  }, [tid])
  useEvents(['task.log'], (_t, e) => {
    if (e.id === tid) setLines((l) => (l.length && l[l.length - 1].seq >= e.seq ? l : [...l, { seq: e.seq, line: e.line }]))
  })
  useEffect(() => { if (box.current) box.current.scrollTop = box.current.scrollHeight }, [lines])
  return (
    <div ref={box} className="num max-h-[50vh] overflow-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[11.5px] leading-relaxed">
      {lines.length === 0 && <span className="text-mute">{t('common.loading')}</span>}
      {lines.map((l) => {
        const m = l.line.match(/^::step \d+ (.*)$/)
        if (m) return <div key={l.seq} className="mt-2 font-sans text-[12.5px] font-semibold text-fg">▸ {m[1]}</div>
        const n = l.line.match(/^(?:\[[^\]]+\] )?::note (\S+)/)
        if (n) return <div key={l.seq} className="font-sans text-warn">{t(n[1])}</div>
        return <div key={l.seq} className="whitespace-pre-wrap break-all">{l.line}</div>
      })}
    </div>
  )
}
