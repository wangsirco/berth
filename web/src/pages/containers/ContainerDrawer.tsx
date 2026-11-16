import { useState } from 'react'
import { ObjectActivity } from '@/components/ai/Activity'
import { Link } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { AskAIButton } from '@/components/ai/AiDrawer'
import { Button } from '@/components/ui/Button'
import { Drawer } from '@/components/ui/Dialog'
import { Pill } from '@/components/ui/Box'
import { Segmented } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Container } from '@/lib/types'
import { NewSiteDialog } from '../sites/NewSiteDialog'
import { Exposure, openTerminal } from './common'
import { portText } from './ContainerList'
import { LogsView } from './LogsView'
import { FlowsView } from '@/components/FlowsView'

interface Detail { container: Container & { env: string[]; cmd: string[]; health?: string; oom_killed: boolean; started_at?: string; compose_file?: string; runtime?: string }; relations: { from: { type: string; id: string }; to: { type: string; id: string }; kind: string }[]; access?: Pick<Container, 'exposure' | 'sites' | 'wan'> }

export function ContainerDrawer({ name, tab: initial = 'info', onClose, onChanged }: { name: string; tab?: 'info' | 'logs'; onClose: () => void; onChanged: () => void }) {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const [tab, setTab] = useState<'info' | 'logs' | 'rel' | 'flows'>(initial)
  const d = useApi<Detail>(`/api/v1/hosts/${host}/containers/${encodeURIComponent(name)}`, [name])
  const listen = useApi<{ items: { port: number; loopback: boolean }[] }>(`/api/v1/hosts/${host}/containers/${encodeURIComponent(name)}/listening`, [name])
  const compat = useApi<{ page_size: number; needed: boolean; runtime: boolean }>(`/api/v1/hosts/${host}/compat`, [host])
  const c = d.data?.container
  const [siteFor, setSiteFor] = useState(false)
  const webPort = c?.ports.find((p) => p.private)?.private ?? listen.data?.items.find((l) => !l.loopback)?.port
  const manage = can(user, 'containers.manage')
  const done = () => { void d.reload(); onChanged() }
  const kv = (k: string, v: React.ReactNode) => (
    <div className="grid grid-cols-[110px_1fr] gap-2 border-b border-line px-4 py-2 text-[13px]"><span className="text-mute">{k}</span><span className="min-w-0 break-words">{v}</span></div>
  )
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={name}
      right={c && manage && (
        <div className="flex gap-1.5">
          {c.state === 'running' ? <>
            <Button size="sm" onClick={() => run('container.restart', { name }, { host, onDone: done })}>{t('action.container.restart')}</Button>
            <Button size="sm" onClick={() => run('container.stop', { name }, { host, onDone: done })}>{t('action.container.stop')}</Button>
            {can(user, 'terminal.use') && c.runtime !== 'krun' && <Button size="sm" onClick={() => openTerminal(name)}>{t('containers.shell')}</Button>}
          </> : <>
            <AskAIButton question={t('ai.q_container', { name })} context={`container ${name} on host ${host}: state ${c.state}, ${c.status}${c.oom_killed ? ', killed: out of memory' : ''}`} />
            <Button size="sm" variant="primary" onClick={() => run('container.start', { name }, { host, onDone: done })}>{t('action.container.start')}</Button>
          </>}
        </div>
      )}>
      <ObjectActivity type="container" id={name} className="mx-4 mt-3" />
      <div className="border-b border-line px-4 py-2">
        <Segmented value={tab} onChange={setTab} options={[{ value: 'info', label: t('containers.tab_info') }, { value: 'logs', label: t('containers.logs') }, { value: 'flows', label: t('flows.tab') }, { value: 'rel', label: t('containers.tab_relations') }]} />
      </div>
      {siteFor && c && webPort && <NewSiteDialog preset={{ type: 'container', container: name, port: webPort }} onClose={() => setSiteFor(false)} onDone={() => { setSiteFor(false); done() }} />}
      {tab === 'logs' && <LogsView host={host} name={name} />}
      {tab === 'flows' && <FlowsView obj={'container:' + name} />}
      {tab === 'info' && c && (
        <div className="pb-4">
          {kv(t('containers.state'), <><Pill tone={c.state === 'running' ? 'ok' : undefined}>{c.state}</Pill> <span className="text-mute">{c.status}</span>{c.health && <Pill className="ml-1">{c.health}</Pill>}{c.oom_killed && <Pill tone="bad" className="ml-1">OOM</Pill>}</>)}
          {kv(t('containers.image'), <span className="num">{c.image}</span>)}
          {c.project && kv(t('containers.col_project'), <Link className="text-acc hover:underline" to={hostPath(host, 'containers/compose/' + encodeURIComponent(c.project))}>{c.project}</Link>)}
          {kv(t('containers.access'), <div className="space-y-1">
            <Exposure c={{ ...c, ...d.data?.access }} full />
            {can(user, 'sites.manage') && webPort && d.data?.access?.exposure !== 'public' && (
              <div><Button size="sm" onClick={() => setSiteFor(true)}>{t('containers.make_site', { port: webPort })}</Button></div>
            )}
          </div>)}
          {kv(t('containers.col_ports'), <div className="flex flex-wrap gap-1">
            {c.ports.map((p) => <Pill key={portText(p)} className="num">{portText(p)}</Pill>)}
            {listen.data?.items.map((l) => <Pill key={'l' + l.port} className="num">{l.loopback ? t('containers.loopback', { port: l.port }) : t('containers.internal', { port: l.port })}</Pill>)}
          </div>)}
          {kv(t('containers.networks'), c.networks.join(', '))}
          {kv(t('containers.mounts'), <div className="num space-y-0.5 text-[12px]">{c.mounts.map((m) => <div key={m.destination}>{m.name || m.source} → {m.destination}{!m.rw && ' (ro)'}</div>)}</div>)}
          {kv(t('containers.restarts'), c.restart_count)}
          {compat.data?.needed && kv(t('compat.mode'), <div className="flex flex-wrap items-center gap-2">
            <span>{c.runtime === 'krun' ? t('compat.mode_krun') : t('compat.mode_normal', { k: compat.data.page_size / 1024 })}</span>
            {manage && c.project && c.service && (c.runtime === 'krun'
              ? <Button size="sm" onClick={() => run('compose.runtime', { name: c.project, service: c.service, runtime: '' }, { host, onDone: done })}>{t('compat.switch_normal')}</Button>
              : compat.data.runtime
                ? <Button size="sm" onClick={() => run('compose.runtime', { name: c.project, service: c.service, runtime: 'krun' }, { host, onDone: done })}>{t('compat.switch_krun')}</Button>
                : <Button size="sm" variant="ghost" onClick={() => run('compat.install', {}, { host, onDone: () => void compat.reload() })}>{t('action.compat.install')}</Button>)}
            <span className="w-full text-[12px] text-mute">{t('compat.mode_hint')}{c.runtime === 'krun' && ' ' + t('compat.no_exec')}</span>
          </div>)}
          {c.started_at && kv(t('containers.started'), datetime(Date.parse(c.started_at) / 1000))}
          {kv(t('containers.cmd'), <span className="num text-[12px]">{c.cmd?.join(' ')}</span>)}
          {kv(t('containers.env'), <div className="num space-y-0.5 text-[12px]">{c.env.map((e) => <div key={e} className="break-all">{e}</div>)}<p className="mt-1 font-sans text-mute">{t('containers.env_note')}</p></div>)}
        </div>
      )}
      {tab === 'rel' && (
        <div className="p-4 text-[13px]">
          {(d.data?.relations ?? []).length === 0 && <p className="text-mute">{t('containers.no_relations')}</p>}
          <ul className="space-y-1">
            {d.data?.relations.map((r, i) => {
              const other = r.from.type === 'container' && r.from.id === name ? r.to : r.from
              return <li key={i}><span className="text-mute">{t('rel.' + r.kind)}</span> {t('obj.' + other.type)} <b className="num font-medium">{other.id}</b></li>
            })}
          </ul>
        </div>
      )}
    </Drawer>
  )
}
