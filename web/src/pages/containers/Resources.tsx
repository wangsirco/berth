import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Switch } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { ago, bytes } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useState } from 'react'

interface Image { id: string; tags: string[]; size: number; created: number; dangling: boolean; used_by: string[] }
interface Network { id: string; name: string; driver: string; subnet?: string; containers: string[]; builtin: boolean }
interface Volume { name: string; driver: string; mountpoint: string; project?: string; containers: string[] }

export function ImagesTab() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'containers.manage')
  const l = useApi<{ items: Image[] }>(`/api/v1/hosts/${host}/images`, [host])
  const [ref, setRef] = useState('')
  const items = (l.data?.items ?? []).sort((a, b) => b.size - a.size)
  return (
    <Box title={t('containers.tab_images')} count={items.length} right={manage && <>
      <Input className="h-7 w-48 text-[13px]" placeholder="nginx:alpine" value={ref} onChange={(e) => setRef(e.target.value)} aria-label={t('images.ref')} />
      <Button size="sm" disabled={!ref} onClick={() => run('image.pull', { ref }, { host, onDone: () => { setRef(''); void l.reload() } })}>{t('action.image.pull')}</Button>
      <Button size="sm" onClick={() => run('image.prune', {}, { host, onDone: () => void l.reload() })}>{t('action.image.prune')}</Button>
    </>}>
      {items.length === 0 && <Empty title={t('images.none')} />}
      {items.map((im) => (
        <Row key={im.id}>
          <div className="min-w-0 flex-1">
            <div className="num truncate text-[13px] font-semibold">{im.tags.join(', ') || im.id.slice(7, 19)}</div>
            <div className="text-[12px] text-mute">{ago(im.created, t)}{im.used_by.length ? ' · ' + t('images.used_by', { names: im.used_by.join(', ') }) : ''}</div>
          </div>
          {im.dangling && <Pill>{t('images.dangling')}</Pill>}
          <span className="num w-20 text-right text-[12px]">{bytes(im.size)}</span>
          {manage && <Button size="sm" variant="ghost" disabled={im.used_by.length > 0} onClick={() => run('image.remove', { ref: im.tags[0] ?? im.id }, { host, onDone: () => void l.reload() })}>{t('common.delete')}</Button>}
        </Row>
      ))}
    </Box>
  )
}

export function NetworksTab() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const l = useApi<{ items: Network[] }>(`/api/v1/hosts/${host}/networks`, [host])
  const [name, setName] = useState('')
  const [subnet, setSubnet] = useState('')
  const [internal, setInternal] = useState(false)
  const ok = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/.test(name) && (!subnet || /^\d+\.\d+\.\d+\.\d+\/\d+$/.test(subnet))
  return (
    <Box title={t('containers.tab_networks')} count={l.data?.items.length}>
      {can(user, 'containers.manage') && <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2.5">
        <Input className="h-8 w-44" placeholder={t('networks.name_ph')} aria-label={t('networks.name_ph')} value={name} onChange={(e) => setName(e.target.value)} />
        <Input className="num h-8 w-44" placeholder={t('networks.subnet_ph')} aria-label={t('networks.subnet_ph')} value={subnet} onChange={(e) => setSubnet(e.target.value)} />
        <Switch checked={internal} onChange={setInternal} label={t('networks.internal')} />
        <Button size="sm" disabled={!ok} onClick={() => run('network.create', { name, subnet, internal }, { host, onDone: () => { setName(''); setSubnet(''); void l.reload() } })}>{t('action.network.create')}</Button>
        <span className="w-full text-[12px] text-mute">{t('networks.create_hint')} <code className="num">networks: {'{'} {name || 'my-net'}: {'{'} external: true {'}'} {'}'}</code></span>
      </div>}
      {l.data?.items.map((n) => (
        <Row key={n.id}>
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-semibold">{n.name} {n.builtin && <Pill>{t('networks.builtin')}</Pill>}</div>
            <div className="num truncate text-[12px] text-mute">{n.driver}{n.subnet && ' · ' + n.subnet}{n.containers.length ? ' · ' + n.containers.join(', ') : ''}</div>
          </div>
          {can(user, 'containers.manage') && !n.builtin && n.containers.length === 0 && <Button size="sm" variant="ghost" onClick={() => run('network.remove', { name: n.name }, { host, onDone: () => void l.reload() })}>{t('common.delete')}</Button>}
        </Row>
      ))}
    </Box>
  )
}

export function VolumesTab() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const l = useApi<{ items: Volume[] }>(`/api/v1/hosts/${host}/volumes`, [host])
  const [name, setName] = useState('')
  return (
    <Box title={t('containers.tab_volumes')} count={l.data?.items.length}>
      {can(user, 'containers.manage') && <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2.5">
        <Input className="h-8 w-52" placeholder={t('volumes.name_ph')} aria-label={t('volumes.name_ph')} value={name} onChange={(e) => setName(e.target.value)} />
        <Button size="sm" disabled={!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/.test(name)} onClick={() => run('volume.create', { name }, { host, onDone: () => { setName(''); void l.reload() } })}>{t('action.volume.create')}</Button>
        <span className="text-[12px] text-mute">{t('volumes.create_hint')}</span>
      </div>}
      {l.data?.items.length === 0 && <Empty title={t('volumes.none')} />}
      {l.data?.items.map((v) => (
        <Row key={v.name}>
          <div className="min-w-0 flex-1">
            <div className="num truncate text-[13px] font-semibold">{v.name}</div>
            <div className="num truncate text-[12px] text-mute">{v.project && v.project + ' · '}{v.containers.length ? v.containers.join(', ') : t('volumes.unused')}</div>
          </div>
          {can(user, 'containers.manage') && v.containers.length === 0 && <Button size="sm" variant="ghost" onClick={() => run('volume.remove', { name: v.name }, { host, onDone: () => void l.reload() })}>{t('common.delete')}</Button>}
        </Row>
      ))}
    </Box>
  )
}
