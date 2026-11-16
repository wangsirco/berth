import { useParams } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { TabBar } from '@/components/ui/Tabs'
import { can, useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import { ProjectsTab, Runtimes, type Lang } from '../sites/ProjectsTab'

const TABS = ['', 'node', 'python', 'go', 'java', 'other', 'runtimes'] as const

// Projects, by language like 宝塔's「Node 项目 / Python 项目…」: one list whichever way each one runs,
// and the language versions on a tab of their own.
export default function ProjectsPage() {
  const t = useT()
  const { host } = useHost()
  const { user } = useAuth()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  const tab = (TABS as readonly string[]).includes(sub) ? sub : ''
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.projects')} value={tab} items={TABS.map((x) => ({ value: x, label: t('projects.tab_' + (x || 'all')), to: hostPath(host, 'projects' + (x ? '/' + x : '')) }))} />
      {tab === 'runtimes' ? <Runtimes manage={can(user, 'sites.manage')} /> : <ProjectsTab key={tab} lang={tab as Lang} />}
    </div>
  )
}
