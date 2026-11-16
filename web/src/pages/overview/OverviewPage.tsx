import { useEffect, useState } from 'react'
import { AIActivityCard } from '@/components/ai/Activity'
import { useHost } from '@/components/shell/host'
import { Notice } from '@/components/ui/Box'
import { useApi, useInterval } from '@/lib/hooks'
import type { Berth, Host, TaskView } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { fixHost, TaskStrip } from './HostDashboard'
import { AppsVms, Greeting, ServerBox, SiteCards, Todo } from './home/Home'

interface Overview { host: Host; berths: Berth[]; tasks: TaskView[]; berths_error?: string }

// 首页：先说要处理什么，再是网站、应用与虚拟机、这台服务器。
export default function OverviewPage() {
  const { host } = useHost()
  const ov = useApi<Overview>(`/api/v1/hosts/${encodeURIComponent(host)}/overview`, [host])
  const [cur, setCur] = useState<Host>()
  useEffect(() => { setCur(undefined) }, [host])
  useEffect(() => { if (ov.data) setCur((c) => c ?? fixHost(ov.data!.host)) }, [ov.data])
  useInterval(() => void ov.reload(), 15000)
  useEvents(['metrics.tick'], (_t, e: { host: string; sample: Host }) => { if (e.host === host) setCur(fixHost(e.sample)) })
  useEvents(['task.update'], () => void ov.reload())
  return (
    <div>
      <Greeting />
      <AIActivityCard />
      <Todo />
      {ov.error && <div className="mb-5"><Notice tone="bad">{ov.error.message}</Notice></div>}
      <div className="mb-5"><TaskStrip tasks={ov.data?.tasks ?? []} /></div>
      <SiteCards />
      <div className="grid gap-3.5 lg:grid-cols-[1.25fr_1fr]">
        <AppsVms berths={ov.data?.berths} />
        <ServerBox host={cur} />
      </div>
    </div>
  )
}
