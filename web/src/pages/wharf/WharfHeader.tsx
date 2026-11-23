import { useState } from 'react'
import { Globe, House, Repeat } from 'lucide-react'
import { OpenPeer } from '@/components/access/OpenPeer'
import { WharfMark } from '@/components/brand/WharfMark'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input } from '@/components/ui/Field'
import { errText, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import { ConnBadge, LiveLine } from '../links/LinksPage'
import type { WharfStatus } from '../network/NetworkPage'

// The paired Wharf at the top of its page: its two names (LAN / internet), which one this
// link uses, switching between them, and opening Wharf on the right one.
export function WharfHeader({ st, reload }: { st: WharfStatus; reload: () => void }) {
  const t = useT()
  const { user } = useAuth()
  const l = st.link!
  const mode = l.mode === 'public' ? 'public' : 'lan'
  const lan = l.peer_lan || (mode === 'lan' ? l.url : '')
  const pub = l.peer_public || (mode === 'public' ? l.url : '')
  const [sw, setSw] = useState(false)
  const name = (icon: React.ReactNode, label: string, u: string, on: boolean) => (
    <span className={'inline-flex items-center gap-1 ' + (on ? 'text-fg' : 'text-mute')} data-peer-name={label}>
      {icon}{label} <span className="num">{u || t('access.not_set')}</span>
    </span>
  )
  const sso = async (via: 'lan' | 'public') => (await post<{ url: string }>('/api/v1/wharf/sso', { return_to: '/', via })).url
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-[var(--radius-box)] border border-line bg-card px-4 py-3" data-wharf-header>
      <WharfMark className="size-8" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2 text-[14px] font-semibold">{t('wharfp.paired_with', { name: l.name })}
          <span className={'rounded-full bg-acc/15 px-2 py-0.5 text-[11.5px] font-medium text-acc' + (st.live?.online ? ' sr-only' : '')} data-link-mode={mode}>{t(mode === 'lan' ? 'linkmode.tag_lan' : 'linkmode.tag_public')}</span>
        </div>
        <div className="mt-0.5 flex flex-wrap gap-x-4 gap-y-0.5 text-[12px]">
          {name(<House className="size-3.5" />, t('access.lan'), lan, mode === 'lan')}
          {name(<Globe className="size-3.5" />, t('access.public'), pub, mode === 'public')}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-2"><ConnBadge live={st.live} /><LiveLine live={st.live} />
          {l.key_auth && <span className="inline-flex items-center gap-1 rounded-full bg-ok/15 px-2 py-0.5 text-[11.5px] text-ok" title={t('wharfp.key_auth_hint')} data-key-auth>{t('wharfp.key_auth')}</span>}
        </div>
      </div>
      {l.scopes.includes('sso')
        ? <OpenPeer id="wharf" lan={lan} pub={pub} open={sso} label={t('wharf.open')} />
        : <OpenPeer id="wharf" lan={lan} pub={pub} open={() => undefined} label={t('wharf.open')} />}
      {user?.role === 'super' && <Button size="sm" variant="ghost" onClick={() => setSw(true)}><Repeat className="size-3.5" />{t(mode === 'lan' ? 'linkmode.to_public' : 'linkmode.to_lan')}</Button>}
      {sw && <SwitchMode to={mode === 'lan' ? 'public' : 'lan'} known={mode === 'lan' ? pub : lan} onClose={() => setSw(false)} onDone={() => { setSw(false); reload() }} />}
    </div>
  )
}

function SwitchMode({ to, known, onClose, onDone }: { to: 'lan' | 'public'; known: string; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const [u, setU] = useState(known)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true); setErr('')
    try { await post('/api/v1/wharf/mode', { mode: to, url: /^https:\/\//.test(u) ? u : 'https://' + u }); onDone() } catch (e) { setErr(errText(e)) } finally { setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t(to === 'lan' ? 'linkmode.to_lan' : 'linkmode.to_public')}
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" className="ml-auto" disabled={!u || busy} onClick={() => void go()}>{t('linkmode.switch')}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]">
        <p className="text-mute">{t(to === 'lan' ? 'linkmode.switch_lan_d' : 'linkmode.switch_public_d')}</p>
        {err && <Notice tone="bad">{err}</Notice>}
        <Field label={t(to === 'lan' ? 'linkmode.wharf_lan' : 'linkmode.wharf_public')}><Input className="num" value={u} onChange={(e) => setU(e.target.value.trim())} placeholder={to === 'lan' ? 'https://wharf.lan' : 'https://wharf.example.com'} /></Field>
      </div>
    </Dialog>
  )
}
