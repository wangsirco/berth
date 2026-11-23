import { Cloud, Globe, House, Network } from 'lucide-react'
import { useState } from 'react'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { Dialog } from '@/components/ui/Dialog'
import { post, put } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { PanelTunnel } from './PanelTunnel'

// How people reach the panel from outside. Most cloud servers are only reachable over
// the internet, so the installer and the welcome wizard both ask; Settings can change
// it later. GET /api/v1/access/check says what this machine looks like from outside.

export interface AccessCheck {
  public: boolean
  entry: boolean
  port: string
  public_ip: string
  public_ipv6: string
  on_interface: boolean
  lan_ip: string
  client_ip: string
  client_public: boolean
  firewalld: boolean
  port_open: boolean
  tailscale?: string
  tunnel_host?: string
  tunnel_entry?: boolean
  tunnel_access?: boolean
}

export type AccessChoice = 'public' | 'tailscale' | 'cloudflare' | 'lan'

export const usePublicCheck = () => useApi<AccessCheck>('/api/v1/access/check')

// suggest: what most people in this situation want.
export function suggestChoice(c: AccessCheck): AccessChoice {
  if (c.public) return 'public'
  if (c.client_public) return 'public' // they are already coming in over the internet
  if (c.tailscale) return 'tailscale'
  if (c.public_ip && c.on_interface) return 'public' // a cloud server with its address on the NIC
  return 'lan'
}

// applyPublicAccess turns public access on or off. On: the setting (with the entry in the
// same change, since public access needs one) and, when firewalld blocks the panel port,
// an allow rule confirmed at once: opening a port cannot lock anyone out.
export async function applyPublicAccess(on: boolean, c: AccessCheck, extra: Record<string, string> = {}, noEntry = false) {
  await put('/api/v1/settings' + (noEntry ? '?no_entry=1' : ''), { values: { ...extra, 'access.public': on ? '1' : '' } })
  if (on && c.firewalld && !c.port_open) {
    await post('/api/v1/hosts/local/changes/firewall.rule.set', { action: 'allow', proto: 'tcp', ports: c.port, source: 'any', comment: 'Berth panel (public access)', enabled: true })
    await post('/api/v1/hosts/local/changes/firewall.confirm', {})
  }
}

const ICONS = { public: Globe, tailscale: Network, cloudflare: Cloud, lan: House } as const

export function PublicAccessPicker({ check, choice, onChoice, entry, onTunnel }: { check: AccessCheck; choice: AccessChoice; onChoice: (c: AccessChoice) => void; entry: string; onTunnel?: () => void }) {
  const t = useT()
  const host = check.public_ip || location.hostname
  return (
    <div className="space-y-3">
      <div className="rounded-[var(--radius-box)] border border-line bg-soft px-3.5 py-2.5 text-[12.5px] leading-relaxed">
        {check.public_ip
          ? <div>{t(check.on_interface ? 'pub.detect_direct' : 'pub.detect_nat', { ip: check.public_ip })}</div>
          : <div>{t('pub.detect_none')}</div>}
        {check.public_ipv6 && <div>{t('pub.detect_v6', { ip: check.public_ipv6 })}</div>}
        <div className="text-mute">{t(check.client_public ? 'pub.you_public' : 'pub.you_private', { ip: check.client_ip })}</div>
      </div>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {(['public', 'tailscale', 'cloudflare', 'lan'] as const).map((k) => {
          const Icon = ICONS[k]
          return (
            <button key={k} type="button" aria-pressed={choice === k} onClick={() => onChoice(k)} className={cx('flex gap-2.5 rounded-[var(--radius-box)] border p-2.5 text-left text-[13px]', choice === k ? 'border-acc bg-tint' : 'border-line hover:border-acc')}>
              <Icon className="mt-0.5 size-4 shrink-0 text-acc" aria-hidden />
              <span>
                <span className="block font-semibold">{t('pub.opt_' + k)}{k === suggestChoice(check) && <span className="ml-1.5 text-[11.5px] font-normal text-acc">{t('pub.suggested')}</span>}</span>
                <span className="block text-[12px] text-mute">{t('pub.opt_' + k + '_d')}</span>
              </span>
            </button>
          )
        })}
      </div>
      {choice === 'public' && (
        <div className="space-y-2 text-[12.5px]">
          {!check.public_ip && <Notice tone="warn">{t('pub.no_ip_warn')}</Notice>}
          <p>{t('pub.url')} <span className="num font-semibold">https://{host}:{check.port}{entry}</span></p>
          <ul className="list-disc space-y-1 pl-5 text-mute">
            <li>{t(entry ? 'pub.need_entry_ok' : 'pub.need_entry', { entry })}</li>
            <li>{t(check.firewalld ? (check.port_open ? 'pub.fw_open' : 'pub.fw_will_open') : 'pub.fw_none', { port: check.port })}</li>
            <li>{t(check.on_interface ? 'pub.sg_direct' : 'pub.sg_nat', { port: check.port })}</li>
            <li className="text-warn">{t('pub.need_2fa')}</li>
          </ul>
        </div>
      )}
      {choice === 'tailscale' && (
        <div className="space-y-2 text-[12.5px]">
          {check.tailscale
            ? <p>{t('pub.ts_here')} <span className="num font-semibold">https://{check.tailscale}:{check.port}{entry}</span></p>
            : <>
                <p>{t('pub.ts_steps')}</p>
                <pre className="num overflow-x-auto rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12px]">curl -fsSL https://tailscale.com/install.sh | sh{'\n'}sudo tailscale up</pre>
                <p className="text-mute">{t('pub.ts_then')} <a className="text-acc underline" href="https://tailscale.com/download" target="_blank" rel="noreferrer">tailscale.com/download</a></p>
              </>}
          <p className="text-mute">{t('pub.ts_allowed')}</p>
        </div>
      )}
      {choice === 'cloudflare' && <PanelTunnel check={check} entry={entry} onChanged={onTunnel} />}
      {choice === 'lan' && <p className="text-[12.5px] text-mute">{t('pub.lan_note', { ip: check.lan_ip || '—', port: check.port })}</p>}
    </div>
  )
}

