import { useEffect, useMemo, useState } from 'react'
import { MoreHorizontal, Play, Plus } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Segmented, Select, Textarea } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { post } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Action { type: 'script' | 'change'; script?: string; user?: string; timeout?: number; kind?: string; params?: unknown }
interface Run { id: number; job_id: number; started_at: number; ms: number; status: string; code: number; trigger: string; change_id?: string; output?: string }
interface Job { id: number; host: string; name: string; cron: string; action: Action; enabled: boolean; created_by: string; next?: number; last?: Run }
interface SysEntry { source: string; user: string; schedule: string; command: string }
interface Timer { unit: string; activates: string; next?: number; last?: number }
interface Data { items: Job[]; system: { entries: SysEntry[]; timers: Timer[] } | null }

// Berth actions people commonly put on a schedule; anything else can be typed in.
const COMMON_KINDS = ['backup.run', 'image.prune', 'system.updates', 'container.restart', 'compose.pull', 'registry.prune']

const runTone = (s?: string): 'ok' | 'bad' | 'busy' | 'off' | 'warn' => s === 'ok' ? 'ok' : s === 'failed' || s === 'interrupted' ? 'bad' : s === 'running' ? 'busy' : s === 'pending' || s === 'skipped' ? 'warn' : 'off'

// Scheduled jobs (方案 §5.14): Berth's own, then what the system already schedules.
export function Jobs() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<Data>(`/api/v1/hosts/${host}/jobs`, [host])
  useEvents(['change.done', 'cron.run'], () => void d.reload())
  const [edit, setEdit] = useState<Job | 'new' | null>(null)
  const [runs, setRuns] = useState<Job | null>(null)
  const [showSys, setShowSys] = useState(false)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const sys = d.data.system
  const sysCount = (sys?.entries.length ?? 0) + (sys?.timers.length ?? 0)
  return (
    <div className="space-y-4">
      <Box title={t('jobs.title')} count={d.data.items.length} right={manage && <Button size="sm" variant="primary" onClick={() => setEdit('new')}><Plus className="size-3.5" />{t('jobs.add')}</Button>}>
        {d.data.items.length === 0 && <Empty title={t('jobs.empty')}>{t('jobs.empty_hint')}</Empty>}
        {d.data.items.map((j) => (
          <Row key={j.id} onClick={() => setRuns(j)}>
            <Dot state={j.enabled ? runTone(j.last?.status) : 'off'} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 text-[13px] font-semibold"><span className="truncate">{j.name}</span>{!j.enabled && <Pill>{t('jobs.paused')}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">
                {j.action.type === 'script' ? `${j.action.user || 'root'} $ ${(j.action.script ?? '').split('\n')[0]}` : t('jobs.does', { kind: t('kind.' + j.action.kind) })}
              </div>
            </div>
            <span className="num w-32 text-[12px] text-mute max-md:hidden" title={t('jobs.expr')}>{j.cron}</span>
            <span className="w-36 text-[12px] max-sm:hidden">{j.next ? t('jobs.next', { at: datetime(j.next) }) : '—'}</span>
            <span className="w-36 text-[12px] text-mute max-lg:hidden">{j.last ? `${t('jobs.st_' + j.last.status)} · ${datetime(j.last.started_at)}` : t('jobs.never')}</span>
            <span className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
              {manage && <Button size="sm" variant="ghost" aria-label={t('action.cron.run')} title={t('action.cron.run')} onClick={() => run('cron.run', { id: j.id }, { host })}><Play className="size-3.5" /></Button>}
              {manage && <Menu items={[
                { label: t('common.edit'), onClick: () => setEdit(j) },
                { label: j.enabled ? t('jobs.pause') : t('jobs.resume'), onClick: () => run('cron.set', { id: j.id, name: j.name, cron: j.cron, action: j.action, enabled: !j.enabled }, { host }) },
                { label: t('jobs.runs'), onClick: () => setRuns(j) },
                'sep',
                { label: t('action.cron.delete'), danger: true, onClick: () => run('cron.delete', { id: j.id }, { host }) },
              ]} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />}
            </span>
          </Row>
        ))}
      </Box>
      {sys && sysCount > 0 && (
        <Box title={t('jobs.system')} count={sysCount} right={<button className="text-[12.5px] text-acc hover:underline" onClick={() => setShowSys(!showSys)}>{showSys ? t('jobs.hide') : t('jobs.show')}</button>}>
          {!showSys ? <div className="px-4 py-2.5 text-[12.5px] text-mute">{t('jobs.system_hint')}</div> : <>
            {sys.entries.map((e, i) => (
              <div key={'c' + i} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0">
                <span className="num w-32 shrink-0">{e.schedule}</span>
                <span className="w-16 shrink-0 text-mute">{e.user}</span>
                <span className="num min-w-0 flex-1 truncate" title={e.command}>{e.command}</span>
                <span className="num w-40 truncate text-[11.5px] text-mute max-md:hidden">{e.source}</span>
              </div>
            ))}
            {sys.timers.map((tm) => (
              <div key={tm.unit} className="flex items-center gap-3 border-b border-line px-4 py-1.5 text-[12.5px] last:border-b-0">
                <span className="w-32 shrink-0 text-[12px]">{tm.next ? datetime(tm.next) : '—'}</span>
                <span className="w-16 shrink-0 text-mute">timer</span>
                <span className="num min-w-0 flex-1 truncate">{tm.activates.replace(/\.service$/, '')}</span>
                <span className="w-40 text-[11.5px] text-mute max-md:hidden">{tm.last ? t('jobs.last_at', { at: datetime(tm.last) }) : ''}</span>
              </div>
            ))}
          </>}
        </Box>
      )}
      {edit && <JobDialog job={edit === 'new' ? null : edit} onClose={() => setEdit(null)} />}
      {runs && <RunsDrawer job={runs} onClose={() => setRuns(null)} />}
    </div>
  )
}

