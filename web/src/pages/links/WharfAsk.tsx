import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { del, errText, get, post } from '@/lib/api'
import { useInterval } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'
import { useReauth } from '../account/reauth'

// WharfAsk: ask the router's Wharf (found on the LAN) to pair, like Bluetooth — its admin
// allows on Wharf's page; both show the same six digits; Berth then finishes by itself.

interface Ask { state: '' | 'waiting' | 'refused' | 'paired' | 'expired' | 'error'; url?: string; sas?: string; expires_at?: number; error?: string; name?: string }

export function WharfAsk({ addr, onManual }: { addr: string; onManual: () => void }) {
  const t = useT()
  const reauth = useReauth()
  const [ask, setAsk] = useState<Ask>({ state: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const load = () => void get<Ask>('/api/v1/wharf/request').then((a) => { if (!a.url || a.url === addr.replace(/\/$/, '')) setAsk(a) }).catch(() => {})
  useEffect(load, [addr])
  useEvents(['wharf.request'], load)
  useInterval(load, ask.state === 'waiting' ? 2000 : null)
  const start = async () => {
    setErr(''); setBusy(true)
    try { setAsk(await reauth(() => post<Ask>('/api/v1/wharf/request', { addr }))) } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  const cancel = () => void del('/api/v1/wharf/request').then(() => setAsk({ state: '' }))
  const left = ask.expires_at ? Math.max(0, Math.round((ask.expires_at - Date.now() / 1000) / 60)) : 0
  return (
    <div className="w-full space-y-2" data-wharf-ask={ask.state || 'idle'}>
      {(ask.state === '' || ask.state === 'refused' || ask.state === 'expired' || ask.state === 'error') && <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" disabled={busy} onClick={() => void start()}>{t('links.wharf_ask')}</Button>
        <Button size="sm" variant="ghost" onClick={onManual}>{t('links.wharf_manual')}</Button>
        <span className="text-[12px] text-mute">{t('links.wharf_ask_hint')}</span>
      </div>}
      {ask.state === 'waiting' && <div className="rounded-lg bg-soft p-3">
        <div className="flex items-center gap-2 text-[12.5px]"><span className="size-2 animate-pulse rounded-full bg-acc" />{t('links.wharf_waiting', { min: left })}</div>
        <div className="mt-2 flex items-baseline gap-3"><ShieldCheck className="size-4 self-center text-acc" /><span className="text-[12px] text-mute">{t('links.wharf_sas')}</span><span className="num text-[22px] font-semibold tracking-widest" data-wharf-sas>{ask.sas}</span></div>
        <div className="mt-1 text-[12px] text-mute">{t('links.wharf_sas_hint')}</div>
        <Button size="sm" variant="ghost" className="mt-2" onClick={cancel}>{t('common.cancel')}</Button>
      </div>}
      {ask.state === 'paired' && <Notice tone="acc">{t('links.wharf_paired', { name: ask.name ?? 'Wharf' })} <Link className="underline" to="/local/wharf">{t('links.wharf_open')}</Link></Notice>}
      {ask.state === 'refused' && <Notice tone="warn">{t('links.wharf_refused')}</Notice>}
      {ask.state === 'expired' && <Notice tone="warn">{t('links.wharf_expired')}</Notice>}
      {ask.state === 'error' && <Notice tone="bad">{ask.error}</Notice>}
      {err && <Notice tone="bad">{err}</Notice>}
    </div>
  )
}
