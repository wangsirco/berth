import { Link } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { hostPath } from '@/components/shell/host'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Field'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import type { AccessCheck } from './PublicAccess'

interface DomRoot { root: string; account?: string; zone_id?: string }
interface Ddns { id: number; name: string; type: string; last_ip?: string; last_error?: string; enabled: boolean }

const isIP = (v: string) => /^[\d.]+$/.test(v) || v.includes(':')

// The panel's internal and external addresses (Settings → Access). Commands other
// machines run, such as adding a host, use them. An external domain can follow this
// server's address through DDNS with one click.
export function PanelAddress({ lan, pub, savedPub, onChange, publicOnly }: { lan: string; pub: string; savedPub: string; onChange: (k: string, v: string) => void; publicOnly?: boolean }) {
  const t = useT()
  const run = useChange()
  const check = useApi<AccessCheck>('/api/v1/access/check')
  const domain = savedPub && !isIP(savedPub) ? savedPub.toLowerCase() : ''
  const ddns = useApi<{ items: Ddns[] }>(domain ? '/api/v1/ddns' : null, [domain])
  const doms = useApi<{ items: DomRoot[] }>(domain ? '/api/v1/hosts/local/domains/overview' : null, [domain])
  const entry = ddns.data?.items.find((d) => d.name === domain && d.type === 'A')
  const root = doms.data?.items.filter((r) => domain === r.root || domain.endsWith('.' + r.root)).sort((a, b) => b.root.length - a.root.length)[0]
  return (
    <>
      {!publicOnly && <Field label={t('addr.lan')} hint={t('addr.lan_hint')}>
        <Input className="num" value={lan} onChange={(e) => onChange('panel.lan_host', e.target.value.trim())} placeholder={check.data?.lan_ip ? `${check.data.lan_ip} / mini.lan` : 'mini.lan'} />
      </Field>}
      <Field label={publicOnly ? t('access.public_set') : t('addr.public')} hint={t('addr.public_hint')}>
        <Input className="num" value={pub} onChange={(e) => onChange('panel.public_host', e.target.value.trim())} placeholder={check.data?.public_ip ? `${check.data.public_ip} / panel.example.com` : 'panel.example.com'} />
        {domain && pub === savedPub && (
          <div className="mt-1.5 text-[12px]">
            {entry
              ? <span className={entry.last_error ? 'text-bad' : 'text-mute'}>{entry.last_error ? t('addr.ddns_err', { err: entry.last_error }) : t('addr.ddns_on', { ip: entry.last_ip || '…' })}</span>
              : root?.account && root.zone_id
                ? <span className="flex flex-wrap items-center gap-2 text-mute">{t('addr.ddns_offer')}<Button size="sm" onClick={() => run('ddns.set', { integration: root.account, zone_id: root.zone_id, name: domain, type: 'A', proxied: false, source: 'auto' }, { host: 'local', onDone: () => void ddns.reload() })}>{t('addr.ddns_btn')}</Button></span>
                : doms.data && <span className="text-mute">{t('addr.ddns_no_account')} <Link className="text-acc hover:underline" to={hostPath('local', 'domains/accounts')}>{t('domains.add_account')}</Link></span>}
          </div>
        )}
      </Field>
    </>
  )
}
