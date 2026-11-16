import { useParams } from 'react-router-dom'
import { hostPath, useHost } from '@/components/shell/host'
import { TabBar } from '@/components/ui/Tabs'
import { useT } from '@/lib/i18n'
import { CloudflareTab } from '../network/NetworkPage'

const TABS = ['', 'zones', 'access', 'protect'] as const

// Cloudflare: tunnel, zones and attack mode, Access, ban sync. Accounts are added under 域名 → DNS 账号.
export default function CloudflarePage() {
  const t = useT()
  const { host } = useHost()
  const sub = (useParams()['*'] ?? '').split('/')[0]
  const tab = (TABS as readonly string[]).includes(sub) ? sub : ''
  return (
    <div className="space-y-4">
      <TabBar label="Cloudflare" value={tab} items={TABS.map((x) => ({ value: x, label: t('cf.tab_' + (x || 'tunnel')), to: hostPath(host, 'cloudflare' + (x ? '/' + x : '')) }))} />
      <CloudflareTab section={(tab || 'tunnel') as 'tunnel' | 'zones' | 'access' | 'protect'} />
    </div>
  )
}
