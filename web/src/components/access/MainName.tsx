import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { get, post } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { LocalNameDialog } from './LocalNameGuide'

// One name per panel: any other way in (an IP, the port, another name) moves to the main
// name once this device can open it, because Passkey belongs to a name. A one-time hop link
// carries the security entry and the sign-in across.

export interface Where { lan: string; lan_name: string; public: string; public_name: string; current: string; is_main: boolean; fallback: string; setup: boolean; renamed: string }

// reachable: the browser opens this panel at base (resolves the name, trusts the certificate)
export async function reachable(base: string, ms = 1500): Promise<boolean> {
  const c = new AbortController()
  const timer = setTimeout(() => c.abort(), ms)
  try { await fetch(base.replace(/\/$/, '') + '/api/v1/ca.crt', { mode: 'no-cors', cache: 'no-store', signal: c.signal }); return true } catch { return false } finally { clearTimeout(timer) }
}

const skipped = () => sessionStorage.getItem('berth.main-name.skip') === '1'

// GoToMainName: on the login page and in the panel; does nothing on the main or public name
export function GoToMainName({ onUnreachable }: { onUnreachable?: (w: Where) => void }) {
  useEffect(() => {
    let live = true
    void (async () => {
      const w = await get<Where>('/api/v1/access/where').catch(() => null)
      if (!w || w.setup || w.is_main || !w.lan || skipped()) return
      if (w.public_name && w.current === w.public_name) return // opened from outside by the public name: stay
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
export function NameBanners() {
  const t = useT()
  const nav = useNavigate()
  const [w, setW] = useState<Where | null>(null)
  const [unreach, setUnreach] = useState<Where | null>(null)
  const [guide, setGuide] = useState(false)
  useEffect(() => { void get<Where>('/api/v1/access/where').then(setW).catch(() => {}) }, [])
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
    </>
  )
}
