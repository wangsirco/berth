import { useEffect, useRef, useState } from 'react'
import { get, post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { duration } from '@/lib/format'
import type { TaskView } from '@/lib/types'
import { useApi } from '@/lib/hooks'
import { useEvents } from '@/lib/useEvents'
import { Button } from '../ui/Button'
import { Drawer } from '../ui/Dialog'
import { Dot, Empty, Row } from '../ui/Box'
import { cx } from '../ui/cx'

export function Steps({ task }: { task: TaskView }) {
  const n = Math.max(task.steps.length, 1)
  return (
    <span className="inline-flex gap-[3px]" aria-label={`${task.step + 1}/${n}`}>
      {Array.from({ length: n }, (_, i) => (
        <i key={i} className={cx('h-1.5 w-7 rounded-[1px]',
          i < task.step || task.status === 'done' ? 'bg-acc' : i === task.step && task.status === 'running' ? 'animate-[berth-pulse_1.6s_ease-in-out_infinite] bg-acc-2' : 'bg-line')} />
      ))}
    </span>
  )
}

const dot = (s: TaskView['status']): 'busy' | 'ok' | 'off' | 'bad' => (s === 'running' ? 'busy' : s === 'done' ? 'ok' : s === 'waiting' ? 'off' : 'bad')

export function TaskDrawer({ open, onOpenChange, focus }: { open: boolean; onOpenChange: (o: boolean) => void; focus?: string }) {
  const t = useT()
  const list = useApi<{ items: TaskView[] }>(open ? '/api/v1/tasks?limit=30' : null, [open])
  const [sel, setSel] = useState<string | undefined>(focus)
  const [lines, setLines] = useState<{ seq: number; line: string }[]>([])
  const [follow, setFollow] = useState(true)
  const box = useRef<HTMLPreElement>(null)
  useEffect(() => { if (focus) setSel('tsk_' + focus.replace(/^chg_/, '')) }, [focus])
  useEffect(() => {
    setLines([])
    if (!sel) return
    get<{ items: { seq: number; line: string }[] }>(`/api/v1/tasks/${sel}/logs`).then((r) => setLines(r.items)).catch(() => {})
  }, [sel])
  useEvents(['task.update', 'task.log'], (type, e) => {
    if (type === 'task.update') void list.reload()
    if (type === 'task.log' && e.id === sel) setLines((l) => (l.length && l[l.length - 1].seq >= e.seq ? l : [...l, { seq: e.seq, line: e.line }]))
  })
  // "::progress 3/8 name" lines from long installs become a bar
  const progress = [...lines].reverse().map((l) => /^::progress (\d+)\/(\d+) (.*)$/.exec(l.line)).find(Boolean)
    useEffect(() => { if (follow && box.current) box.current.scrollTop = box.current.scrollHeight }, [lines, follow])
  const items = list.data?.items ?? []
  // opened without a task in mind: show the one running (or the latest)
  useEffect(() => { if (open && !sel && items.length) setSel((items.find((x) => x.status === 'running') ?? items[0]).id) }, [open, items.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const cur = items.find((x) => x.id === sel)
  const now = Date.now() / 1000
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t('tasks.title')}>
      {items.length === 0 ? <Empty title={t('tasks.empty')}>{t('tasks.empty_hint')}</Empty> : (
        <div className="flex h-full flex-col">
          <div className="max-h-[40%] overflow-auto border-b border-line">
            {items.map((x) => (
              <Row key={x.id} onClick={() => setSel(x.id)} className={cx(sel === x.id && 'bg-tint')}>
                <Dot state={dot(x.status)} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13.5px] font-semibold">{t('kind.' + x.kind)}</div>
                  <div className="text-[12px] text-mute">
                    {x.status === 'waiting' && x.wait_reason ? t(x.wait_reason, x.wait_params) : t('tasks.status.' + x.status)}
                    {' · '}{x.created_by}
                    {x.started_at ? ' · ' + duration((x.finished_at || now) - x.started_at, t) : ''}
                  </div>
                </div>
                <Steps task={x} />
              </Row>
            ))}
          </div>
          {cur && (
            <div className="flex min-h-0 flex-1 flex-col">
              <div className="flex items-center gap-2 px-4 py-2 text-[12.5px]">
                <span className="text-mute">{t('tasks.log')}</span>
                <label className="ml-auto inline-flex items-center gap-1 text-mute"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />{t('tasks.follow')}</label>
                <Button size="sm" onClick={() => navigator.clipboard?.writeText(lines.map((l) => l.line).join('\n'))}>{t('common.copy')}</Button>
                {(cur.status === 'running' || cur.status === 'waiting') && <Button size="sm" variant="danger" onClick={() => void post(`/api/v1/tasks/${cur.id}/cancel`)}>{t('tasks.cancel')}</Button>}
              </div>
              {progress && <Progress p={progress} done={cur.status === 'done'} />}
              <pre ref={box} className="num min-h-0 flex-1 overflow-auto bg-term px-4 py-2 text-[12px] leading-relaxed text-term-fg">
                {lines.filter((l) => !l.line.startsWith('::step ') && !l.line.startsWith('::progress ')).map((l) => l.line).join('\n') || t('tasks.no_log')}
              </pre>
            </div>
          )}
        </div>
      )}
    </Drawer>
  )
}

function Progress({ p, done }: { p: RegExpExecArray; done: boolean }) {
  const t = useT()
  const [n, total, name] = [Number(p[1]), Number(p[2]), p[3]]
  const key = 'desktop.item.' + name
  const label = t(key) === key ? name : t(key)
  const pct = done ? 100 : Math.round(((n - 1) / total) * 100)
  return (
    <div className="px-4 pb-2 text-[12px]">
      <div className="mb-1 flex justify-between"><span>{done ? t('tasks.progress_done') : t('tasks.progress_now', { name: label })}</span><span className="num text-mute">{done ? total : n}/{total}</span></div>
      <div className="h-1.5 overflow-hidden rounded-full bg-line"><div className="h-full rounded-full bg-acc transition-all" style={{ width: pct + '%' }} /></div>
    </div>
  )
}
