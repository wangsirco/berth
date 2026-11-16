import { Play, Plus, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { FindingList } from './native/Native'
import type { Site } from './types'

interface Worker { name: string; command: string }
interface Report { framework: string; version: string; native: boolean; root_sub: string; extensions: string[]; workers: Worker[]; schedule?: string; findings: { level: 'ok' | 'warn' | 'block'; code: string; params?: Record<string, string> }[] }
interface Status { fpm: string; port: number; user: string; workers: Record<string, string>; schedule?: string }
interface Runtime { version: string; installed: boolean }

const INI = ['memory_limit', 'upload_max_filesize', 'post_max_size', 'max_execution_time', 'max_input_vars', 'display_errors'] as const
const INI_DEF: Record<string, string> = { memory_limit: '256M', upload_max_filesize: '64M', post_max_size: '64M', max_execution_time: '120', max_input_vars: '3000', display_errors: 'Off' }
const dotted = (tag?: string) => (tag && tag.length === 2 ? tag[0] + '.' + tag[1] : tag ?? '')

// The PHP tab of a site: how it runs (shared container or its own sandboxed FPM), moving
// between the two, the compatibility check, its own php.ini, workers, the scheduler, commands.
export function PhpSite({ s }: { s: Site }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<{ native: boolean; check?: Report; status?: Status }>(`/api/v1/hosts/${host}/sites/${s.id}/php`, [s.id])
  const rts = useApi<{ items: Runtime[] }>(`/api/v1/hosts/${host}/php/native`, [host])
  useEvents(['change.done'], () => { void d.reload(); void rts.reload() })
  const tg = s.target
  const [ini, setIni] = useState<Record<string, string>>({})
  const [workers, setWorkers] = useState<Worker[]>([])
  const [schedule, setSchedule] = useState('')
  const [rootSub, setRootSub] = useState('')
  const [cmd, setCmd] = useState('')
  useEffect(() => {
    setIni({ ...INI_DEF, ...(tg.php_ini ?? {}) }); setWorkers(tg.php_workers ?? []); setSchedule(tg.schedule ?? ''); setRootSub(tg.root_sub ?? '')
  }, [s]) // eslint-disable-line react-hooks/exhaustive-deps
  const update = (patch: Partial<Site['target']>) => run('site.update', { ...s, target: { ...tg, ...patch } }, { host })
  const ver = dotted(tg.php)
  const nativeOK = (rts.data?.items ?? []).some((r) => r.version === ver && r.installed)
  const st = d.data?.status
  const rep = d.data?.check
  const changed = JSON.stringify(ini) !== JSON.stringify({ ...INI_DEF, ...(tg.php_ini ?? {}) }) || JSON.stringify(workers) !== JSON.stringify(tg.php_workers ?? []) || schedule !== (tg.schedule ?? '') || rootSub !== (tg.root_sub ?? '')
  const save = () => update({ php_ini: ini, php_workers: workers.filter((w) => w.name && w.command), schedule: schedule.trim(), root_sub: rootSub.trim() })
  return (
    <div className="space-y-4 p-4">
      {/* how it runs */}
      <div className="rounded-xl border border-line bg-canvas p-3.5">
        <div className="flex flex-wrap items-center gap-2 text-[13.5px]">
          <b>PHP {ver}</b>
          {tg.native ? <Pill tone="acc">{t('phps.native')}</Pill> : <Pill>{t('phps.container')}</Pill>}
          {tg.native && st && <span className="flex items-center gap-1.5 text-[12.5px] text-mute"><Dot state={st.fpm === 'active' ? 'ok' : 'bad'} />{t('phps.fpm', { state: st.fpm, user: st.user })}</span>}
          {manage && <div className="ml-auto">
            {!tg.native && (nativeOK
              ? <Button size="sm" variant="primary" onClick={() => update({ native: true })}>{t('phps.to_native')}</Button>
              : <span className="text-[12px] text-mute">{['8.2', '8.3', '8.4', '8.5'].includes(ver) ? t('phps.install_first', { v: ver }) : t('phps.old_version')}</span>)}
            {tg.native && <Button size="sm" onClick={() => update({ native: false, php_workers: [], schedule: '' })}>{t('phps.to_container')}</Button>}
          </div>}
        </div>
        <p className="mt-1.5 text-[12.5px] text-mute">{tg.native ? t('phps.native_hint') : t('phps.container_hint')}</p>
      </div>

      {/* compatibility check */}
      {rep && <Box title={t('phps.check')} right={rep.framework && <Pill>{t('phps.fw_' + rep.framework)}</Pill>}>
        <div className="px-4 py-2"><FindingList findings={rep.findings} /></div>
        {manage && tg.native && (rep.root_sub !== (tg.root_sub ?? '') || (rep.workers.length > 0 && (tg.php_workers ?? []).length === 0) || (rep.schedule && !tg.schedule)) &&
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5 text-[12.5px]">
            <span className="text-mute">{t('phps.suggest', { root: rep.root_sub || '/', n: rep.workers.length + (rep.schedule ? 1 : 0) })}</span>
            <Button size="sm" className="ml-auto" onClick={() => update({ root_sub: rep.root_sub, php_workers: rep.workers, schedule: rep.schedule ?? '' })}>{t('phps.apply_suggest')}</Button>
          </div>}
      </Box>}

      {/* settings (native only) */}
      {tg.native && <Box title={t('phps.settings')}>
        <div className="grid gap-x-4 gap-y-2 px-4 py-3 sm:grid-cols-2">
          {INI.map((k) => (
            <label key={k} className="grid grid-cols-[150px_1fr] items-center gap-2 text-[13px]"><span className="num text-mute">{k}</span>
              {k === 'display_errors'
                ? <Select disabled={!manage} value={ini[k] ?? 'Off'} onChange={(e) => setIni({ ...ini, [k]: e.target.value })}><option>Off</option><option>On</option></Select>
                : <Input className="num h-8" disabled={!manage} value={ini[k] ?? ''} onChange={(e) => setIni({ ...ini, [k]: e.target.value.trim() })} aria-label={k} />}
            </label>
          ))}
          <label className="grid grid-cols-[150px_1fr] items-center gap-2 text-[13px]"><span className="text-mute">{t('phps.root_sub')}</span>
            <Input className="num h-8" disabled={!manage} value={rootSub} placeholder="public" onChange={(e) => setRootSub(e.target.value)} aria-label={t('phps.root_sub')} /></label>
        </div>
        <div className="border-t border-line px-4 py-3">
          <div className="mb-1.5 text-[13px] font-semibold">{t('phps.workers')}</div>
          <p className="mb-2 text-[12px] text-mute">{t('phps.workers_hint')}</p>
          {workers.map((w, i) => (
            <div key={i} className="mb-1.5 flex items-center gap-2">
              <Dot state={st?.workers[w.name] === 'active' ? 'ok' : st?.workers[w.name] ? 'bad' : 'off'} />
              <Input className="num h-8 w-28" disabled={!manage} value={w.name} placeholder="queue" onChange={(e) => setWorkers(workers.map((x, j) => (j === i ? { ...x, name: e.target.value.toLowerCase() } : x)))} />
              <Input className="num h-8 flex-1" disabled={!manage} value={w.command} placeholder="php artisan queue:work" onChange={(e) => setWorkers(workers.map((x, j) => (j === i ? { ...x, command: e.target.value } : x)))} />
              {manage && <Button size="sm" variant="ghost" aria-label={t('common.delete')} onClick={() => setWorkers(workers.filter((_, j) => j !== i))}><Trash2 className="size-4" /></Button>}
            </div>
          ))}
          {manage && <Button size="sm" variant="ghost" onClick={() => setWorkers([...workers, { name: '', command: '' }])}><Plus className="size-3.5" />{t('phps.add_worker')}</Button>}
          <label className="mt-3 grid grid-cols-[150px_1fr] items-center gap-2 text-[13px]"><span className="text-mute">{t('phps.schedule')}</span>
            <Input className="num h-8" disabled={!manage} value={schedule} placeholder="php artisan schedule:run" onChange={(e) => setSchedule(e.target.value)} aria-label={t('phps.schedule')} /></label>
          {st?.schedule && <p className="mt-1 text-[12px] text-mute">{t('phps.schedule_last', { r: st.schedule })}</p>}
        </div>
        {manage && changed && <div className="border-t border-line px-4 py-2.5"><Button variant="primary" onClick={save}>{t('phps.save')}</Button></div>}
      </Box>}

      {/* a command in the site's folder */}
      {tg.native && manage && <Box title={t('phps.run')}>
        <div className="flex gap-2 px-4 py-3">
          <Input className="num flex-1" value={cmd} placeholder="composer install --no-dev -o" onChange={(e) => setCmd(e.target.value)} aria-label={t('phps.run')} />
          <Button disabled={!cmd.trim()} onClick={() => run('site.php_run', { id: s.id, command: cmd.trim() }, { host })}><Play className="size-3.5" />{t('action.site.php_run')}</Button>
        </div>
        <p className="px-4 pb-3 text-[12px] text-mute">{t('phps.run_hint', { user: st?.user ?? 'site-web' + s.id })}</p>
      </Box>}
      {d.error && <Notice tone="warn">{d.error.message}</Notice>}
    </div>
  )
}