type Preset = 'hourly' | 'daily' | 'weekly' | 'monthly' | 'custom'

function toCron(p: Preset, time: string, dow: string, dom: string, custom: string): string {
  const [h, m] = (time || '03:00').split(':').map((x) => String(Number(x)))
  switch (p) {
    case 'hourly': return `${m} * * * *`
    case 'daily': return `${m} ${h} * * *`
    case 'weekly': return `${m} ${h} * * ${dow}`
    case 'monthly': return `${m} ${h} ${dom} * *`
    default: return custom
  }
}

function fromCron(c: string): { p: Preset; time: string; dow: string; dom: string } {
  const f = c.trim().split(/\s+/)
  const pad = (x: string) => x.padStart(2, '0')
  if (f.length === 5 && /^\d+$/.test(f[0])) {
    if (f[1] === '*' && f[2] === '*' && f[3] === '*' && f[4] === '*') return { p: 'hourly', time: '00:' + pad(f[0]), dow: '1', dom: '1' }
    if (/^\d+$/.test(f[1]) && f[3] === '*') {
      const time = pad(f[1]) + ':' + pad(f[0])
      if (f[2] === '*' && f[4] === '*') return { p: 'daily', time, dow: '1', dom: '1' }
      if (f[2] === '*' && /^\d$/.test(f[4])) return { p: 'weekly', time, dow: f[4], dom: '1' }
      if (/^\d+$/.test(f[2]) && f[4] === '*') return { p: 'monthly', time, dow: '1', dom: f[2] }
    }
  }
  return { p: 'custom', time: '03:00', dow: '1', dom: '1' }
}

