import { useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Textarea } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { ago } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Upd { container: string; project?: string; service?: string; image: string; update: boolean; error?: string }

// Image updates (方案 §5.3): what has a newer image upstream; updating a project pulls,
// recreates and rolls back by itself when a service does not come up.
export function UpdatesTab() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'containers.manage')
  const d = useApi<{ items: Upd[]; checked_at: number }>(`/api/v1/hosts/${host}/image-updates`, [host])
  useEvents(['change.done'], () => void d.reload())
  const [checking, setChecking] = useState(false)
  const refresh = async () => { setChecking(true); try { await get(`/api/v1/hosts/${host}/image-updates?refresh=1`); await d.reload() } finally { setChecking(false) } }
  const items = d.data?.items ?? []
  const byProject = new Map<string, Upd[]>()
  for (const u of items) { const k = u.project || ''; byProject.set(k, [...(byProject.get(k) ?? []), u]) }
  const withUpd = items.filter((u) => u.update).length
  return (
    <div className="space-y-4">
      <Box title={t('upd.title')} count={withUpd} right={<div className="flex items-center gap-2 text-[12px] text-mute">
        {d.data?.checked_at ? t('system.checked', { ago: ago(d.data.checked_at, t) }) : ''}
        <Button size="sm" variant="ghost" disabled={checking} onClick={() => void refresh()}><RefreshCw className={'size-3.5 ' + (checking ? 'animate-spin' : '')} />{t('system.check_now')}</Button>
      </div>}>
        {!d.data && <div className="px-4 py-3 text-[12.5px] text-mute">{d.error ? d.error.message : t('upd.checking')}</div>}
        {d.data && items.length === 0 && <Empty title={t('upd.none_running')} />}
        {[...byProject.entries()].sort(([, a], [, b]) => Number(b.some((x) => x.update)) - Number(a.some((x) => x.update))).map(([proj, us]) => (
          <div key={proj || '-'} className="border-b border-line px-4 py-2 last:border-b-0">
            <div className="flex items-center gap-2 text-[13px]">
              <b>{proj || t('upd.standalone')}</b>
              {us.some((u) => u.update) ? <Pill tone="acc">{t('upd.available')}</Pill> : <span className="text-[12px] text-mute">{t('upd.latest')}</span>}
              {manage && proj && us.some((u) => u.update) && <Button size="sm" variant="primary" className="ml-auto" onClick={() => run('compose.upgrade', { name: proj }, { host })}>{t('action.compose.upgrade')}</Button>}
            </div>
            {us.map((u) => (
              <div key={u.container} className="flex items-center gap-2 pl-2 text-[12.5px]">
                <Dot state={u.error ? 'warn' : u.update ? 'busy' : 'ok'} />
                <span className="w-40 truncate">{u.service || u.container}</span>
                <span className="num min-w-0 flex-1 truncate text-mute">{u.image}</span>
                {u.error && <span className="truncate text-[11.5px] text-warn" title={u.error}>{t('upd.cant_check')}</span>}
              </div>
            ))}
            {!proj && us.some((u) => u.update) && <div className="pl-2 text-[12px] text-mute">{t('upd.standalone_hint')}</div>}
          </div>
        ))}
      </Box>
      <RegistriesBox />
    </div>
  )
}

