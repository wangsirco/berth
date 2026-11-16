import { post } from '@/lib/api'
import { clock } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { usePick, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { cx } from './ui/cx'

interface Item { type: 'change' | 'login'; ts: number; change?: Change; login?: { username: string; ok: boolean; method: string; ip: string } }

export function ActorBadge({ c }: { c: Change }) {
  const t = useT()
  if (c.actor.type === 'user' && c.actor.via === 'ui') return null
  return <span className="num ml-1 rounded border border-line px-1 text-[11px] text-mute" title={t('via.' + c.actor.via)}>{c.actor.name}</span>
}

export const undoable = (c: Change) => c.status === 'done' && !!c.undo

// Timeline: the overview's "动态" — recent changes and sign-ins, with undo where possible.
export function Timeline({ limit = 12 }: { limit?: number }) {
  const t = useT()
  const pick = usePick()
  const tl = useApi<{ items: Item[] }>(`/api/v1/timeline?limit=${limit}`)
  useEvents(['change.done'], () => void tl.reload())
  const items = tl.data?.items ?? []
  if (!items.length) return <p className="text-[13px] text-mute">{tl.loading ? t('common.loading') : t('timeline.empty')}</p>
  return (
    <ol className="border-l-2 border-line">
      {items.map((it, i) => {
        const c = it.change
        return (
          <li key={i} className={cx('relative py-1.5 pl-3.5 text-[13px] before:absolute before:-left-[5px] before:top-[11px] before:size-2 before:rounded-full before:shadow-[0_0_0_2px_var(--canvas)]',
            c ? (c.status === 'failed' ? 'before:bg-bad' : 'before:bg-acc') : 'before:bg-[#8c959f]')}>
            {c ? (
              <>
                <span className={cx(c.status === 'reverted' && 'line-through decoration-mute')}>{pick(c.summary)}</span>
                <ActorBadge c={c} />
                <span className="block text-[12px] text-mute">
                  {t('via.' + c.actor.via)} · {clock(it.ts)}
                  {c.status === 'failed' && ' · ' + t('status_change.failed')}
                  {undoable(c) && <> · <button className="text-acc hover:underline" onClick={() => void post(`/api/v1/changes/${c.id}/revert`).then(() => tl.reload())}>{t('timeline.undo')}</button></>}
                </span>
              </>
            ) : (
              <>
                {it.login!.ok ? t('timeline.login', { method: t('login_method.' + it.login!.method) }) : t('timeline.login_failed')}
                <span className="block text-[12px] text-mute">{it.login!.username} · {it.login!.ip} · {clock(it.ts)}</span>
              </>
            )}
          </li>
        )
      })}
    </ol>
  )
}
