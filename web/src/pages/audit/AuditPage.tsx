import { ChevronRight, Download } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { RiskBadge } from '@/components/change/RiskBadge'
import { ActorBadge, undoable } from '@/components/Timeline'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Input, Select } from '@/components/ui/Field'
import { ApiError, get, post } from '@/lib/api'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { usePick, useT } from '@/lib/i18n'
import type { Change } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { ChangeDetail } from './ChangeDetail'

const state = (s: Change['status']) => (s === 'done' ? 'ok' : s === 'failed' || s === 'rejected' ? 'bad' : s === 'pending_approval' ? 'warn' : s === 'running' ? 'busy' : 'off')

type Group = { session: string; items: Change[] }

// group consecutive changes from the same AI session into one collapsible entry.
export function groupBySession(list: Change[]): (Change | Group)[] {
  const out: (Change | Group)[] = []
  for (const c of list) {
    const s = c.actor.ai_session
    const last = out[out.length - 1]
    if (s && last && 'session' in last && last.session === s) last.items.push(c)
    else if (s) out.push({ session: s, items: [c] })
    else out.push(c)
  }
  return out.map((x) => ('session' in x && x.items.length === 1 ? x.items[0] : x))
}

function csv(list: Change[], pick: (m?: Record<string, string>) => string) {
  const esc = (s: string) => `"${s.replaceAll('"', '""')}"`
  const rows = [['time', 'actor', 'via', 'ip', 'kind', 'status', 'summary', 'id'].join(',')]
  for (const c of list) rows.push([new Date(c.ts * 1000).toISOString(), c.actor.name, c.actor.via, c.actor.ip, c.kind, c.status, pick(c.summary), c.id].map((x) => esc(String(x))).join(','))
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob(['﻿' + rows.join('\n')], { type: 'text/csv' }))
  a.download = `berth-audit-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
}

export default function AuditPage() {
  const t = useT()
  const pick = usePick()
  const [sp, setSp] = useSearchParams()
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<Change | null>(null)
  // ?id=<change> opens that change (links from the AI activity card and timelines)
  useEffect(() => {
    const id = new URLSearchParams(location.search).get('id')
    if (id) void get<Change>(`/api/v1/changes/${encodeURIComponent(id)}`).then(setOpen).catch(() => {})
  }, [])
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [more, setMore] = useState<Change[]>([])
  const [err, setErr] = useState('')
  const filter = new URLSearchParams()
  for (const k of ['via', 'status', 'kind']) if (sp.get(k)) filter.set(k, sp.get(k)!)
  filter.set('limit', '100')
  const list = useApi<{ items: Change[] }>(`/api/v1/changes?${filter}`, [sp.toString()])
  const pending = useApi<{ items: Change[] }>('/api/v1/changes/pending')
  useEvents(['change.done', 'change.pending'], () => { void list.reload(); void pending.reload() })
  const all = useMemo(() => [...(list.data?.items ?? []), ...more], [list.data, more])
  const shown = all.filter((c) => !q || (pick(c.summary) + c.actor.name + c.kind).toLowerCase().includes(q.toLowerCase()))
  const set = (k: string, v: string) => { const n = new URLSearchParams(sp); if (v) n.set(k, v); else n.delete(k); setSp(n); setMore([]) }
  const act = async (fn: () => Promise<unknown>) => {
    setErr('')
    try { await fn(); await list.reload(); await pending.reload() } catch (e) { setErr((e as ApiError).message) }
  }
  const loadMore = async () => {
    const last = all[all.length - 1]
    if (!last) return
    const n = new URLSearchParams(filter)
    n.set('before', last.id)
    const r = await get<{ items: Change[] }>(`/api/v1/changes?${n}`)
    setMore((m) => [...m, ...r.items])
  }

  const row = (c: Change, nested = false) => (
    <Row key={c.id} onClick={() => setOpen(c)} className={nested ? 'pl-9' : ''}>
      <Dot state={state(c.status)} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px]">{pick(c.summary)}<ActorBadge c={c} /></div>
        <div className="text-[12px] text-mute">{c.actor.name} · {t('via.' + c.actor.via)} · {datetime(c.ts)}{c.status !== 'done' && ' · ' + t('status_change.' + c.status)}</div>
      </div>
      <span className="hidden sm:inline"><RiskBadge risk={c.plan.risk} /></span>
      {undoable(c) && <Button size="sm" onClick={(e) => { e.stopPropagation(); void act(() => post(`/api/v1/changes/${c.id}/revert`)) }}>{t('timeline.undo')}</Button>}
    </Row>
  )

  return (
    <div className="space-y-4">
      {err && <Notice tone="bad">{err}</Notice>}
      {(pending.data?.items.length ?? 0) > 0 && (
        <Box title={t('audit.pending')} count={pending.data!.items.length}>
          {pending.data!.items.map((c) => (
            <Row key={c.id}>
              <Dot state="warn" />
              <div className="min-w-0 flex-1 cursor-pointer" onClick={() => setOpen(c)}>
                <div className="text-[13.5px]">{pick(c.summary)}<ActorBadge c={c} /></div>
                <div className="text-[12px] text-mute">{c.actor.intent || t('via.' + c.actor.via)} · {datetime(c.ts)}</div>
              </div>
              <RiskBadge risk={c.plan.risk} />
              <Button size="sm" onClick={() => void act(() => post(`/api/v1/changes/${c.id}/reject`))}>{t('audit.reject')}</Button>
              <Button size="sm" variant="primary" onClick={() => void act(() => post(`/api/v1/changes/${c.id}/confirm`))}>{t('audit.approve')}</Button>
            </Row>
          ))}
        </Box>
      )}
      <Box title={t('nav.audit')} right={
        <>
          <Input className="h-7 w-40 text-[13px]" placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.target.value)} />
          <Select className="h-7 w-28 text-[13px]" value={sp.get('via') ?? ''} onChange={(e) => set('via', e.target.value)} aria-label={t('audit.via')}>
            <option value="">{t('audit.all_via')}</option>
            {['ui', 'api', 'mcp', 'cli'].map((v) => <option key={v} value={v}>{t('via.' + v)}</option>)}
          </Select>
          <Select className="hidden h-7 w-28 text-[13px] sm:block" value={sp.get('status') ?? ''} onChange={(e) => set('status', e.target.value)} aria-label={t('audit.status')}>
            <option value="">{t('audit.all_status')}</option>
            {['done', 'failed', 'reverted', 'pending_approval', 'rejected', 'running'].map((v) => <option key={v} value={v}>{t('status_change.' + v)}</option>)}
          </Select>
          <Button size="sm" onClick={() => csv(shown, pick)} aria-label={t('audit.export')}><Download className="size-3.5" /><span className="hidden sm:inline">{t('audit.export')}</span></Button>
        </>}>
        {shown.length === 0 && <Empty title={list.loading ? t('common.loading') : t('audit.empty')}>{t('audit.empty_hint')}</Empty>}
        {groupBySession(shown).map((x) => 'session' in x ? (
          <div key={x.session} className="border-b border-line last:border-b-0">
            <Row onClick={() => setExpanded((e) => ({ ...e, [x.session]: !e[x.session] }))}>
              <ChevronRight className={`size-4 text-mute transition-transform ${expanded[x.session] ? 'rotate-90' : ''}`} />
              <div className="min-w-0 flex-1">
                <div className="text-[13.5px] font-semibold">{t('audit.ai_group', { name: x.items[0].actor.name, n: x.items.length })}</div>
                <div className="truncate text-[12px] text-mute">{x.items[0].actor.intent || x.session} · {datetime(x.items[x.items.length - 1].ts)}</div>
              </div>
              <Pill>{t('via.' + x.items[0].actor.via)}</Pill>
            </Row>
            {expanded[x.session] && x.items.map((c) => row(c, true))}
          </div>
        ) : row(x))}
        {all.length >= 100 && <div className="p-3 text-center"><Button size="sm" onClick={() => void loadMore()}>{t('audit.load_more')}</Button></div>}
      </Box>
      <Drawer open={!!open} onOpenChange={(o) => !o && setOpen(null)} title={open ? pick(open.title) : ''}
        right={open && undoable(open) ? <Button size="sm" onClick={() => void act(() => post(`/api/v1/changes/${open.id}/revert`)).then(() => setOpen(null))}>{t('timeline.undo')}</Button> : undefined}>
        {open && <ChangeDetail c={open} />}
      </Drawer>
    </div>
  )
}
