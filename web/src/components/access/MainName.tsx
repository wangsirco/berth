import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { get, post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { LocalNameDialog } from './LocalNameGuide'

// One name per panel: any other way in (an IP, the port, another name) moves to the main
// name once this device can open it, because Passkey belongs to a name. A one-time hop link
// carries the security entry and the sign-in across.

export interface Where { lan: string; lan_name: string; public: string; public_name: string; current: string; is_main: boolean; fallback: string; setup: boolean; renamed: string; passkeys_here: number; passkeys_main: number; old_name: string; adopt_on_pair: boolean; my_passkeys?: string[] }

// reachable: the browser opens this panel at base (resolves the name, trusts the certificate)
export async function reachable(base: string, ms = 1500): Promise<boolean> {
  const c = new AbortController()
  const timer = setTimeout(() => c.abort(), ms)
  try { await fetch(base.replace(/\/$/, '') + '/api/v1/ca.crt', { mode: 'no-cors', cache: 'no-store', signal: c.signal }); return true } catch { return false } finally { clearTimeout(timer) }
}

const skipped = () => {
  if (new URLSearchParams(location.search).get('stay') === '1') sessionStorage.setItem('berth.main-name.skip', '1') // came back on purpose
  return sessionStorage.getItem('berth.main-name.skip') === '1'
}

// Passkeys live on this name and not yet on the main one: moving first would leave them behind
export const keysLeftBehind = (w: Where) => w.passkeys_here > 0 && w.passkeys_main === 0

// GoToMainName: on the login page and in the panel; does nothing on the main or public name.
// signedIn=false (the login page): waits when the Passkeys are here (onWait), so the person
// signs in with them first; afterwards the move dialog takes them over.
export function GoToMainName({ onUnreachable, onWait, signedIn = true }: { onUnreachable?: (w: Where) => void; onWait?: (w: Where) => void; signedIn?: boolean }) {
  useEffect(() => {
    let live = true
    void (async () => {
      const w = await get<Where>('/api/v1/access/where').catch(() => null)
      if (!w || w.setup || w.is_main || !w.lan || skipped()) return
      if (keysLeftBehind(w)) { if (live) onWait?.(w); return } // sign in here first (login), or the move dialog (panel)
      if (signedIn && w.renamed) return // the move dialog explains first
      if (w.public_name && w.current === w.public_name) return // opened from outside by the public name: stay
      if (new URL(w.lan).hostname === location.hostname) return // already on it (whatever the server saw): never loop
      if (!(await reachable(w.lan))) { if (live) onUnreachable?.(w); return }
      const h = await get<{ url: string }>('/api/v1/access/hop?to=lan&path=' + encodeURIComponent(location.pathname + location.search)).catch(() => null)
      if (live && h?.url) location.replace(h.url)
    })()
    return () => { live = false }
  }, [])
  return null
}

// NameBanners: in the panel — the name changed (add the Passkey again), or this device does
// not open the main name yet
// RenamedNow: right after pairing — wait for the new name, then say so and offer to go there
export function RenamedNow() {
  const [w, setW] = useState<Where | null>(null)
  useEffect(() => {
    let n = 0
    const timer = setInterval(() => {
      void get<Where>('/api/v1/access/where').then((x) => { if (x.renamed) { setW(x); clearInterval(timer) } }).catch(() => {})
      if (++n > 20) clearInterval(timer)
    }, 1500)
    return () => clearInterval(timer)
  }, [])
  if (!w || skipped()) return null
  return <MoveDialog w={w} onClose={() => setW(null)} />
}

// MoveDialog: the panel got a new name — what that means, and going over with a Passkey
function MoveDialog({ w, onClose }: { w: Where; onClose: () => void }) {
  const t = useT()
  const [oldName, newName] = (w.renamed || `${w.current}>${w.lan_name}`).split('>')
  const go = async () => {
    void post('/api/v1/access/renamed/seen', {})
    const h = await get<{ url: string }>('/api/v1/access/hop?to=lan&path=' + encodeURIComponent('/account?passkey=add')).catch(() => null)
    location.href = h?.url ?? w.lan
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('move.title', { name: newName })}
      footer={<><Button variant="ghost" onClick={onClose}>{t('common.later')}</Button><Button variant="primary" className="ml-auto" onClick={() => void go()} data-move-go>{t('move.go', { name: newName })}</Button></>}>
      <div className="space-y-3 px-4 py-3 text-[13px]" data-move-dialog>
        <p>{t('move.why', { old: oldName, name: newName })}</p>
        <ol className="list-decimal space-y-1.5 pl-5">
          <li>{t('move.s1', { url: w.lan })}</li>
          <li>{t('move.s2')}</li>
          <li>{t('move.s3', { old: oldName })}</li>
        </ol>
        <p className="text-[12px] text-mute">{t('move.bookmark', { url: w.lan })}</p>
      </div>
    </Dialog>
  )
}

