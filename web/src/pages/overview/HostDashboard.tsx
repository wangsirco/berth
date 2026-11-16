import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Steps } from '@/components/shell/TaskDrawer'
import { Timeline } from '@/components/Timeline'
import { Box, Dot, Empty, Notice, Row } from '@/components/ui/Box'
import { bytes, duration, pct } from '@/lib/format'
import { useApi, useInterval, useMedia } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Berth, Host, TaskView } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { BerthBar } from './BerthBar'
import { Dashboard } from './Dashboard'
import { ResourcePanel } from './ResourcePanel'

interface Overview { host: Host; berths: Berth[]; tasks: TaskView[]; berths_error?: string }

// fixHost: a remote agent may send null for lists it has nothing in.
export const fixHost = (h: Host): Host => ({ ...h, disks: h.disks ?? [], net: h.net ?? [], per_cpu: h.per_cpu ?? [], temps: h.temps ?? [] })

export function TaskStrip({ tasks }: { tasks: TaskView[] }) {
  const t = useT()
  const now = Date.now() / 1000
  const active = tasks.filter((x) => x.status === 'running' || x.status === 'waiting')
  if (!active.length) return null
  return (
    <div className="space-y-2">
      {active.map((x) => (
        <div key={x.id} className="flex flex-wrap items-center gap-3 rounded-[var(--radius-box)] border border-acc/40 bg-tint px-3.5 py-2.5 text-[13.5px]">
          <b className="font-semibold">{t('kind.' + x.kind)}</b>
          <span className="text-mute">
            {x.status === 'waiting' ? t(x.wait_reason ?? 'tasks.status.waiting', x.wait_params)
              : t('overview.step', { n: x.step + 1, of: Math.max(x.steps.length, 1), name: x.steps[x.step] ?? '' }) + ' · ' + duration(now - (x.started_at ?? now), t)}
          </span>
          <Steps task={x} />
        </div>
      ))}
    </div>
  )
}

export function HostDashboard() {
  const t = useT()
  const { host } = useHost()
  const ov = useApi<Overview>(`/api/v1/hosts/${encodeURIComponent(host)}/overview`, [host])
  const [live, setLive] = useState<Host[]>([])
  const [cur, setCur] = useState<Host>()
  const phone = useMedia('(max-width: 1023px)')
  useEffect(() => { setLive([]); setCur(undefined) }, [host])
  useEffect(() => { if (ov.data) setCur((c) => c ?? fixHost(ov.data!.host)) }, [ov.data])
  useInterval(() => void ov.reload(), 5000)
  useEvents(['metrics.tick'], (_t, e: { host: string; sample: Host }) => {
    if (e.host !== host) return
    const h = fixHost(e.sample)
    setCur(h)
    setLive((l) => [...l.slice(-179), h])
  })
  useEvents(['task.update'], () => void ov.reload())
  const d = ov.data
  const containers = (d?.berths ?? []).filter((b) => b.type === 'container').sort((a, b) => b.mem_bytes - a.mem_bytes)
  const aside = (
    <aside className="space-y-5">
      <ResourcePanel host={cur} hostName={host} live={live} />
      <section>
        <h2 className="mb-2 text-[13.5px] font-semibold">{t('overview.activity')}</h2>
        <Timeline />
      </section>
    </aside>
  )
  return (
    <div className="space-y-4">
      {ov.error && <Notice tone="bad">{ov.error.message}</Notice>}
      <Dashboard host={cur} live={live} />
      {d && <BerthBar berths={d.berths ?? []} host={cur ?? fixHost(d.host)} />}
      {d?.berths_error && <Notice tone="warn">{d.berths_error}</Notice>}
      <div className="grid gap-6 lg:grid-cols-[1fr_300px]">
        <div className="min-w-0 space-y-4">
          <TaskStrip tasks={d?.tasks ?? []} />
          {phone && aside}
          <Box title={t('nav.containers')} count={containers.length} right={<Link to={hostPath(host, 'containers')} className="text-acc hover:underline">{t('overview.all_containers')}</Link>}>
            {containers.length === 0 && <Empty title={t('overview.no_containers')} />}
            {containers.slice(0, 8).map((c) => (
              <Row key={c.id}>
                <Dot state="ok" />
                <Link to={hostPath(host, 'containers/c/' + encodeURIComponent(c.name))} className="min-w-0 flex-1 truncate text-[13.5px] font-semibold hover:underline">{c.name}</Link>
                <span className="hidden text-[12px] text-mute sm:inline">{c.project}</span>
                <span className="num w-20 text-right text-[12px]">{bytes(c.mem_bytes)}</span>
                <span className="num w-14 text-right text-[12px] text-mute">{pct(c.cpu, 1)}</span>
              </Row>
            ))}
          </Box>
        </div>
        {!phone && aside}
      </div>
    </div>
  )
}
