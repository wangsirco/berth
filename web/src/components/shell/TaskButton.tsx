import { CheckCircle2, ListChecks, Loader2, XCircle } from 'lucide-react'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { TaskView } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { cx } from '../ui/cx'
import { Steps } from './TaskDrawer'

// TaskButton: in the sidebar, always there — what is running now (with its steps), how many wait,
// or the last one that finished. A click opens the task log on the one that matters.
export function TaskButton({ onOpen }: { onOpen: (changeId?: string) => void }) {
  const t = useT()
  const d = useApi<{ items: TaskView[] }>('/api/v1/tasks?limit=10')
  useEvents(['task.update'], () => void d.reload())
  const items = d.data?.items ?? []
  const running = items.filter((x) => x.status === 'running')
  const waiting = items.filter((x) => x.status === 'waiting')
  const last = items.find((x) => x.status === 'done' || x.status === 'failed' || x.status === 'canceled')
  const cur = running[0] ?? waiting[0]
  const busy = running.length + waiting.length > 0
  return (
    <button onClick={() => onOpen(cur?.change_id ?? last?.change_id)} aria-label={t('tasks.title')}
      className={cx('mb-1 flex w-full flex-col gap-1 rounded-[9px] px-2.5 py-[7px] text-left text-[13px] hover:bg-[color-mix(in_srgb,var(--line)_45%,transparent)]', busy && 'bg-tint')}>
      <span className="flex w-full items-center gap-2">
        {running.length ? <Loader2 className="size-4 animate-spin text-acc" /> : last?.status === 'failed' ? <XCircle className="size-4 text-bad" /> : last ? <CheckCircle2 className="size-4 text-mute" /> : <ListChecks className="size-4 text-mute" />}
        <span className={cx('flex-1 truncate', busy ? 'font-semibold' : 'text-mute')}>
          {busy ? t('tasks.side_busy', { r: running.length, w: waiting.length }) : t('tasks.title')}
        </span>
        {!busy && last && <span className="text-[11.5px] text-mute">{ago(last.finished_at || last.created_at, t)}</span>}
      </span>
      {cur && (
        <span className="flex w-full flex-col gap-1 pl-6">
          <span className="truncate text-[12px] text-mute">{t('kind.' + cur.kind)}{cur.status === 'waiting' && cur.wait_reason ? ' · ' + t(cur.wait_reason, cur.wait_params ?? {}) : ''}</span>
          {cur.status === 'running' && <Steps task={cur} />}
        </span>
      )}
      {!busy && last?.status === 'failed' && <span className="truncate pl-6 text-[12px] text-bad">{t('tasks.side_failed', { what: t('kind.' + last.kind) })}</span>}
    </button>
  )
}