export function NameBanners() {
  const t = useT()
  const nav = useNavigate()
  const [w, setW] = useState<Where | null>(null)
  const [unreach, setUnreach] = useState<Where | null>(null)
  const [guide, setGuide] = useState(false)
  const [move, setMove] = useState(false)
  useEffect(() => { void get<Where>('/api/v1/access/where').then((x) => { setW(x); if (!x.is_main && x.lan && (x.renamed || keysLeftBehind(x)) && !skipped()) setMove(true) }).catch(() => {}) }, [])
  const [oldName, newName] = (w?.renamed ?? '').split('>')
  const seen = () => { void post('/api/v1/access/renamed/seen', {}); setW((x) => x && { ...x, renamed: '' }) }
  const bar = 'mb-4 flex flex-wrap items-center gap-2 rounded-[var(--radius-box)] border px-3.5 py-2.5 text-[13px]'
  return (
    <>
      <GoToMainName onUnreachable={setUnreach} />
      {w?.renamed && newName && <div role="status" data-renamed className={bar + ' border-acc bg-acc/10'}>
        <span>{t('access.renamed', { old: oldName, name: newName })}</span>
        <Button size="sm" variant="primary" className="ml-auto" onClick={() => { seen(); nav('/account') }}>{t('access.add_passkey')}</Button>
        <Button size="sm" variant="ghost" onClick={seen}>{t('common.got_it')}</Button>
      </div>}
      {unreach && !skipped() && <div role="status" data-unreachable className={bar + ' border-line bg-soft'}>
        <span>{t('access.unreachable', { url: unreach.lan })}</span>
        <Button size="sm" className="ml-auto" onClick={() => setGuide(true)}>{t('access.fix_device')}</Button>
        <Button size="sm" variant="ghost" onClick={() => { sessionStorage.setItem('berth.main-name.skip', '1'); setUnreach(null) }}>{t('common.later')}</Button>
      </div>}
      {guide && <LocalNameDialog onClose={() => setGuide(false)} />}
      {move && w && <MoveDialog w={w} onClose={() => { sessionStorage.setItem('berth.main-name.skip', '1'); setMove(false) }} />}
    </>
  )
}

// LoginNameNotice: on the login page — wait here for Passkeys (old name), or go back to them
// (new name without any yet)
export function LoginNameNotice() {
  const t = useT()
  const [wait, setWait] = useState<Where | null>(null)
  const [w, setW] = useState<Where | null>(null)
  useEffect(() => { void get<Where>('/api/v1/access/where').then(setW).catch(() => {}) }, [])
  const back = w && w.is_main && w.passkeys_main === 0 && w.old_name
  return (
    <>
      <GoToMainName signedIn={false} onWait={setWait} />
      {wait && <div className="rounded-[var(--radius-box)] border border-acc bg-acc/10 px-3 py-2 text-[12.5px]" data-login-wait>{t('move.login_here', { url: wait.lan })}</div>}
      {back && <div className="rounded-[var(--radius-box)] border border-line bg-soft px-3 py-2 text-[12.5px]" data-login-back>
        {t('move.no_keys_here', { old: w.old_name })}{' '}
        <a className="font-semibold text-acc hover:underline" href={`https://${w.old_name}${location.port ? ':' + location.port : ''}/login?stay=1`}>{t('move.back_to', { old: w.old_name })}</a>
      </div>}
    </>
  )
}
