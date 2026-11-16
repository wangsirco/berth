import { useNavigate, useParams } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { Notice } from '@/components/ui/Box'
import { useApi, useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { Container } from '@/lib/types'
import { useEvents } from '@/lib/useEvents'
import { ComposeDrawer, ComposeList, type Project } from './Compose'
import { ContainerDrawer } from './ContainerDrawer'
import { ContainerList } from './ContainerList'
import { ImagesTab, NetworksTab, VolumesTab } from './Resources'
import { UpdatesTab } from './Upkeep'
import { TabBar } from '@/components/ui/Tabs'

// 应用 (compose projects, store apps among them) first, like 1Panel's 容器 → 编排
const TABS = ['', 'list', 'updates', 'images', 'networks', 'volumes'] as const

// Routes: containers (apps), containers/compose/:name, containers/list, containers/c/:name, containers/images…
export default function ContainersPage() {
  const t = useT()
  const nav = useNavigate()
  const { host } = useHost()
  const rest = (useParams()['*'] ?? '').split('/')
  const tab = rest[0] === 'c' ? 'list' : rest[0] === 'compose' ? '' : rest[0]
  const openName = rest[0] === 'c' ? decodeURIComponent(rest[1] ?? '') : ''
  const openProject = rest[0] === 'compose' && rest[1] ? decodeURIComponent(rest[1]) : ''
  const list = useApi<{ items: Container[] }>(`/api/v1/hosts/${host}/containers`, [host])
  const projects = useApi<{ items: Project[] }>(tab === '' || openProject ? `/api/v1/hosts/${host}/compose` : null, [host, tab])
  useInterval(() => void list.reload(), 10000)
  useEvents(['change.done'], () => { void list.reload(); void projects.reload() })
  const reload = () => { void list.reload(); void projects.reload() }
  const back = (p: string) => nav(hostPath(host, 'containers' + (p ? '/' + p : '')))
  const project = projects.data?.items.find((p) => p.name === openProject)
  return (
    <div className="space-y-3">
      <TabBar label={t('nav.containers')} value={tab} items={TABS.map((x) => ({ value: x, label: t('containers.tab_' + (x || 'apps')), to: hostPath(host, 'containers' + (x ? '/' + x : '')) }))} />
      {list.error && <Notice tone="bad">{list.error.message}</Notice>}
      {tab === 'list' && <ContainerList list={list.data?.items ?? []} reload={reload} onLogs={(c) => nav(hostPath(host, 'containers/c/' + encodeURIComponent(c.name)) + '?tab=logs')} />}
      {tab === '' && <ComposeList list={projects.data?.items ?? []} reload={reload} />}
      {tab === 'updates' && <UpdatesTab />}
      {tab === 'images' && <ImagesTab />}
      {tab === 'networks' && <NetworksTab />}
      {tab === 'volumes' && <VolumesTab />}
      {openName && <ContainerDrawer name={openName} tab={new URLSearchParams(location.search).get('tab') === 'logs' ? 'logs' : 'info'} onClose={() => back('list')} onChanged={reload} />}
      {project && <ComposeDrawer project={project} onClose={() => back('')} onChanged={reload} />}
    </div>
  )
}
