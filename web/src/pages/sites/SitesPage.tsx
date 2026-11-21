import { useState } from 'react'
import { ImportDialog } from '@/components/import/ImportDialog'
import { PhpTab } from './PhpTab'
import { Navigate, useParams } from 'react-router-dom'
import { Spark } from '@/components/charts/Spark'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath, useHost } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Box, Empty, Notice } from '@/components/ui/Box'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { ReauthProvider } from '../account/reauth'
import { CertsTab } from './CertsTab'
import { NewSiteDialog } from './NewSiteDialog'
import { SitePage } from './SitePage'
import { SiteList } from './SiteList'
import type { Stats } from './StatsView'
import { type Site } from './types'
import { TabBar } from '@/components/ui/Tabs'

const TABS = ['', 'php', 'certs'] as const

export function SiteSpark({ host, id }: { host: string; id: number }) {
  const s = useApi<Stats>(`/api/v1/hosts/${host}/sites/${id}/stats?range=24h`, [id])
  if (!s.data) return <span className="w-[110px]" />
  return (
    <span className="flex items-center gap-2">
      <Spark values={s.data.series.map((p) => p.req)} label={String(s.data.total.req)} />
      <span className="num w-12 text-right text-[12px] text-mute">{s.data.total.req}</span>
    </span>
  )
}

export function useSites(host: string) {
  return useApi<{ items: Site[] }>(`/api/v1/hosts/${host}/sites`, [host])
}

export default function SitesPage() {
  const t = useT()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const rest = (useParams()['*'] ?? '').split('/')
  const tab = rest[0] === 's' ? '' : rest[0]
  const openId = rest[0] === 's' ? Number(rest[1]) : 0
  const sites = useSites(host)
  // projects and DNS have pages of their own now
  const moved = tab === 'projects' ? 'projects' : tab === 'dns' ? 'domains' : ''
  const edge = useApi<{ installed: boolean; running: boolean; image: string; outdated?: boolean; running_image?: string }>(`/api/v1/hosts/${host}/edge`, [host])
  const [creating, setCreating] = useState(() => new URLSearchParams(location.search).get('new') === '1')
  const [importing, setImporting] = useState(false)
  useEvents(['change.done'], () => { void sites.reload(); void edge.reload() })
  const manage = can(user, 'sites.manage')
  const reload = () => { void sites.reload(); void edge.reload() }
  if (moved) return <Navigate to={hostPath(host, moved)} replace />
  if (openId > 0) return <ReauthProvider><SitePage id={openId} onChanged={reload} /></ReauthProvider>
  return (
    <ReauthProvider>
      <div className="space-y-3">
        <TabBar label={t('nav.sites')} value={rest[0] === 's' ? '' : tab} items={TABS.map((x) => ({ value: x, label: t('sites.tab_' + (x || 'sites')), to: hostPath(host, 'sites' + (x ? '/' + x : '')) }))}
          right={tab === '' && manage && edge.data?.running && <>
            {user?.role === 'super' && <Button size="sm" onClick={() => setImporting(true)}>{t('import.button')}</Button>}
            <Button size="sm" variant="primary" onClick={() => setCreating(true)}>＋ {t('sites.new_title')}</Button>
          </>} />
        <div>
          {importing && <ImportDialog onClose={() => { setImporting(false); void sites.reload() }} />}
        </div>
        {tab === '' && <>
          {edge.data && !edge.data.running && (
            <Notice tone="warn">
              <div className="flex flex-wrap items-center gap-2">
                <span>{edge.data.installed ? t('sites.edge_stopped') : t('sites.edge_missing')}</span>
                {manage && <Button size="sm" variant="primary" onClick={() => run('edge.install', {}, { host, onDone: reload })}>{t('action.edge.install')}</Button>}
              </div>
            </Notice>
          )}
          {edge.data?.running && edge.data.outdated && (
            <Notice>
              <div className="flex flex-wrap items-center gap-2">
                <span>{t('sites.edge_outdated', { from: edge.data.running_image ?? '', to: edge.data.image })}</span>
                {manage && <Button size="sm" onClick={() => run('edge.upgrade', {}, { host, onDone: reload })}>{t('action.edge.upgrade')}</Button>}
              </div>
            </Notice>
          )}
          {sites.error && <Notice tone="bad">{sites.error.message}</Notice>}
          {sites.data?.items.length === 0 ? <Box title={t('nav.sites')} count={0}><Empty title={t('sites.none')} action={manage && edge.data?.running ? <Button variant="primary" onClick={() => setCreating(true)}>{t('sites.new_title')}</Button> : undefined}>{t('sites.none_hint')}</Empty></Box>
            : sites.data && <SiteList host={host} sites={sites.data.items} />}
          {edge.data?.running && <p className="text-[12px] text-mute">{t('sites.edge_note', { image: edge.data.image })}</p>}
        </>}
        {tab === 'php' && <PhpTab />}
        {tab === 'certs' && <CertsTab />}
        {creating && <NewSiteDialog onClose={() => setCreating(false)} onDone={reload} />}
      </div>
    </ReauthProvider>
  )
}
