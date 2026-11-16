import { Navigate, useParams } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { TabBar } from '@/components/ui/Tabs'
import { useT } from '@/lib/i18n'
import { DnsTab } from '../sites/DnsTab'
import { MyDomains } from './MyDomains'

const TABS = ['', 'accounts', 'ddns'] as const

// Domains: my domains (expiry, where each name points, what uses it), DNS accounts with their
// records, and DDNS. Cloudflare's own features have a page of their own.
export default function DomainsPage() {
  const t = useT()
  const { host } = useHost()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  if (sub === 'cloudflare') return <Navigate to={hostPath(host, 'cloudflare')} replace />
  const tab = (TABS as readonly string[]).includes(sub) ? sub : ''
  return (
    <div className="space-y-4">
      <TabBar label={t('nav.domains')} value={tab} items={TABS.map((x) => ({ value: x, label: t('domains.tab_' + (x || 'mine')), to: hostPath(host, 'domains' + (x ? '/' + x : '')) }))} />
      {tab === '' && <MyDomains />}
      {tab !== '' && <DnsTab section={tab as 'accounts' | 'ddns'} />}
    </div>
  )
}