// PublicAccessSettings: the same choice in Settings, applied with one button.
export function PublicAccessSettings({ entry, onSaved }: { entry: string; onSaved: () => void }) {
  const t = useT()
  const check = usePublicCheck()
  const [choice, setChoice] = useState<AccessChoice | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [ask, setAsk] = useState(false)
  if (!check.data) return <p className="text-[13px] text-mute">{check.error ? check.error.message : t('pub.checking')}</p>
  const c = check.data
  const cur = choice ?? (c.public ? 'public' : c.tunnel_host ? 'cloudflare' : suggestChoice(c) === 'public' ? 'lan' : suggestChoice(c))
  const turnOn = cur === 'public' && !c.public
  const turnOff = cur !== 'public' && c.public
  const apply = async () => {
    if (turnOn && !entry && !ask) { setAsk(true); return }
    setAsk(false)
    setBusy(true)
    setErr('')
    try {
      await applyPublicAccess(turnOn, c, {}, turnOn && !entry)
      await check.reload()
      setChoice(null)
      onSaved()
    } catch (e) {
      setErr((e as Error).message)
    } finally { setBusy(false) }
  }
  return (
    <div className="space-y-2">
      <p className="text-[13px]">{c.tunnel_host && !c.public ? t('pub.state_tunnel', { host: c.tunnel_host }) : t(c.public ? 'pub.state_on' : 'pub.state_off')}</p>
      <PublicAccessPicker check={c} choice={cur} onChoice={setChoice} entry={entry} onTunnel={() => { void check.reload(); onSaved() }} />
      {err && <Notice tone="bad">{err}</Notice>}
      {turnOn && !entry && <Notice tone="warn">{t('pub.no_entry_warn')}</Notice>}
      {(turnOn || turnOff) && <Button variant={turnOn ? 'primary' : 'default'} disabled={busy} onClick={() => void apply()}>{t(turnOn ? 'pub.turn_on' : 'pub.turn_off')}</Button>}
      {ask && <NoEntryConfirm public onCancel={() => setAsk(false)} onConfirm={() => void apply()} />}
    </div>
  )
}

// NoEntryConfirm: going without a security entry, asked a second time with what it means
export function NoEntryConfirm({ public: pub, onCancel, onConfirm }: { public?: boolean; onCancel: () => void; onConfirm: () => void }) {
  const t = useT()
  const [ok, setOk] = useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onCancel()} title={t('noentry.title')}
      footer={<><Button className="ml-auto" onClick={onCancel}>{t('common.cancel')}</Button><Button variant="danger" disabled={!ok} onClick={onConfirm}>{t('noentry.confirm')}</Button></>}>
      <div className="space-y-2.5 px-4 py-3 text-[13px]">
        <Notice tone="bad">{t(pub ? 'noentry.risk_public' : 'noentry.risk')}</Notice>
        <ul className="list-disc space-y-1 pl-5 text-[12.5px] text-mute">
          <li>{t('noentry.p1')}</li><li>{t('noentry.p2')}</li><li>{t('noentry.p3')}</li>
        </ul>
        <label className="flex items-center gap-2 text-[12.5px]"><input type="checkbox" checked={ok} onChange={(e) => setOk(e.target.checked)} />{t('noentry.understand')}</label>
      </div>
    </Dialog>
  )
}
