import { Bot, ChevronDown, ChevronRight, CircleAlert, Eye, Hand, Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useLocale, useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

export interface Activity { id: number; token_id: number; ts: number; method: string; kind?: string; host?: string; obj_type?: string; obj_id?: string; change_id?: string; ok: boolean; summary?: string }
interface ChangeV { id: string; kind: string; host: string; status: string; title: Record<string, string>; summary: Record<string, string> }
export interface TokenNow { id: number; name: string; kind: string; working: boolean; last: number; count_24h: number; recent: Activity[]; running: ChangeV[]; pending: ChangeV[] }

type TFn = ReturnType<typeof useT>

// describe: one activity in plain words — "重启容器 n8n", "读取日志 web", "预览了 部署项目 acme".
export function describe(t: TFn, a: Activity): string {
  const obj = a.obj_id ? ' ' + a.obj_id : ''
  if (a.kind) {
    const what = t('kind.' + a.kind)
    if (a.summary === 'planned') return t('ai.previewed', { what: what + obj })
    if (a.summary === 'pending_approval') return t('ai.asked', { what: what + obj })
    if (!a.ok) return t('ai.failed', { what: what + obj })
    return what + obj
  }
  const key = 'ai.tool_' + a.method
  const what = t(key)
  return (what === key ? a.method : what) + obj + (a.ok ? '' : ' · ' + t('ai.error'))
}

// useTokenActivity: the tokens that worked in the last day, refreshed as they act.
export function useTokenActivity() {
  const d = useApi<{ items: TokenNow[] }>('/api/v1/tokens/activity')
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const soon = () => { clearTimeout(timer.current); timer.current = setTimeout(() => void d.reload(), 600) }
  useEvents(['token.activity', 'change.done', 'change.pending'], soon)
  // "working" fades two minutes after the last call: look again now and then
  useEffect(() => { const id = setInterval(() => void d.reload(), 30000); return () => clearInterval(id) }, [])
  return d
}

function Breathing({ on }: { on: boolean }) {
  return (
    <span className="relative inline-flex size-2.5">
      {on && <span className="absolute inline-flex size-full animate-ping rounded-full bg-acc opacity-60" />}
      <span className={cx('relative inline-flex size-2.5 rounded-full', on ? 'bg-acc' : 'bg-mute')} />
    </span>
  )
}

// AIActivityCard: on the home page — who is working right now, what they are doing, what waits for you.
export function AIActivityCard() {
  const t = useT()
  const { host } = useHost()
  const d = useTokenActivity()
  const [locale] = useLocale()
  const [more, setMore] = useState(false)
  const items = d.data?.items ?? []
  if (items.length === 0) return null
  const working = items.filter((x) => x.working || x.pending.length > 0)
  if (working.length === 0) {
    const x = items[0]
    return (
      <div className="mb-4 flex items-center gap-2 text-[12.5px] text-mute">
        <Bot className="size-4" />
        <span>{t('ai.recently', { name: x.name, ago: ago(x.last, t), n: x.count_24h })}</span>
        <Link className="text-acc hover:underline" to={hostPath(host, 'tokens') + '?t=' + x.id}>{t('ai.timeline')}</Link>
      </div>
    )
  }
  return (
    <Box className="mb-5" title={<span className="inline-flex items-center gap-2"><Bot className="size-4 text-acc" />{t('ai.now_title')}</span>}>
      {working.map((x) => (
        <div key={x.id} className="border-b border-line px-4 py-3 last:border-b-0">
          <div className="flex flex-wrap items-center gap-2">
            <Breathing on={x.working} />
            <b className="text-[13.5px]">{x.name}</b>
            <Pill>{t('tokens.kind_' + x.kind)}</Pill>
            <span className="text-[13px]">{x.recent[0] ? (x.working ? t('ai.doing', { what: describe(t, x.recent[0]) }) : describe(t, x.recent[0])) : ''}</span>
            <span className="ml-auto text-[12px] text-mute">{ago(x.last, t)} · {t('ai.n_today', { n: x.count_24h })}</span>
          </div>
          {x.running.map((c) => (
            <div key={c.id} className="mt-2 flex items-center gap-2 rounded-lg bg-tint px-2.5 py-1.5 text-[12.5px]">
              <Loader2 className="size-3.5 animate-spin text-acc" />
              <span className="flex-1">{c.summary[locale] ?? c.title[locale] ?? c.kind}</span>
              <Link className="text-acc hover:underline" to={hostPath(c.host, 'audit') + '?id=' + c.id}>{t('ai.watch')}</Link>
            </div>
          ))}
          {x.pending.map((c) => (
            <div key={c.id} className="mt-2 flex items-center gap-2 rounded-lg border border-warn px-2.5 py-1.5 text-[12.5px]">
              <Hand className="size-3.5 text-warn" />
              <span className="flex-1">{t('ai.waiting', { what: c.summary[locale] ?? c.title[locale] ?? c.kind })}</span>
              <Link to={hostPath(c.host, 'audit') + '?pending=1'}><Button size="sm" variant="primary">{t('ai.approve')}</Button></Link>
            </div>
          ))}
          {x.recent.length > 1 && (
            <button className="mt-1.5 inline-flex items-center gap-1 text-[12px] text-mute hover:text-fg" onClick={() => setMore(!more)}>
              {more ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}{t('ai.earlier')}
            </button>
          )}
          {more && <ActivityList items={x.recent.slice(1)} />}
        </div>
      ))}
    </Box>
  )
}

export function ActivityList({ items, names }: { items: Activity[]; names?: Record<number, string> }) {
  const t = useT()
  return (
    <ul className="mt-1 space-y-0.5">
      {items.map((a) => (
        <li key={a.id} className="flex items-center gap-2 text-[12.5px]">
          {a.kind ? (a.ok ? <span className="size-1.5 rounded-full bg-acc" /> : <CircleAlert className="size-3.5 text-bad" />) : <Eye className="size-3.5 text-mute" />}
          <span className="num w-16 shrink-0 text-mute">{ago(a.ts, t)}</span>
          {names?.[a.token_id] && <b className="shrink-0">{names[a.token_id]}</b>}
          <span className="min-w-0 flex-1 truncate">{describe(t, a)}</span>
          {a.host && a.host !== 'local' && <span className="text-mute">{a.host}</span>}
          {a.change_id && <Link className="text-acc hover:underline" to={hostPath(a.host || 'local', 'audit') + '?id=' + a.change_id}>{t('ai.details')}</Link>}
        </li>
      ))}
    </ul>
  )
}

// ObjectActivity: a strip on an object's page when a token worked on it in the last ten minutes.
export function ObjectActivity({ type, id, className }: { type: string; id: string | number; className?: string }) {
  const t = useT()
  const { host } = useHost()
  const d = useApi<{ items: (Activity & { token_name: string })[] }>(`/api/v1/hosts/${host}/activity?type=${encodeURIComponent(type)}&id=${encodeURIComponent(String(id))}`, [host, type, id])
  useEvents(['token.activity'], (_t, e: { activity: Activity }) => { if (e?.activity?.obj_type === type && e.activity.obj_id === String(id)) void d.reload() })
  const [open, setOpen] = useState(false)
  const items = d.data?.items ?? []
  if (items.length === 0) return null
  const x = items[0]
  const live = Date.now() / 1000 - x.ts < 120
  return (
    <div className={cx('rounded-xl border border-acc/40 bg-tint px-3 py-2 text-[12.5px]', className)}>
      <button className="flex w-full items-center gap-2 text-left" onClick={() => setOpen(!open)}>
        <Breathing on={live} /><Bot className="size-4 text-acc" />
        <span className="flex-1">{t(live ? 'ai.here_now' : 'ai.here_recent', { name: x.token_name, ago: ago(x.ts, t), what: describe(t, x) })}</span>
        {open ? <ChevronDown className="size-3.5 text-mute" /> : <ChevronRight className="size-3.5 text-mute" />}
      </button>
      {open && <ActivityList items={items} names={Object.fromEntries(items.map((i) => [i.token_id, i.token_name]))} />}
    </div>
  )
}
