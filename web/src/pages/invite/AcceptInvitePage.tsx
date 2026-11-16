import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { PermSummary, type Quota } from '@/components/invite/Invites'
import { Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Input, Label } from '@/components/ui/Field'
import { ApiError, get, post } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import type { User } from '@/lib/types'
import { AuthCard } from '../auth/AuthCard'

interface Info { panel: string; by: string; preset: string; role: string; perms: string[]; quota: Quota; simple: boolean; note: string; expires_at: number }

// /accept-invite#CODE: what the invitation gives, then a username and password.
export default function AcceptInvitePage() {
  const t = useT()
  const nav = useNavigate()
  const { signedIn } = useAuth()
  const code = location.hash.slice(1)
  const [info, setInfo] = useState<Info | null>(null)
  const [bad, setBad] = useState(false)
  const [f, setF] = useState({ username: '', display_name: '', password: '', again: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (!code || code === 'invalid') { setBad(true); return }
    get<Info>(`/api/v1/invite/${encodeURIComponent(code)}`).then(setInfo).catch(() => setBad(true))
  }, [code])
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value })
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (f.password !== f.again) { setErr(t('setup.mismatch')); return }
    setErr('')
    setBusy(true)
    try {
      const r = await post<{ user: User; csrf: string }>(`/api/v1/invite/${encodeURIComponent(code)}/accept`, { username: f.username, display_name: f.display_name, password: f.password })
      history.replaceState(null, '', '/accept-invite')
      signedIn(r.user, r.csrf)
      nav('/', { replace: true })
    } catch (e) {
      setErr((e as ApiError).message)
    } finally { setBusy(false) }
  }
  if (bad) return <AuthCard title={t('inv.bad_title')} sub={t('inv.bad_sub')}><p className="text-[13px] text-mute">{t('inv.bad_hint')}</p></AuthCard>
  if (!info) return <AuthCard title={t('common.loading')}><span /></AuthCard>
  return (
    <AuthCard title={t('inv.accept_title', { by: info.by, panel: info.panel || 'Berth' })} sub={info.note || t('inv.accept_sub')}>
      <div className="mb-3 rounded-[var(--radius-box)] border border-line bg-soft px-3.5 py-2.5 text-[12.5px]">
        <div className="mb-1 font-semibold">{t('inv.you_can', { what: t('inv.preset_' + info.preset) })}</div>
        <PermSummary role={info.role} perms={info.perms} quota={info.quota} simple={info.simple} />
      </div>
      <form onSubmit={submit} className="space-y-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <div><Label htmlFor="u" hint={t('setup.username_hint')}>{t('login.username')}</Label><Input id="u" value={f.username} onChange={set('username')} autoComplete="username" autoFocus /></div>
        <div><Label htmlFor="d">{t('setup.display_name')}</Label><Input id="d" value={f.display_name} onChange={set('display_name')} /></div>
        <div><Label htmlFor="p" hint={t('setup.password_hint')}>{t('login.password')}</Label><Input id="p" type="password" value={f.password} onChange={set('password')} autoComplete="new-password" /></div>
        <div><Label htmlFor="a">{t('setup.again')}</Label><Input id="a" type="password" value={f.again} onChange={set('again')} autoComplete="new-password" /></div>
        <Button type="submit" variant="primary" className="w-full" disabled={busy || !f.username || !f.password}>{t('inv.accept')}</Button>
      </form>
    </AuthCard>
  )
}
