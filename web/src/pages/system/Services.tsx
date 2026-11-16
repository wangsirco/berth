import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { MoreHorizontal } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Input, Segmented, Switch } from '@/components/ui/Field'
import { Menu, type MenuEntry } from '@/components/ui/Menu'
import { can, useAuth } from '@/lib/auth'
import { bytes, datetime } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Service { name: string; description: string; load: string; active: string; sub: string; enabled?: string; mem_bytes?: number; protected?: string }
type Filter = 'running' | 'failed' | 'boot' | 'all'

export function Services() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const d = useApi<{ items: Service[] }>(`/api/v1/hosts/${host}/system/services`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<Filter>('running')
  const [logs, setLogs] = useState<string | null>(null)
  const items = d.data?.items ?? []
  const failed = items.filter((s) => s.active === 'failed').length
  const list = useMemo(() => items.filter((s) => {
    if (q && !(s.name + ' ' + s.description).toLowerCase().includes(q.toLowerCase())) return false
    if (filter === 'running') return s.active === 'active' || s.active === 'activating'
    if (filter === 'failed') return s.active === 'failed'
    if (filter === 'boot') return s.enabled === 'enabled'
    return true
  }), [items, q, filter])
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const act = (s: Service, action: string) => run('system.service', { name: s.name, action }, { host })
  return (
    <>
      {failed > 0 && filter !== 'failed' && <Notice tone="warn"><button className="hover:underline" onClick={() => setFilter('failed')}>{t('system.failed_services', { n: failed })}</button></Notice>}
      <Box title={t('system.tab_services')} count={list.length} right={
        <div className="flex items-center gap-2">
          <Input className="w-44" placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.target.value)} />
          <Segmented<Filter> value={filter} onChange={setFilter} options={(['running', 'failed', 'boot', 'all'] as Filter[]).map((f) => ({ value: f, label: t('system.svc_' + f) }))} />
        </div>}>
        {list.length === 0 && <Empty title={t('system.no_services')} />}
        {list.slice(0, 300).map((s) => {
          const items: MenuEntry[] = [
            { label: t('system.logs'), onClick: () => setLogs(s.name) },
            ...(manage ? [
              'sep' as const,
              ...(s.active === 'active' ? [{ label: t('system.svc_restart'), onClick: () => act(s, 'restart') }, { label: t('system.svc_stop'), onClick: () => act(s, 'stop'), danger: true, disabled: !!s.protected }]
                : [{ label: t('system.svc_start'), onClick: () => act(s, 'start') }]),
              s.enabled === 'enabled' ? { label: t('system.svc_disable'), onClick: () => act(s, 'disable'), danger: true, disabled: !!s.protected }
                : { label: t('system.svc_enable'), onClick: () => act(s, 'enable'), disabled: s.enabled === 'static' || s.enabled === 'masked' },
            ] : []),
          ]
          return (
            <Row key={s.name} onClick={() => setLogs(s.name)}>
              <Dot state={s.active === 'active' ? 'ok' : s.active === 'failed' ? 'bad' : s.active === 'activating' ? 'busy' : 'off'} />
              <div className="min-w-0 flex-1">
                <div className="num truncate text-[13px] font-semibold">{s.name.replace(/\.service$/, '')}</div>
                <div className="truncate text-[12px] text-mute">{s.description}</div>
              </div>
              {s.protected && <span className="max-sm:hidden" title={t('system.protected_hint')}><Pill>{t('system.protected')}</Pill></span>}
              <span className="w-20 text-[12px] text-mute max-sm:hidden">{s.enabled === 'enabled' ? t('system.at_boot') : ''}</span>
              <span className="w-24 text-[12px]">{t('system.state_' + s.active, { sub: s.sub })}</span>
              <span className="num w-16 text-right text-[12px] text-mute max-md:hidden">{s.mem_bytes ? bytes(s.mem_bytes) : ''}</span>
              <span onClick={(e) => e.stopPropagation()}>
                <Menu items={items} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />
              </span>
            </Row>
          )
        })}
      </Box>
      {logs && <LogsDrawer name={logs} onClose={() => setLogs(null)} />}
    </>
  )
}

