import { useNavigate } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { bytes, pct } from '@/lib/format'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Container, Stat } from '@/lib/types'
import { ContainerMenu, Exposure, stateDot } from './common'

interface Listen { port: number; proto: string; loopback: boolean }

// InternalPorts shows ports a container listens on without publishing them (方案 §5.2.1).
function InternalPorts({ c }: { c: Container }) {
  const t = useT()
  const { host } = useHost()
  const l = useApi<{ items: Listen[] }>(c.state === 'running' ? `/api/v1/hosts/${host}/containers/${encodeURIComponent(c.name)}/listening` : null)
  const published = new Set(c.ports.map((p) => p.private))
  return <>{l.data?.items.filter((x) => !x.loopback && !published.has(x.port)).map((x) => <Pill key={x.port} className="num">{t('containers.internal', { port: x.port })}</Pill>)}</>
}

export function portText(p: Container['ports'][number]) {
  return p.public ? `${p.ip && p.ip !== '0.0.0.0' && p.ip !== '::' ? p.ip + ':' : ''}${p.public}→${p.private}/${p.proto}` : `${p.private}/${p.proto}`
}

export function ContainerList({ list, reload, onLogs }: { list: Container[]; reload: () => void; onLogs: (c: Container) => void }) {
  const t = useT()
  const nav = useNavigate()
  const { host } = useHost()
  const stats = useApi<{ items: Stat[] }>(`/api/v1/hosts/${host}/containers/stats`, [host])
  useInterval(() => void stats.reload(), 5000)
  const st = new Map(stats.data?.items.map((s) => [s.id, s]))
  const projects = [...new Set(list.filter((c) => c.project && !c.adopted).map((c) => c.project!))]
  return (
    <div className="space-y-3">
      {projects.length > 0 && (
        <Notice>
          {t('containers.discovered', { n: projects.length })}{' '}
          <Button size="sm" className="ml-2" onClick={() => nav(hostPath(host, 'containers/compose'))}>{t('containers.review')}</Button>
        </Notice>
      )}
      <Box>
        {list.length === 0 && <Empty title={t('containers.none')}>{t('containers.none_hint')}</Empty>}
        {list.length > 0 && (
          <div className="hidden border-b border-line bg-soft px-3.5 py-1.5 text-[12px] text-mute md:flex md:gap-3">
            <span className="w-2.5" /><span className="flex-1">{t('containers.col_name')}</span><span className="w-56">{t('containers.col_ports')}</span>
            <span className="w-14 text-right">CPU</span><span className="w-24 text-right">{t('overview.memory')}</span>
            <span className="hidden w-28 text-right xl:block">{t('overview.network')}</span><span className="hidden w-24 lg:block">{t('containers.col_project')}</span><span className="w-8" />
          </div>
        )}
        {list.map((c) => {
          const s = st.get(c.id)
          return (
            <Row key={c.id} onClick={() => nav(hostPath(host, 'containers/c/' + encodeURIComponent(c.name)))}>
              <Dot state={stateDot(c.state)} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13.5px] font-semibold" data-state={c.state}>{c.name}</div>
                <div className="num truncate text-[12px] text-mute">{c.image} · {c.status}</div>
                <Exposure c={c} />
              </div>
              <div className="hidden w-56 flex-wrap gap-1 md:flex">
                {c.ports.filter((p) => p.public).map((p) => <Pill key={portText(p)} className="num" tone={!p.ip || p.ip === '0.0.0.0' || p.ip === '::' ? 'warn' : undefined}>{portText(p)}</Pill>)}
                <InternalPorts c={c} />
              </div>
              <span className="num w-14 text-right text-[12px] max-sm:hidden">{s ? pct(s.cpu, 1) : "—"}</span>
              <span className="num w-24 text-right text-[12px]">
                {s ? bytes(s.mem_bytes) : '—'}
                {s && s.mem_limit > 0 && <span className="mt-0.5 block h-1 overflow-hidden rounded-full bg-soft"><span className="block h-full bg-acc" style={{ width: Math.min(100, (s.mem_bytes / s.mem_limit) * 100) + '%' }} /></span>}
              </span>
              <span className="num hidden w-28 text-right text-[12px] text-mute xl:block max-md:hidden">{s ? `↓${bytes(s.net_rx)} ↑${bytes(s.net_tx)}` : ''}</span>
              <span className="hidden w-24 truncate text-[12px] text-mute lg:block max-md:hidden">{c.project}</span>
              <ContainerMenu c={c} onDone={reload} onLogs={() => onLogs(c)} />
            </Row>
          )
        })}
      </Box>
    </div>
  )
}