function JobDialog({ job, onClose }: { job: Job | null; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const init = fromCron(job?.cron ?? '0 3 * * *')
  const [name, setName] = useState(job?.name ?? '')
  const [preset, setPreset] = useState<Preset>(init.p)
  const [time, setTime] = useState(init.time)
  const [dow, setDow] = useState(init.dow)
  const [dom, setDom] = useState(init.dom)
  const [custom, setCustom] = useState(job?.cron ?? '*/30 * * * *')
  const [type, setType] = useState<'script' | 'change'>(job?.action.type ?? 'script')
  const [script, setScript] = useState(job?.action.script ?? '')
  const [runAs, setRunAs] = useState(job?.action.user ?? 'root')
  const [timeout, setTimeout_] = useState(String((job?.action.timeout ?? 3600) / 60))
  const [kind, setKind] = useState(job?.action.kind ?? 'backup.run')
  const [params, setParams] = useState(job?.action.params ? JSON.stringify(job.action.params, null, 2) : '{}')
  const [next, setNext] = useState<number[] | string>([])
  const cron = toCron(preset, time, dow, dom, custom)
  useEffect(() => {
    const id = setTimeout(() => {
      post<{ next: number[] }>('/api/v1/jobs/next', { cron }).then((r) => setNext(r.next)).catch((e: Error) => setNext(e.message))
    }, 250)
    return () => clearTimeout(id)
  }, [cron])
  const paramsOK = useMemo(() => { try { JSON.parse(params || '{}'); return true } catch { return false } }, [params])
  const save = () => {
    const action: Action = type === 'script'
      ? { type, script, user: runAs.trim() || 'root', timeout: Math.round(Number(timeout || '60') * 60) }
      : { type, kind: kind.trim(), params: JSON.parse(params || '{}') }
    run('cron.set', { ...(job ? { id: job.id, enabled: job.enabled } : {}), name: name.trim(), cron, action }, { host, onDone: onClose })
  }
  const ok = name.trim() !== '' && typeof next !== 'string' && (type === 'script' ? script.trim() !== '' : kind.trim() !== '' && paramsOK)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={job ? t('jobs.edit') : t('jobs.add')} wide
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!ok} onClick={save}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Field label={t('jobs.name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('jobs.name_ph')} autoFocus /></Field>
        <Field label={t('jobs.when')} hint={typeof next === 'string' ? <span className="text-bad">{next}</span> : next.length > 0 ? t('jobs.next_runs', { list: next.slice(0, 3).map(datetime).join(' · ') }) : ''}>
          <div className="space-y-2">
            <Segmented<Preset> value={preset} onChange={setPreset} options={(['hourly', 'daily', 'weekly', 'monthly', 'custom'] as Preset[]).map((p) => ({ value: p, label: t('jobs.p_' + p) }))} />
            <div className="flex flex-wrap items-center gap-2 text-[13px]">
              {preset === 'weekly' && <Select value={dow} onChange={(e) => setDow(e.target.value)}>{[1, 2, 3, 4, 5, 6, 0].map((d) => <option key={d} value={d}>{t('jobs.dow_' + d)}</option>)}</Select>}
              {preset === 'monthly' && <Select value={dom} onChange={(e) => setDom(e.target.value)}>{Array.from({ length: 28 }, (_, i) => i + 1).map((d) => <option key={d} value={d}>{t('jobs.dom', { d })}</option>)}</Select>}
              {preset === 'hourly' && <><span>{t('jobs.at_minute')}</span><Input className="w-20" type="number" min={0} max={59} value={Number(time.split(':')[1])} onChange={(e) => setTime('00:' + String(Math.min(59, Math.max(0, Number(e.target.value)))).padStart(2, '0'))} /></>}
              {(preset === 'daily' || preset === 'weekly' || preset === 'monthly') && <Input className="w-28" type="time" value={time} onChange={(e) => setTime(e.target.value)} />}
              {preset === 'custom' && <Input className="num w-56" value={custom} onChange={(e) => setCustom(e.target.value)} placeholder="*/30 * * * *" />}
              <span className="num text-[12px] text-mute">{cron}</span>
            </div>
          </div>
        </Field>
        <Field label={t('jobs.what')}>
          <Segmented value={type} onChange={setType} options={[{ value: 'script', label: t('jobs.t_script') }, { value: 'change', label: t('jobs.t_change') }]} />
        </Field>
        {type === 'script' ? <>
          <Field label={t('jobs.script')} hint={t('jobs.script_hint')}><Textarea className="num min-h-36 text-[12.5px]" value={script} onChange={(e) => setScript(e.target.value)} placeholder={'#!/bin/bash\n/usr/local/bin/cleanup.sh'} /></Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('jobs.run_as')}><Input value={runAs} onChange={(e) => setRunAs(e.target.value)} /></Field>
            <Field label={t('jobs.timeout')}><Input type="number" min={1} max={1440} value={timeout} onChange={(e) => setTimeout_(e.target.value)} /></Field>
          </div>
        </> : <>
          <Field label={t('jobs.kind')} hint={t('jobs.kind_hint')}>
            <Input list="job-kinds" value={kind} onChange={(e) => setKind(e.target.value)} />
            <datalist id="job-kinds">{COMMON_KINDS.map((k) => <option key={k} value={k}>{t('kind.' + k)}</option>)}</datalist>
          </Field>
          <Field label={t('jobs.params')} hint={paramsOK ? t('jobs.params_hint') : <span className="text-bad">{t('jobs.params_bad')}</span>}>
            <Textarea className="num min-h-24 text-[12.5px]" value={params} onChange={(e) => setParams(e.target.value)} />
          </Field>
        </>}
      </div>
    </Dialog>
  )
}

function RunsDrawer({ job, onClose }: { job: Job; onClose: () => void }) {
  const t = useT()
  const { host } = useHost()
  const d = useApi<{ items: Run[] }>(`/api/v1/hosts/${host}/jobs/${job.id}/runs`, [job.id])
  useEvents(['cron.run'], (_t, e: { job_id: number }) => { if (e.job_id === job.id) void d.reload() })
  const [open, setOpen] = useState<number | null>(null)
  const items = d.data?.items ?? []
  useEffect(() => { if (open === null && items.length) setOpen(items[0].id) }, [items, open])
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={job.name}>
      <div className="space-y-0">
        {items.length === 0 && <Empty title={t('jobs.no_runs')} />}
        {items.map((r) => (
          <div key={r.id} className="border-b border-line">
            <button className="flex w-full items-center gap-3 px-4 py-2 text-left text-[12.5px] hover:bg-soft" onClick={() => setOpen(open === r.id ? null : r.id)}>
              <Dot state={runTone(r.status)} />
              <span className="w-28">{datetime(r.started_at)}</span>
              <span className="flex-1">{t('jobs.st_' + r.status)}{r.status === 'failed' && r.code > 0 ? ` · ${t('jobs.code', { n: r.code })}` : ''}</span>
              <span className="text-mute">{t('jobs.trig_' + r.trigger)}</span>
              <span className="num w-16 text-right text-mute">{r.ms ? (r.ms / 1000).toFixed(1) + ' s' : ''}</span>
            </button>
            {open === r.id && <pre className="num max-h-80 overflow-auto whitespace-pre-wrap bg-term px-4 py-2 text-[11.5px] leading-relaxed text-term-fg">{r.output || (r.change_id ? t('jobs.see_change', { id: r.change_id }) : t('tasks.no_log'))}</pre>}
          </div>
        ))}
      </div>
    </Drawer>
  )
}