function LogsDrawer({ name, onClose }: { name: string; onClose: () => void }) {
  const t = useT()
  const { host } = useHost()
  const d = useApi<{ text: string }>(`/api/v1/hosts/${host}/system/services/${encodeURIComponent(name)}/logs?lines=400`, [name])
  useInterval(() => void d.reload(), 5000)
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={name}>
      <pre className="num h-full overflow-auto whitespace-pre-wrap bg-term px-4 py-2 text-[11.5px] leading-relaxed text-term-fg">{d.data?.text || t('tasks.no_log')}</pre>
    </Drawer>
  )
}

interface Proc { pid: number; ppid: number; name: string; user: string; state: string; cpu: number; rss: number; threads: number; started: number; cmd: string; unit?: string; container?: string; instance?: string }

export function Processes() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'system.manage')
  const [live, setLive] = useState(true)
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<'cpu' | 'mem'>('cpu')
  const d = useApi<{ items: Proc[] }>(`/api/v1/hosts/${host}/system/processes`, [host])
  useInterval(() => void d.reload(), live ? 3000 : null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  const list = d.data.items
    .filter((p) => !q || (p.name + ' ' + p.cmd + ' ' + p.user + ' ' + (p.unit ?? '') + ' ' + (p.container ?? '')).toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => (sort === 'cpu' ? b.cpu - a.cpu || b.rss - a.rss : b.rss - a.rss))
    .slice(0, 150)
  return (
    <Box title={t('system.tab_processes')} count={d.data.items.length} right={
      <div className="flex items-center gap-2">
        <Input className="w-44" placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.target.value)} />
        <Segmented value={sort} onChange={setSort} options={[{ value: 'cpu', label: 'CPU' }, { value: 'mem', label: t('overview.memory') }]} />
        <Switch checked={live} onChange={setLive} label={t('system.live')} />
      </div>}>
      <div className="hidden border-b border-line bg-soft px-3.5 py-1.5 text-[12px] text-mute md:flex md:gap-3">
        <span className="w-16">PID</span><span className="flex-1">{t('system.process')}</span><span className="w-40">{t('system.belongs')}</span>
        <span className="w-20">{t('system.user')}</span><span className="w-14 text-right">CPU</span><span className="w-20 text-right">{t('overview.memory')}</span><span className="w-20" />
      </div>
      {list.map((p) => (
        <div key={p.pid} className="flex items-center gap-3 border-b border-line px-3.5 py-1.5 text-[12.5px] last:border-b-0" title={`${p.cmd}\n${t('system.started_at', { at: datetime(p.started) })} · ${t('system.threads', { n: p.threads })}`}>
          <span className="num w-16 text-mute">{p.pid}</span>
          <div className="min-w-0 flex-1"><div className="truncate font-semibold">{p.name}</div><div className="num truncate text-[11px] text-mute">{p.cmd}</div></div>
          <span className="w-40 truncate text-[12px] max-md:hidden">
            {p.container ? <Link className="text-acc hover:underline" to={hostPath(host, 'containers/c/' + encodeURIComponent(p.container))}>{t('flows.kind_container')} {p.container}</Link>
              : p.instance ? <Link className="text-acc hover:underline" to={hostPath(host, 'vms')}>{t('flows.kind_vm')} {p.instance}</Link>
                : p.unit ? <span className="text-mute">{p.unit.replace(/\.service$/, '')}</span> : ''}
          </span>
          <span className="w-20 truncate text-mute max-md:hidden">{p.user}</span>
          <span className="num w-14 text-right">{p.cpu.toFixed(1)}%</span>
          <span className="num w-20 text-right">{bytes(p.rss)}</span>
          <span className="w-20 text-right">
            {manage && <Menu items={[
              { label: t('system.end'), onClick: () => run('system.process.signal', { pid: p.pid, signal: 'TERM', name: p.name }, { host, onDone: () => void d.reload() }) },
              { label: t('system.kill'), danger: true, onClick: () => run('system.process.signal', { pid: p.pid, signal: 'KILL', name: p.name }, { host, onDone: () => void d.reload() }) },
            ]} trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} />}
          </span>
        </div>
      ))}
    </Box>
  )
}
