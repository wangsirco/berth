import { Check, Cloud, ExternalLink, Fingerprint, KeyRound, Mail, ShieldAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Field'
import { get } from '@/lib/api'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { TokenDialog } from '@/pages/cloudflare/Welcome'
import type { AccessCheck } from './PublicAccess'
import type { Where } from './MainName'

// PanelTunnel: the Cloudflare choice, done right here. No Cloudflare account yet: connect
// one in place. One that can make tunnels: pick the name (panel.your-domain), whether the
// security entry is still asked for there, and Cloudflare Access (an email code first);
// one button does the rest. Already published: where, and how to stop.

interface Zone { id: string; name: string }
interface Account { id: string; name: string; zones?: Zone[]; features?: Record<string, boolean>; error?: string }
interface CF { accounts: Account[]; tunnel: { integration: string; name: string } | null }

export function PanelTunnel({ check, entry, onChanged }: { check: AccessCheck; entry: string; onChanged?: () => void }) {
  const t = useT()
  const run = useChange()
  const d = useApi<CF>('/api/v1/hosts/local/cloudflare')
  const where = useApi<Where>('/api/v1/access/where')
  const [tok, setTok] = useState<null | 'new' | { id: string; name: string }>(null)
  const [acct, setAcct] = useState('')
  const [zone, setZone] = useState('')
  const [label, setLabel] = useState('panel')
  const [needEntry, setNeedEntry] = useState(true)
  const [access, setAccess] = useState(true)
  const [emails, setEmails] = useState('')
  const done = () => { void d.reload(); void where.reload(); onChanged?.() }

  const accounts = d.data?.accounts ?? []
  const fixed = d.data?.tunnel?.integration // a tunnel there is: its account
  const cur = accounts.find((a) => a.id === (fixed || acct)) ?? accounts.find((a) => !a.error && a.features?.tunnel) ?? accounts[0]
  const zones = cur?.zones ?? []
  useEffect(() => { if (zones.length && !zones.some((z) => z.name === zone)) setZone(zones[0].name) }, [zones, zone])
  const canAccess = !!cur?.features?.access
  useEffect(() => { if (cur && !canAccess) setAccess(false) }, [cur, canAccess])

  const dialog = tok && <TokenDialog existing={tok === 'new' ? undefined : tok} newId={accounts.length ? 'cf' + (accounts.length + 1) : 'cf'} onClose={() => setTok(null)} onDone={done} />

  // published already
  if (check.tunnel_host) {
    const url = 'https://' + check.tunnel_host + (check.tunnel_entry && entry ? entry : '')
    return (
      <div className="space-y-2 rounded-[var(--radius-box)] border border-acc/40 bg-tint px-3.5 py-3 text-[12.5px]">
        <p className="flex items-center gap-1.5 font-semibold text-[13px]"><Check className="size-4 text-acc" />{t('ptun.live')}</p>
        <a className="num block break-all text-acc underline" href={url} target="_blank" rel="noreferrer">{url}</a>
        <ul className="space-y-0.5 text-mute">
          <li>{t(check.tunnel_access ? 'ptun.live_access' : 'ptun.live_no_access')}</li>
          <li className={check.tunnel_entry ? '' : 'text-warn'}>{t(check.tunnel_entry ? 'ptun.live_entry' : 'ptun.live_no_entry')}</li>
          <li>{t('ptun.live_2fa')}</li>
        </ul>
        {where.data && !(where.data.my_passkeys ?? []).includes(check.tunnel_host) && <AddPasskeyThere host={check.tunnel_host} others={where.data.my_passkeys ?? []} />}
        <Button size="sm" onClick={() => run('cf.tunnel.panel', { on: false }, { host: 'local', onDone: done })}>{t('ptun.stop')}</Button>
      </div>
    )
  }

  if (!d.data) return <p className="text-[12.5px] text-mute">{d.error ? d.error.message : t('ptun.loading')}</p>

  // no Cloudflare account yet: connect one here
  if (accounts.length === 0) {
    return (
      <div className="space-y-2.5 rounded-[var(--radius-box)] border border-line px-3.5 py-3 text-[12.5px]">
        <p className="text-[13px]">{t('ptun.need_account')}</p>
        <ol className="list-decimal space-y-0.5 pl-5 text-mute">
          <li>{t('ptun.how1')}</li><li>{t('ptun.how2')}</li><li>{t('ptun.how3')}</li>
        </ol>
        <Button size="sm" variant="primary" onClick={() => setTok('new')}><Cloud className="size-3.5" />{t('ptun.connect')}</Button>
        <p className="text-[12px] text-mute">{t('ptun.need_zone')}</p>
        {dialog}
      </div>
    )
  }

  // the account cannot do it (token broken, or without the tunnel permission)
  if (!cur || cur.error || (!fixed && !cur.features?.tunnel) || zones.length === 0) {
    const why = !cur || cur.error ? t('ptun.acct_error', { name: cur?.name ?? '' }) : zones.length === 0 ? t('ptun.no_zone') : t('ptun.no_tunnel_perm')
    return (
      <div className="space-y-2">
        <Notice tone="warn"><p>{why}</p>{cur && <Button size="sm" className="mt-2" onClick={() => setTok({ id: cur.id, name: cur.name })}><KeyRound className="size-3.5" />{t('ptun.replace_token')}</Button>}</Notice>
        {dialog}
      </div>
    )
  }

  const host = (label.trim().toLowerCase() || 'panel') + '.' + zone
  const mails = emails.split(/[\s,，;；]+/).map((x) => x.trim()).filter(Boolean)
  const badMail = mails.some((m) => !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(m))
  const ready = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(label.trim().toLowerCase() || 'panel') && !!zone && (!access || (mails.length > 0 && !badMail))
  const go = () => run('cf.tunnel.panel', { integration: cur.id, hostname: host, on: true, entry: needEntry, emails: access ? mails : [] }, { host: 'local', onDone: done })
  return (
    <div className="space-y-3 rounded-[var(--radius-box)] border border-line px-3.5 py-3 text-[12.5px]">
      <p className="text-[13px]">{t(fixed ? 'ptun.ready_tunnel' : 'ptun.ready', { name: cur.name })}</p>
      {!fixed && accounts.length > 1 && (
        <label className="block"><span className="mb-1 block font-semibold">{t('ptun.account')}</span>
          <Select value={cur.id} onChange={(e) => setAcct(e.target.value)}>{accounts.map((a) => <option key={a.id} value={a.id} disabled={!!a.error || !a.features?.tunnel}>{a.name}</option>)}</Select>
        </label>
      )}
      <div>
        <span className="mb-1 block font-semibold">{t('ptun.name')}</span>
        <div className="flex items-center gap-1.5">
          <Input className="num w-36" value={label} onChange={(e) => setLabel(e.target.value)} aria-label={t('ptun.name')} />
          <span className="text-mute">.</span>
          <Select className="num" value={zone} onChange={(e) => setZone(e.target.value)} aria-label={t('ptun.zone')}>{zones.map((z) => <option key={z.id} value={z.name}>{z.name}</option>)}</Select>
        </div>
        <p className="num mt-1 text-mute">https://{host}{needEntry && entry ? entry : ''}</p>
      </div>
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={access} disabled={!canAccess} onChange={(e) => setAccess(e.target.checked)} />
        <span><span className="inline-flex items-center gap-1 font-semibold"><Mail className="size-3.5 text-acc" />{t('ptun.access')}</span><span className="ml-1.5 text-[11.5px] text-acc">{t('pub.suggested')}</span>
          <span className="block text-mute">{canAccess ? t('ptun.access_d') : t('ptun.access_no_perm')}</span></span>
      </label>
      {access && (
        <div className="pl-6">
          <Input className="num" value={emails} onChange={(e) => setEmails(e.target.value)} placeholder="you@example.com" aria-label={t('ptun.emails')} />
          <p className="mt-1 text-mute">{badMail ? <span className="text-bad">{t('ptun.bad_email')}</span> : t('ptun.emails_d')}</p>
        </div>
      )}
      {!canAccess && <Button size="sm" variant="ghost" onClick={() => setTok({ id: cur.id, name: cur.name })}><KeyRound className="size-3.5" />{t('ptun.replace_token')}</Button>}
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={needEntry} onChange={(e) => setNeedEntry(e.target.checked)} />
        <span><span className="font-semibold">{t('ptun.entry')}</span>
          <span className="block text-mute">{entry ? t('ptun.entry_d', { entry }) : t('ptun.entry_none')}</span></span>
      </label>
      {!needEntry && !access && <Notice tone="warn"><span className="inline-flex items-start gap-1.5"><ShieldAlert className="mt-0.5 size-4 shrink-0 text-warn" />{t('ptun.open_warn')}</span></Notice>}
      <ul className="list-disc space-y-0.5 pl-5 text-mute">
        <li>{t(fixed ? 'ptun.step_tunnel_have' : 'ptun.step_tunnel')}</li>
        <li>{t('ptun.step_dns', { host })}</li>
        <li>{t('ptun.step_cert')}</li>
        <li>{t('ptun.step_2fa')}</li>
      </ul>
      <Button variant="primary" disabled={!ready} onClick={go}><ExternalLink className="size-3.5" />{t('ptun.go')}</Button>
      {dialog}
    </div>
  )
}

// AddPasskeyThere: Passkeys belong to a name — the ones on mini.lan do not sign in on the
// public name. Go there signed in (a one-time link) and add one, before it is needed outside.
export function AddPasskeyThere({ host, others }: { host: string; others: string[] }) {
  const t = useT()
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    const w = window.open('about:blank', '_blank')
    try {
      const h = await get<{ url: string }>('/api/v1/access/hop?to=public&path=' + encodeURIComponent('/account?passkey=add'))
      if (w) w.location.href = h.url
      else location.href = h.url
    } catch { w?.close() } finally { setBusy(false) }
  }
  return (
    <div className="rounded-[var(--radius-box)] border border-warn/50 bg-[color-mix(in_srgb,var(--warn)_8%,var(--canvas))] px-3 py-2.5" data-add-passkey-there>
      <p className="flex items-start gap-1.5 font-semibold text-[13px]"><Fingerprint className="mt-0.5 size-4 shrink-0 text-warn" />{t('ptun.pk_missing', { host })}</p>
      <p className="mt-1 text-mute">{others.length ? t('ptun.pk_missing_d', { names: others.join('、'), host }) : t('ptun.pk_none_d', { host })}</p>
      <Button size="sm" variant="primary" className="mt-2" disabled={busy} onClick={() => void go()}><Fingerprint className="size-3.5" />{t('ptun.pk_add', { host })}</Button>
    </div>
  )
}