function RegistriesBox() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'containers.manage')
  const d = useApi<{ mirrors: string[]; logins: string[]; log_opts?: Record<string, string> }>(`/api/v1/hosts/${host}/registries`, [host])
  const [logSize, setLogSize] = useState<string | null>(null)
  useEvents(['change.done'], () => void d.reload())
  const [mir, setMir] = useState<string | null>(null)
  const [login, setLogin] = useState({ registry: '', user: '', password: '' })
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : null
  const cur = d.data.mirrors.join('\n')
  const val = mir ?? cur
  return (
    <Box title={t('upd.registries')}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <div>
          <div className="mb-1 font-semibold">{t('upd.mirrors')}</div>
          <p className="mb-1.5 text-[12px] text-mute">{t('upd.mirrors_hint')}</p>
          <Textarea className="num min-h-16 text-[12px]" disabled={!manage} value={val} placeholder="https://docker.m.daocloud.io" onChange={(e) => setMir(e.target.value)} />
          {manage && val !== cur && <Button size="sm" variant="primary" className="mt-1.5" onClick={() => run('docker.mirrors', { mirrors: val.split('\n').map((x) => x.trim()).filter(Boolean) }, { host, onDone: () => setMir(null) })}>{t('common.apply')}</Button>}
        </div>
        <div>
          <div className="mb-1 font-semibold">{t('upd.log_cap')}</div>
          <p className="mb-1.5 text-[12px] text-mute">{t('upd.log_cap_hint')}</p>
          <div className="flex items-center gap-2">
            <Input className="num w-28" disabled={!manage} value={logSize ?? d.data.log_opts?.['max-size'] ?? ''} placeholder="50m" onChange={(e) => setLogSize(e.target.value)} />
            <span className="text-[12px] text-mute">× {d.data.log_opts?.['max-file'] ?? '3'}</span>
            {manage && logSize !== null && <Button size="sm" variant="primary" onClick={() => run('docker.logs', { max_size: logSize.trim() }, { host, onDone: () => setLogSize(null) })}>{t('common.apply')}</Button>}
          </div>
        </div>
        <div>
          <div className="mb-1 font-semibold">{t('upd.logins')}</div>
          {d.data.logins.length === 0 && <p className="text-[12px] text-mute">{t('upd.no_logins')}</p>}
          {d.data.logins.map((r) => <div key={r} className="flex items-center gap-2"><span className="num flex-1">{r}</span>{manage && <button className="text-[12px] text-bad hover:underline" onClick={() => run('docker.logout', { registry: r }, { host })}>{t('action.docker.logout')}</button>}</div>)}
          {manage && <div className="mt-2 flex flex-wrap gap-2">
            <Input className="num w-56" placeholder="ghcr.io" value={login.registry} onChange={(e) => setLogin({ ...login, registry: e.target.value })} aria-label={t('upd.registry')} />
            <Input className="w-36" placeholder={t('login.username')} value={login.user} onChange={(e) => setLogin({ ...login, user: e.target.value })} />
            <Input className="w-44" type="password" autoComplete="off" placeholder={t('upd.password')} value={login.password} onChange={(e) => setLogin({ ...login, password: e.target.value })} />
            <Button size="sm" disabled={!login.registry || !login.user || !login.password} onClick={() => run('docker.login', login, { host, onDone: () => setLogin({ registry: '', user: '', password: '' }) })}>{t('action.docker.login')}</Button>
          </div>}
        </div>
      </div>
    </Box>
  )
}

// CPU and memory limits for each service of a compose project.
export function LimitsBox({ project, services }: { project: string; services: string[] }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [v, setV] = useState<Record<string, { cpus: string; memory: string }>>({})
  return (
    <div className="space-y-1.5">
      <div className="text-[12.5px] font-semibold">{t('upd.limits')}</div>
      {services.map((s) => {
        const x = v[s] ?? { cpus: '', memory: '' }
        return (
          <div key={s} className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <span className="w-32 truncate">{s}</span>
            <Input className="num w-20" placeholder="CPU" value={x.cpus} onChange={(e) => setV({ ...v, [s]: { ...x, cpus: e.target.value } })} aria-label={'CPU ' + s} />
            <Input className="num w-24" placeholder="512m" value={x.memory} onChange={(e) => setV({ ...v, [s]: { ...x, memory: e.target.value } })} aria-label={t('overview.memory') + ' ' + s} />
            <Button size="sm" onClick={() => run('compose.limits', { name: project, service: s, cpus: x.cpus, memory: x.memory }, { host })}>{t('common.apply')}</Button>
          </div>
        )
      })}
      <p className="text-[11.5px] text-mute">{t('upd.limits_hint')}</p>
    </div>
  )
}
